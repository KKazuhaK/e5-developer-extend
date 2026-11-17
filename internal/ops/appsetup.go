package ops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/appsetup"
	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
	"github.com/KKazuhaK/e5-developer-extend/internal/version"
)

// ErrBootstrapBlocked means the tenant does not let Microsoft Graph Command
// Line Tools sign in with the permissions automatic setup needs.
var ErrBootstrapBlocked = errors.New("the tenant does not allow signing in with Microsoft Graph Command Line Tools")

// AppSetupOptions describes one automatic app registration.
type AppSetupOptions struct {
	// AppOnly sets up the client-secret identity instead of the delegated one.
	AppOnly bool `json:"appOnly"`
	// Method is device_code (default) or browser.
	Method string `json:"method"`
	// Tenant is "organizations" (default) or a tenant ID or domain.
	Tenant string `json:"tenant"`
	// Account names the account whose vault entry receives the client secret
	// (AppOnly). The account does not have to exist yet.
	Account string `json:"account"`
	// Repair adds the missing permissions (and consent) to the app registration
	// the existing account Account signs in with, and nothing else: the
	// tenant, the kind of identity and the app come from that account, no
	// account is added and no client secret is created, stored or deleted.
	// Setup refuses to touch any other app registration.
	Repair bool `json:"repair"`
}

// AppSetupSession is a signed-in bootstrap session. The administrator's token
// lives only in this object (in memory) until Close or until it is dropped.
// Call Plan, show it, then Apply with the user's choices.
type AppSetupSession struct {
	o      *Ops
	opts   AppSetupOptions
	setup  *appsetup.Setup
	admin  string
	tenant string
	// repair is the account being repaired, nil outside repair.
	repair *config.Account

	// mu guards the administrator's token: operations hold it shared while
	// they use it, Close takes it exclusively before dropping the token.
	mu     sync.RWMutex
	closed bool
}

var errSessionClosed = errors.New("the app setup session is closed")

// enter marks the start of an operation that uses the token; call the
// returned function when it ends.
func (s *AppSetupSession) enter() (func(), error) {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, errSessionClosed
	}
	return s.mu.RUnlock, nil
}

// Admin is the sign-in name of the administrator, for the user to confirm.
func (s *AppSetupSession) Admin() string { return s.admin }

// TenantID is the tenant the administrator signed in to.
func (s *AppSetupSession) TenantID() string { return s.tenant }

// Close drops the administrator's token. It waits for operations in flight
// (they are short once their context is cancelled); later ones fail.
func (s *AppSetupSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.setup != nil {
		s.setup.Token = ""
	}
}

// Scopes returns the Graph permissions the bootstrap sign-in asks for.
func AppSetupScopes(appOnly bool) []string {
	// Each identity asks only for the grant permission it needs.
	if appOnly {
		return []string{"Application.ReadWrite.All", "AppRoleAssignment.ReadWrite.All"}
	}
	return []string{"Application.ReadWrite.All", "DelegatedPermissionGrant.ReadWrite.All"}
}

func (o *Ops) bootstrap(ctx context.Context, method, tenant string, scopes []string, ui auth.LoginUI, c *http.Client) (auth.Token, error) {
	if o.Bootstrap != nil {
		return o.Bootstrap(ctx, method, tenant, auth.MicrosoftGraphCLIClientID, scopes, ui, c)
	}
	return auth.Bootstrap(ctx, method, tenant, auth.MicrosoftGraphCLIClientID, scopes, ui, c)
}

// BeginAppSetup asks the administrator to sign in (device code or browser) and
// returns a session. progress may be nil.
func (o *Ops) BeginAppSetup(ctx context.Context, opts AppSetupOptions, ui auth.LoginUI, progress func(string)) (*AppSetupSession, error) {
	if opts.Method == "" {
		opts.Method = config.AuthDeviceCode
	}
	if opts.Method != config.AuthDeviceCode && opts.Method != config.AuthBrowser {
		return nil, fmt.Errorf("automatic setup signs in with %s or %s, not %q", config.AuthDeviceCode, config.AuthBrowser, opts.Method)
	}
	var repair *config.Account
	if opts.Repair {
		cfg, _, err := o.LoadConfig()
		if err != nil {
			return nil, err
		}
		acct, err := o.FindAccount(cfg, opts.Account)
		if err != nil {
			return nil, err
		}
		cp := *acct
		repair = &cp
		opts.Tenant, opts.AppOnly = acct.Tenant, acct.Auth == config.AuthClientSecret
	}
	if opts.Tenant == "" {
		opts.Tenant = "organizations"
	}
	if opts.AppOnly && !opts.Repair {
		if err := ValidateAccount(config.Account{Name: opts.Account, Auth: config.AuthClientSecret, Tenant: "x", ClientID: testClientID}); err != nil {
			return nil, fmt.Errorf("--account: %w", err)
		}
	}
	proxy := ""
	if cfg, _, err := o.LoadConfigOrDefault(); err == nil {
		proxy = cfg.Network.Proxy
	}
	httpClient := auth.NewHTTPClient(proxy)

	tok, err := o.bootstrap(ctx, opts.Method, opts.Tenant, AppSetupScopes(opts.AppOnly), ui, httpClient)
	if err != nil {
		return nil, explainBootstrap(err)
	}
	host, _ := os.Hostname()
	if o.SetupHostname != "" {
		host = o.SetupHostname
	}
	kind := appsetup.KindDelegated
	if opts.AppOnly {
		kind = appsetup.KindApp
	}
	s := &AppSetupSession{o: o, opts: opts, admin: tok.Account, tenant: tok.TenantID, repair: repair}
	s.setup = &appsetup.Setup{
		Kind: kind, Token: tok.AccessToken, TenantID: tok.TenantID, Base: o.GraphBase, HTTP: httpClient,
		Version: version.Version, Hostname: host, Account: opts.Account, Progress: progress, Backoff: o.SetupBackoff,
	}
	if opts.Repair {
		s.setup.NoSecret = true
	} else if opts.AppOnly {
		s.setup.StoreSecret = s.storeSecret
		s.setup.BeforeSecret = s.checkSecretTarget
		s.setup.LegacyOwner = s.soleAccountOf
	}
	return s, nil
}

const testClientID = "00000000-0000-0000-0000-000000000000"

// explainBootstrap turns a failed administrator sign-in into advice.
func explainBootstrap(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	code := auth.ErrorCode(err)
	switch code {
	case "AADSTS53003", "AADSTS65001", "AADSTS90094", "AADSTS650056", "AADSTS650057", "AADSTS65002",
		"AADSTS500200", "AADSTS50105", "AADSTS7000112", "AADSTS700016":
		return fmt.Errorf("%w (%s)\nUse the manual setup instead: %s", ErrBootstrapBlocked, code, appsetup.Manual)
	}
	return fmt.Errorf("administrator sign-in failed: %w", err)
}

// checkSecretTarget refuses to write secret/<account> over a vault entry that
// belongs to something else: the account must not exist yet, or be a
// client_secret account for this very tenant and app. Otherwise the new secret
// would replace a password or the secret of another app.
func (s *AppSetupSession) checkSecretTarget(app appsetup.AppRef) error {
	cfg, _, err := s.o.LoadConfigOrDefault()
	if err != nil {
		return err
	}
	acct, exists := cfg.Find(s.opts.Account)
	if !exists {
		return nil
	}
	name := s.opts.Account
	switch {
	case acct.Auth != config.AuthClientSecret:
		return fmt.Errorf("account %q already exists and signs in with %s, so its vault entry is not a client secret; "+
			"choose another --account name (nothing was changed)", name, acct.Auth)
	case !strings.EqualFold(acct.Tenant, s.tenant):
		return fmt.Errorf("account %q is for tenant %s but you signed in to tenant %s; choose another --account name (nothing was changed)",
			name, acct.Tenant, s.tenant)
	case !strings.EqualFold(acct.ClientID, app.ClientID):
		return fmt.Errorf("account %q uses the app with client ID %s, not %s (%s); a new secret would replace its secret. "+
			"Choose another --account name (nothing was changed)", name, acct.ClientID, app.ClientID, app.DisplayName)
	}
	return nil
}

// soleAccountOf reports whether exactly one client_secret account on this
// machine uses the app, and it is the one being set up.
func (s *AppSetupSession) soleAccountOf(app appsetup.AppRef) bool {
	cfg, _, err := s.o.LoadConfigOrDefault()
	if err != nil {
		return false
	}
	var names []string
	for _, a := range cfg.Accounts {
		if a.Auth == config.AuthClientSecret && strings.EqualFold(a.ClientID, app.ClientID) {
			names = append(names, a.Name)
		}
	}
	return len(names) == 1 && names[0] == s.opts.Account
}

// storeSecret is the sink for the client secret: straight into the vault.
func (s *AppSetupSession) storeSecret(secret string) error {
	cfg, _, err := s.o.LoadConfigOrDefault()
	if err != nil {
		return err
	}
	v, err := s.o.OpenVault(cfg)
	if err != nil {
		return err
	}
	// An account that exists keeps its secret under its key. For one that is
	// not added yet the secret waits under the bare name; AddAccount moves it
	// to the ID the account gets.
	key := s.opts.Account
	if acct, ok := cfg.Find(s.opts.Account); ok {
		key = acct.Key()
	}
	if err := v.Put(vault.SecretKey(key), []byte(secret)); err != nil {
		return WithHint(fmt.Errorf("store secret: %w", err), s.opts.Account)
	}
	return nil
}

// Plan shows what Apply will do, changing nothing. For the application
// identity it also fails early when the account's vault entry must not be
// overwritten (see checkSecretTarget).
func (s *AppSetupSession) Plan(ctx context.Context) (*appsetup.Plan, error) {
	done, err := s.enter()
	if err != nil {
		return nil, err
	}
	defer done()
	if s.repair != nil {
		p, _, err := s.repairPlan(ctx)
		return p, err
	}
	p, err := s.setup.Plan(ctx)
	if err != nil || !s.opts.AppOnly {
		return p, err
	}
	if err := s.checkPlanTarget(p); err != nil {
		return nil, err
	}
	return p, nil
}

// PlanFor previews the actions of a choice the user has not confirmed yet
// (see appsetup.Setup.PlanFor); it changes nothing.
func (s *AppSetupSession) PlanFor(ctx context.Context, choice appsetup.Choice) (*appsetup.Plan, error) {
	done, err := s.enter()
	if err != nil {
		return nil, err
	}
	defer done()
	if s.repair != nil {
		p, _, err := s.repairPlan(ctx)
		return p, err
	}
	return s.setup.PlanFor(ctx, &choice)
}

// ErrRepairTarget means the app registration setup found is not the one the
// account signs in with, so a repair must not touch it.
var ErrRepairTarget = errors.New("not the app registration of this account")

// repairPlan plans the repair of the account's own app registration and returns
// the choice that adopts it when it is not tagged. It refuses when the tenant
// has another tagged app or the account's app cannot be found among the
// familiar ones, because repairing must never create or change a different app.
func (s *AppSetupSession) repairPlan(ctx context.Context) (*appsetup.Plan, appsetup.Choice, error) {
	want := s.repair.ClientID
	p, err := s.setup.Plan(ctx)
	if err != nil {
		return nil, appsetup.Choice{}, err
	}
	switch {
	case p.Existing != nil:
		if !strings.EqualFold(p.Existing.ClientID, want) {
			return nil, appsetup.Choice{}, fmt.Errorf("%w: setup found the app %q (client ID %s) in this tenant but account %q signs in with client ID %s; "+
				"add the permissions to that app in the Entra admin center instead (nothing was changed)",
				ErrRepairTarget, p.Existing.DisplayName, p.Existing.ClientID, s.repair.Name, want)
		}
		// Adopt even when the app is tagged: if it vanishes before Apply looks
		// again, setup then finds nothing to adopt and stops, never creating an app.
		return p, appsetup.Choice{Adopt: want}, nil
	default:
		for _, c := range p.Candidates {
			if strings.EqualFold(c.ClientID, want) {
				choice := appsetup.Choice{Adopt: c.ClientID}
				q, err := s.setup.PlanFor(ctx, &choice)
				if err != nil {
					return nil, appsetup.Choice{}, err
				}
				ref := c
				q.Existing, q.Candidates = &ref, nil
				return q, choice, nil
			}
		}
	}
	return nil, appsetup.Choice{}, fmt.Errorf("%w: its client ID %s was not found among the app registrations setup recognises in this tenant "+
		"(nothing was changed). Add the missing permissions to that app in the Entra admin center instead (%s)",
		ErrRepairTarget, want, appsetup.Manual)
}

func (s *AppSetupSession) checkPlanTarget(p *appsetup.Plan) error {
	if p.Existing != nil {
		return s.checkSecretTarget(*p.Existing)
	}
	cfg, _, err := s.o.LoadConfigOrDefault()
	if err != nil {
		return err
	}
	acct, exists := cfg.Find(s.opts.Account)
	if !exists {
		return nil
	}
	// A new app is coming (or one of the candidates is adopted): the account
	// only fits if it already uses one of the candidates.
	for _, c := range p.Candidates {
		if s.checkSecretTarget(c) == nil {
			return nil
		}
	}
	if acct.Auth != config.AuthClientSecret {
		return s.checkSecretTarget(appsetup.AppRef{})
	}
	return fmt.Errorf("account %q already exists with another app (client ID %s); a new app's secret would replace its secret. "+
		"Choose another --account name (nothing was changed)", s.opts.Account, acct.ClientID)
}

// RevokeSecret deletes the client secret a finished Apply created, for the
// caller that could not use it (e.g. adding the account failed). If Entra
// cannot be reached the error says which secret is still live there.
func (s *AppSetupSession) RevokeSecret(ctx context.Context, res *appsetup.Result) error {
	done, err := s.enter()
	if err != nil {
		return err
	}
	defer done()
	return s.revokeSecret(ctx, res)
}

func (s *AppSetupSession) revokeSecret(ctx context.Context, res *appsetup.Result) error {
	if res == nil || res.SecretKeyID == "" {
		return nil
	}
	return s.setup.RevokeSecret(ctx, res.ObjectID, res.SecretKeyID, res.SecretName)
}

// DiscardSecret undoes a finished Apply whose client secret will not be used
// (the user left before the account was saved): the secret is deleted from the
// app and from the vault. It does nothing when the account exists, because
// then the account owns the secret (false: nothing was done). The error names the secret that is still
// live in Entra if revoking failed.
func (s *AppSetupSession) DiscardSecret(ctx context.Context, res *appsetup.Result) (bool, error) {
	if res == nil || res.SecretKeyID == "" || !s.opts.AppOnly {
		return false, nil
	}
	done, err := s.enter()
	if err != nil {
		return false, err
	}
	defer done()
	// Whatever the caller's context says, this must run to its end. Holding
	// the config lock makes it atomic with AddAccount: either the account is
	// saved first (and keeps its secret) or the secret is gone first (and
	// AddAccount refuses for want of a stored secret).
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	unlock, err := s.o.LockConfig(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	cfg, _, err := s.o.LoadConfigOrDefault()
	if err != nil {
		return false, err
	}
	if _, exists := cfg.Find(s.opts.Account); exists {
		return false, nil
	}
	// Not added: the secret waits under the bare name (see storeSecret).
	revokeErr := s.revokeSecret(ctx, res)
	var vaultErr error
	if v, err := s.o.OpenVault(cfg); err != nil {
		vaultErr = err
	} else if err := v.Delete(vault.SecretKey(s.opts.Account)); err != nil && !errors.Is(err, vault.ErrNotFound) {
		vaultErr = fmt.Errorf("remove the stored secret: %w", err)
	}
	return true, errors.Join(revokeErr, vaultErr)
}

// Apply carries the plan out. choice is only used when the plan has
// candidates. For the application identity the client secret goes into the
// vault under the account's name; if that account already exists its
// secret_expires is updated.
func (s *AppSetupSession) Apply(ctx context.Context, choice appsetup.Choice) (*appsetup.Result, error) {
	done, err := s.enter()
	if err != nil {
		return nil, err
	}
	defer done()
	if s.repair != nil {
		// The choice is not the user's here: the account's own app, nothing else.
		_, forced, err := s.repairPlan(ctx)
		if err != nil {
			return nil, err
		}
		choice = forced
	}
	res, err := s.setup.Apply(ctx, choice)
	if err != nil {
		return nil, err
	}
	if s.repair != nil && !strings.EqualFold(res.ClientID, s.repair.ClientID) {
		return res, fmt.Errorf("%w: setup changed %s, not %s", ErrRepairTarget, res.ClientID, s.repair.ClientID)
	}
	if s.opts.AppOnly && !s.opts.Repair && res.SecretExpires != "" {
		if cfg, _, err := s.o.LoadConfig(); err == nil {
			if acct, ok := cfg.Find(s.opts.Account); ok && acct.Auth == config.AuthClientSecret {
				if err := s.o.updateAccountKey(ctx, acct.Key(), func(ac *config.Account) { ac.SecretExpires = res.SecretExpires }); err != nil {
					return res, fmt.Errorf("the secret was stored, but saving its expiry date failed: %w", err)
				}
			}
		}
	}
	return res, nil
}

// PrepareAddAccount checks that an account could be added (valid, not taken)
// without changing anything, so automatic setup fails before it creates anything.
func (o *Ops) PrepareAddAccount(in AccountInput) error {
	acct := in.Account()
	if acct.Tenant == "" || acct.Tenant == "organizations" || acct.Tenant == "common" {
		acct.Tenant = "tenant.example" // the real tenant ID is only known after sign-in
	}
	if acct.ClientID == "" {
		acct.ClientID = testClientID
	}
	if err := ValidateAccount(acct); err != nil {
		return err
	}
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return err
	}
	if _, exists := cfg.Find(acct.Name); exists {
		return fmt.Errorf("account %q already exists", acct.Name)
	}
	return nil
}

// LoginAfterSetup is Login for an account whose app registration was just
// created: while Microsoft is still replicating it (AADSTS700016) it waits and
// tries again for about two minutes, reporting progress.
func (o *Ops) LoginAfterSetup(ctx context.Context, name string, ui auth.LoginUI, progress func(string)) (LoginResult, error) {
	backoff := o.SetupBackoff
	if backoff == nil {
		backoff = appsetup.DefaultBackoff
	}
	for attempt := 0; ; attempt++ {
		res, err := o.Login(ctx, name, ui)
		if err == nil || !strings.Contains(err.Error(), "AADSTS700016") || attempt >= len(backoff) {
			return res, err
		}
		if progress != nil {
			progress(fmt.Sprintf("waiting for Microsoft to replicate the new app, retrying sign-in in %s...", backoff[attempt].Round(time.Second)))
		}
		select {
		case <-time.After(backoff[attempt]):
		case <-ctx.Done():
			return LoginResult{}, ctx.Err()
		}
	}
}
