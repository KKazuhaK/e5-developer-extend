package ops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
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
}

// Admin is the sign-in name of the administrator, for the user to confirm.
func (s *AppSetupSession) Admin() string { return s.admin }

// TenantID is the tenant the administrator signed in to.
func (s *AppSetupSession) TenantID() string { return s.tenant }

// Close drops the administrator's token.
func (s *AppSetupSession) Close() {
	if s.setup != nil {
		s.setup.Token = ""
	}
}

// Scopes returns the Graph permissions the bootstrap sign-in asks for.
func AppSetupScopes(appOnly bool) []string {
	scopes := []string{"Application.ReadWrite.All", "DelegatedPermissionGrant.ReadWrite.All"}
	if appOnly {
		scopes = append(scopes, "AppRoleAssignment.ReadWrite.All")
	}
	return scopes
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
	if opts.Tenant == "" {
		opts.Tenant = "organizations"
	}
	if opts.AppOnly {
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
	s := &AppSetupSession{o: o, opts: opts, admin: tok.Account, tenant: tok.TenantID}
	s.setup = &appsetup.Setup{
		Kind: kind, Token: tok.AccessToken, TenantID: tok.TenantID, Base: o.GraphBase, HTTP: httpClient,
		Version: version.Version, Hostname: host, Progress: progress, Backoff: o.SetupBackoff,
	}
	if opts.AppOnly {
		s.setup.StoreSecret = s.storeSecret
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
		"AADSTS500200", "AADSTS50105", "AADSTS900144":
		return fmt.Errorf("%w (%s)\nUse the manual setup instead: %s", ErrBootstrapBlocked, code, appsetup.Manual)
	}
	return fmt.Errorf("administrator sign-in failed: %w", err)
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
	if err := v.Put(vault.SecretKey(s.opts.Account), []byte(secret)); err != nil {
		return WithHint(fmt.Errorf("store secret: %w", err), s.opts.Account)
	}
	return nil
}

// Plan shows what Apply will do, changing nothing.
func (s *AppSetupSession) Plan(ctx context.Context) (*appsetup.Plan, error) {
	return s.setup.Plan(ctx)
}

// Apply carries the plan out. choice is only used when the plan has
// candidates. For the application identity the client secret goes into the
// vault under the account's name; if that account already exists its
// secret_expires is updated.
func (s *AppSetupSession) Apply(ctx context.Context, choice appsetup.Choice) (*appsetup.Result, error) {
	res, err := s.setup.Apply(ctx, choice)
	if err != nil {
		return nil, err
	}
	if s.opts.AppOnly && res.SecretExpires != "" {
		if cfg, _, err := s.o.LoadConfig(); err == nil {
			if acct, ok := cfg.Find(s.opts.Account); ok && acct.Auth == config.AuthClientSecret {
				if err := s.o.updateAccount(ctx, s.opts.Account, func(ac *config.Account) { ac.SecretExpires = res.SecretExpires }); err != nil {
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
