package services

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/appsetup"
	"github.com/KKazuhaK/e5-developer-extend/internal/appsetup/fakegraph"
	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

const adminUPN = "admin.private@contoso.example"

type setupRig struct {
	*rig
	g    *fakegraph.Server
	svc  *AppSetupService
	fail error         // makes the administrator sign-in fail
	hold chan struct{} // when set, the administrator sign-in waits for it or for the context
	seq  atomic.Int32
}

func newSetupRig(t *testing.T) *setupRig {
	t.Helper()
	r := &setupRig{rig: newRig(t), g: fakegraph.New(t)}
	o := r.b.Ops
	o.GraphBase = r.g.Base()
	o.SetupBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	o.SetupHostname = "dev-box"
	o.Bootstrap = func(ctx context.Context, method, tenant, clientID string, scopes []string, ui auth.LoginUI, _ *http.Client) (auth.Token, error) {
		if r.fail != nil {
			return auth.Token{}, r.fail
		}
		if method == config.AuthBrowser {
			_ = ui.OpenBrowser("https://login.example.invalid/authorize?state=STATE-VALUE")
		} else {
			ui.DeviceCode("To sign in, use a web browser to open the page https://microsoft.com/devicelogin and enter the code ADMN1234EF to authenticate.")
		}
		if r.hold != nil {
			select {
			case <-r.hold:
			case <-ctx.Done():
				return auth.Token{}, ctx.Err()
			}
		}
		return auth.Token{AccessToken: fakegraph.Token, Account: adminUPN, TenantID: "tid-123"}, nil
	}
	r.svc = NewAppSetupService(r.b)
	return r
}

func (r *setupRig) id() string { return "setup-" + string(rune('a'+r.seq.Add(1))) + "-0123456789" }

// wait returns the events of the setup up to and including the first of one of the kinds.
func (r *setupRig) wait(id string, kinds ...string) []AppSetupEvent {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var out []AppSetupEvent
		r.ev.mu.Lock()
		for _, e := range r.ev.list {
			if ev, ok := e.Data.(AppSetupEvent); ok && e.Name == AppSetupEventName(id) {
				out = append(out, ev)
			}
		}
		r.ev.mu.Unlock()
		for _, ev := range out {
			for _, k := range kinds {
				if ev.Kind == k {
					return out
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("no %v event for %s", kinds, id)
	return nil
}

func last(evs []AppSetupEvent) AppSetupEvent { return evs[len(evs)-1] }

func (r *setupRig) noLeak() {
	r.t.Helper()
	all := r.ev.json()
	for _, banned := range []string{fakegraph.Token, adminUPN, "STATE-VALUE", "login.example.invalid"} {
		if strings.Contains(all, banned) {
			r.t.Errorf("%q leaked through an event", banned)
		}
	}
}

func TestAppSetupDelegatedFlowThenLoginAfterSetup(t *testing.T) {
	r := newSetupRig(t)
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "new1", Auth: config.AuthDeviceCode}); err != nil {
		t.Fatal(err)
	}
	evs := r.wait(id, "plan")
	var code AppSetupEvent
	for _, ev := range evs {
		if ev.Kind == "device_code" {
			code = ev
		}
	}
	if code.UserCode != "ADMN1234EF" || code.VerificationURI != "https://microsoft.com/devicelogin" {
		t.Fatalf("device code event: %+v", evs)
	}
	plan := last(evs).Plan
	if plan == nil || plan.NeedsChoice || plan.DisplayName != appsetup.NameDelegated || len(plan.Permissions) == 0 ||
		len(plan.Missing) != len(plan.Permissions) || len(plan.Actions) == 0 || plan.TenantID != "tid-123" {
		t.Fatalf("plan: %+v", plan)
	}
	if len(r.g.Writes()) != 0 {
		t.Fatalf("planning changed something: %v", r.g.Writes())
	}
	if err := r.svc.Apply(id, AppSetupChoice{}); err != nil {
		t.Fatal(err)
	}
	evs = r.wait(id, "result", "error")
	res := last(evs).Result
	if res == nil || !res.CreatedApp || !res.ConsentGranted || res.TenantID != "tid-123" || res.ClientID == "" || res.AdminConsentURL != "" {
		t.Fatalf("result: %+v / %+v", res, last(evs))
	}
	progress := 0
	for _, ev := range evs {
		if ev.Kind == "progress" {
			progress++
		}
	}
	if progress == 0 {
		t.Error("no progress events")
	}
	// The wizard saves the account and ends the session; nothing is left to cancel or apply.
	if err := r.svc.Apply(id, AppSetupChoice{}); err == nil {
		t.Error("Apply after the result must fail")
	}
	if err := r.svc.Finish(id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { r.b.mu.Lock(); defer r.b.mu.Unlock(); return len(r.b.setups) == 0 })
	if err := r.svc.Finish(id); err == nil {
		t.Error("Finish twice")
	}
	if c := r.svc.Cancel(id); c.SecretRemoved || c.Error != "" {
		t.Errorf("cancel after the end: %+v", c)
	}

	// The account is added and signed in; Microsoft is slow to know the new app.
	acc := NewAccountService(r.b)
	if err := acc.Add(AccountInput{Name: "new1", Auth: config.AuthDeviceCode, Tenant: res.TenantID, ClientID: res.ClientID, Categories: []string{"user"}}); err != nil {
		t.Fatal(err)
	}
	fa := &flakyAuth{fakeAuth: r.fa, failures: 2}
	r.b.Ops.NewAuth = func(config.Account, auth.Store, *http.Client) (auth.Authenticator, error) { return fa, nil }
	if err := acc.LoginAfterSetup("new1"); err != nil {
		t.Fatal(err)
	}
	var waiting int
	for _, ev := range r.waitLogin("new1", "done") {
		if ev.Kind == "progress" && strings.Contains(ev.Message, "replicate") {
			waiting++
		}
	}
	if waiting != 2 {
		t.Errorf("progress events while waiting: %d", waiting)
	}
	r.noLeak()
}

type flakyAuth struct {
	*fakeAuth
	failures int
}

func (f *flakyAuth) Login(ctx context.Context, ui auth.LoginUI) (auth.Token, error) {
	if f.failures > 0 {
		f.failures--
		return auth.Token{}, errors.New("AADSTS700016: Application with identifier was not found in the directory")
	}
	return f.fakeAuth.Login(ctx, ui)
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestAppSetupCandidatesNeedAChoice(t *testing.T) {
	r := newSetupRig(t)
	other := r.g.AddApp(map[string]any{"displayName": "e5renew"})
	otherClient, _ := r.g.App(other)["appId"].(string)
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "new1", Auth: config.AuthBrowser, Method: config.AuthBrowser}); err != nil {
		t.Fatal(err)
	}
	evs := r.wait(id, "plan")
	if len(r.opened) != 1 {
		t.Fatalf("browser opened %d times", len(r.opened))
	}
	plan := last(evs).Plan
	if !plan.NeedsChoice || len(plan.Candidates) != 1 || plan.Candidates[0].ClientID != otherClient || plan.Existing != nil {
		t.Fatalf("plan: %+v", plan)
	}
	// Nothing, both, or someone else's app: refused, and the session stays open.
	for _, c := range []AppSetupChoice{{}, {Adopt: otherClient, CreateNew: true}, {Adopt: "00000000-0000-0000-0000-00000000dead"}} {
		if err := r.svc.Apply(id, c); err == nil {
			t.Errorf("choice %+v accepted", c)
		}
	}
	if err := r.svc.Apply(id, AppSetupChoice{Adopt: otherClient, Rename: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Apply(id, AppSetupChoice{CreateNew: true}); err == nil {
		t.Error("a second Apply must be refused")
	}
	res := last(r.wait(id, "result", "error")).Result
	if res == nil || !res.AdoptedApp || !res.Renamed || res.ClientID != otherClient || res.DisplayName != appsetup.NameDelegated {
		t.Fatalf("result: %+v", res)
	}
	r.svc.Cancel(id)
	if len(r.g.Apps) != 1 {
		t.Errorf("an app was created as well: %d apps", len(r.g.Apps))
	}
	r.noLeak()
}

func TestAppSetupCreateNewDespiteCandidates(t *testing.T) {
	r := newSetupRig(t)
	r.g.AddApp(map[string]any{"displayName": "e5extend"})
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "new1", Auth: config.AuthDeviceCode}); err != nil {
		t.Fatal(err)
	}
	r.wait(id, "plan")
	if err := r.svc.Apply(id, AppSetupChoice{CreateNew: true, Rename: true}); err != nil {
		t.Fatal(err)
	}
	res := last(r.wait(id, "result", "error")).Result
	if res == nil || !res.CreatedApp || res.AdoptedApp || len(r.g.Apps) != 2 {
		t.Fatalf("result: %+v, %d apps", res, len(r.g.Apps))
	}
	r.svc.Cancel(id)
}

func TestAppSetupConsentNotGrantedKeepsTheApp(t *testing.T) {
	r := newSetupRig(t)
	r.g.Forbidden["POST /oauth2PermissionGrants"] = true
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "new1", Auth: config.AuthDeviceCode}); err != nil {
		t.Fatal(err)
	}
	r.wait(id, "plan")
	if err := r.svc.Apply(id, AppSetupChoice{}); err != nil {
		t.Fatal(err)
	}
	res := last(r.wait(id, "result", "error")).Result
	if res == nil || !res.CreatedApp || res.ConsentGranted || !strings.Contains(res.AdminConsentURL, "/adminconsent?client_id="+res.ClientID) || res.ConsentError == "" {
		t.Fatalf("result: %+v", res)
	}
	r.noLeak()
}

func TestAppSetupForbiddenNamesTheRoles(t *testing.T) {
	r := newSetupRig(t)
	r.g.Forbidden["POST /applications"] = true
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "new1", Auth: config.AuthDeviceCode}); err != nil {
		t.Fatal(err)
	}
	r.wait(id, "plan")
	if err := r.svc.Apply(id, AppSetupChoice{}); err != nil {
		t.Fatal(err)
	}
	ev := last(r.wait(id, "error", "result"))
	if ev.Kind != "error" || ev.Code != "forbidden" || strings.Join(ev.Roles, "|") != "Application Administrator|Cloud Application Administrator|Global Administrator" {
		t.Fatalf("event: %+v", ev)
	}
	// The session is over and a new one may start.
	waitFor(t, func() bool { r.b.mu.Lock(); defer r.b.mu.Unlock(); return len(r.b.setups) == 0 })
	r.noLeak()
}

func TestAppSetupBlockedTenant(t *testing.T) {
	r := newSetupRig(t)
	r.fail = errors.New("AADSTS65001: the user or administrator has not consented to use the application")
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "new1", Auth: config.AuthDeviceCode}); err != nil {
		t.Fatal(err)
	}
	ev := last(r.wait(id, "error"))
	if ev.Code != "blocked" || !strings.Contains(ev.Message, "docs/user/app-registration.md") {
		t.Fatalf("event: %+v", ev)
	}
}

func TestAppSetupCancelAtAnyPoint(t *testing.T) {
	r := newSetupRig(t)
	r.hold = make(chan struct{})
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "new1", Auth: config.AuthDeviceCode}); err != nil {
		t.Fatal(err)
	}
	r.wait(id, "device_code")
	if err := r.svc.Begin(r.id(), AppSetupStart{Name: "new2", Auth: config.AuthDeviceCode}); err == nil {
		t.Error("only one setup may run at a time")
	}
	if err := r.svc.Apply(id, AppSetupChoice{}); err == nil {
		t.Error("Apply before the plan must be refused")
	}
	r.svc.Cancel(id)
	if ev := last(r.wait(id, "error")); ev.Code != "cancelled" {
		t.Fatalf("event: %+v", ev)
	}
	waitFor(t, func() bool { r.b.mu.Lock(); defer r.b.mu.Unlock(); return len(r.b.setups) == 0 })

	// Cancel while the plan waits for a decision: nothing was changed.
	r.hold = nil
	id = r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "new1", Auth: config.AuthDeviceCode}); err != nil {
		t.Fatal(err)
	}
	r.wait(id, "plan")
	r.svc.Cancel(id)
	if ev := last(r.wait(id, "error")); ev.Code != "cancelled" {
		t.Fatalf("event: %+v", ev)
	}
	if len(r.g.Writes()) != 0 || len(r.g.Apps) != 0 {
		t.Errorf("cancelled setup changed something: %v", r.g.Writes())
	}
	if cfg, _, _ := r.b.Ops.LoadConfigOrDefault(); len(cfg.Accounts) != 0 {
		t.Error("an account was left behind")
	}
}

func TestAppSetupRefusesWhatCannotBeAdded(t *testing.T) {
	r := newSetupRig(t)
	acc := NewAccountService(r.b)
	if err := acc.Add(AccountInput{Name: "taken", Auth: config.AuthDeviceCode, Tenant: "t.example", ClientID: testGUID}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []AppSetupStart{
		{Name: "taken", Auth: config.AuthDeviceCode},
		{Name: "bad name", Auth: config.AuthDeviceCode},
		{Name: "new1", Auth: config.AuthPassword},
		{Name: "new1", Auth: config.AuthDeviceCode, Method: "password"},
		{Name: "new1", AppOnly: true}, // an application identity reads a user
	} {
		if err := r.svc.Begin(r.id(), s); err == nil {
			t.Errorf("%+v accepted", s)
		}
	}
	if err := r.svc.Begin("short", AppSetupStart{Name: "new1", Auth: config.AuthDeviceCode}); err == nil {
		t.Error("a bad ID was accepted")
	}
	if len(r.ev.list) != 0 {
		t.Errorf("events for refused setups: %s", r.ev.json())
	}
}

func TestAppSetupApplicationIdentityKeepsTheSecretInTheVault(t *testing.T) {
	r := newSetupRig(t)
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "app1", AppOnly: true, Username: "user@contoso.example", Method: config.AuthBrowser}); err != nil {
		t.Fatal(err)
	}
	plan := last(r.wait(id, "plan")).Plan
	if plan.Kind != "app" || plan.DisplayName != appsetup.NameApp {
		t.Fatalf("plan: %+v", plan)
	}
	if err := r.svc.Apply(id, AppSetupChoice{}); err != nil {
		t.Fatal(err)
	}
	res := last(r.wait(id, "result", "error")).Result
	if res == nil || res.SecretExpires == "" || !res.ConsentGranted {
		t.Fatalf("result: %+v", res)
	}
	if len(r.g.Secrets) != 1 {
		t.Fatalf("secrets handed out: %d", len(r.g.Secrets))
	}
	if strings.Contains(r.ev.json(), r.g.Secrets[0]) {
		t.Error("the client secret went through an event")
	}
	// The wizard then adds the account with the stored secret.
	acc := NewAccountService(r.b)
	if err := acc.Add(AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: res.TenantID, ClientID: res.ClientID,
		Username: "user@contoso.example", Categories: []string{"user"}, SecretExpires: res.SecretExpires, UseStoredSecret: true}); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ := r.b.Ops.LoadConfig()
	v, err := r.b.Ops.OpenVault(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.Get(vault.SecretKey(func() string { a, _ := cfg.Find("app1"); return a.Key() }())); err != nil || string(got) != r.g.Secrets[0] {
		t.Errorf("vault: %q %v", got, err)
	}
	r.noLeak()
}

func actionCodes(p *AppSetupPlan) string {
	var out []string
	for _, a := range p.Actions {
		out = append(out, a.Code)
	}
	return strings.Join(out, " ")
}

func TestAppSetupPreviewFollowsTheChoice(t *testing.T) {
	r := newSetupRig(t)
	cand := r.g.AddApp(map[string]any{"displayName": "e5renew"})
	candClient, _ := r.g.App(cand)["appId"].(string)
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "new1", Auth: config.AuthDeviceCode}); err != nil {
		t.Fatal(err)
	}
	r.wait(id, "plan")

	if p, err := r.svc.Preview(id, AppSetupChoice{}); err != nil || actionCodes(p) != "adopt_or_create" {
		t.Fatalf("no choice yet: %v %v", p, err)
	}
	p, err := r.svc.Preview(id, AppSetupChoice{Adopt: candClient, Rename: true})
	if err != nil || !strings.HasPrefix(actionCodes(p), "adopt_app rename_app set_notes add_tags") || !strings.HasSuffix(actionCodes(p), "create_service_principal grant_consent") {
		t.Fatalf("adopt: %v %v", p, err)
	}
	if len(p.Missing) != len(p.Permissions) {
		t.Errorf("missing: %v", p.Missing)
	}
	if p, _ = r.svc.Preview(id, AppSetupChoice{Adopt: candClient}); strings.Contains(actionCodes(p), "rename_app") {
		t.Errorf("keep the name: %s", actionCodes(p))
	}
	if p, _ = r.svc.Preview(id, AppSetupChoice{CreateNew: true}); actionCodes(p) != "create_app create_service_principal grant_consent" {
		t.Errorf("new: %s", actionCodes(p))
	}
	if _, err := r.svc.Preview(id, AppSetupChoice{Adopt: "00000000-0000-0000-0000-00000000dead"}); err == nil {
		t.Error("a stranger was previewed")
	}
	if len(r.g.Writes()) != 0 {
		t.Errorf("a preview wrote: %v", r.g.Writes())
	}
	if _, err := r.svc.Preview("unknown-id-123", AppSetupChoice{}); err == nil {
		t.Error("unknown session")
	}
	r.svc.Cancel(id)
}

func appOnlyDone(t *testing.T, r *setupRig) (string, *AppSetupResult) {
	t.Helper()
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "app1", AppOnly: true, Username: "user@contoso.example"}); err != nil {
		t.Fatal(err)
	}
	r.wait(id, "plan")
	if err := r.svc.Apply(id, AppSetupChoice{}); err != nil {
		t.Fatal(err)
	}
	res := last(r.wait(id, "result", "error")).Result
	if res == nil || !res.SecretCreated {
		t.Fatalf("result: %+v", res)
	}
	return id, res
}

func liveSecrets(r *setupRig) int {
	n := 0
	r.g.Do(func() {
		for _, ps := range r.g.Passwords {
			n += len(ps)
		}
	})
	return n
}

func TestAppSetupCancelAfterApplyRevokesTheUnusedSecret(t *testing.T) {
	r := newSetupRig(t)
	id, _ := appOnlyDone(t, r)
	if liveSecrets(r) != 1 {
		t.Fatalf("live secrets: %d", liveSecrets(r))
	}
	c := r.svc.Cancel(id)
	if !c.SecretRemoved || c.Error != "" {
		t.Fatalf("cancel: %+v", c)
	}
	if liveSecrets(r) != 0 {
		t.Error("the secret is still live in Entra")
	}
	cfg, _, _ := r.b.Ops.LoadConfigOrDefault()
	v, err := r.b.Ops.OpenVault(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(vault.SecretKey("app1")); err == nil {
		t.Error("the secret is still in the vault")
	}
	waitFor(t, func() bool { r.b.mu.Lock(); defer r.b.mu.Unlock(); return len(r.b.setups) == 0 })
	r.noLeak()
}

func TestAppSetupFinishKeepsTheSecret(t *testing.T) {
	r := newSetupRig(t)
	id, res := appOnlyDone(t, r)
	if err := NewAccountService(r.b).Add(AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: res.TenantID, ClientID: res.ClientID,
		Username: "user@contoso.example", Categories: []string{"user"}, SecretExpires: res.SecretExpires, UseStoredSecret: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Finish(id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { r.b.mu.Lock(); defer r.b.mu.Unlock(); return len(r.b.setups) == 0 })
	if c := r.svc.Cancel(id); c.SecretRemoved || liveSecrets(r) != 1 {
		t.Errorf("a used secret was touched: %+v, %d live", c, liveSecrets(r))
	}
}

func TestAppSetupCancelNeverRevokesAnAccountsSecret(t *testing.T) {
	r := newSetupRig(t)
	id, res := appOnlyDone(t, r)
	// The account exists (saved some other way) but Finish was not called.
	if err := NewAccountService(r.b).Add(AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: res.TenantID, ClientID: res.ClientID,
		Username: "user@contoso.example", Categories: []string{"user"}, UseStoredSecret: true}); err != nil {
		t.Fatal(err)
	}
	if c := r.svc.Cancel(id); c.SecretRemoved || c.Error != "" || liveSecrets(r) != 1 {
		t.Errorf("cancel: %+v, %d live", c, liveSecrets(r))
	}
}

func TestAppSetupCancelSaysWhichSecretStaysLive(t *testing.T) {
	r := newSetupRig(t)
	id, _ := appOnlyDone(t, r)
	r.g.Do(func() { r.g.Forbidden["POST /applications/"] = true })
	c := r.svc.Cancel(id)
	if c.SecretRemoved || !strings.Contains(c.Error, "key ID") || !strings.Contains(c.Error, "still live") {
		t.Fatalf("cancel: %+v", c)
	}
	if strings.Contains(c.Error, r.g.Secrets[0]) {
		t.Error("the secret is in the error")
	}
}

func TestAppSetupAddRacingCancelNeverLeavesAnAccountWithoutItsSecret(t *testing.T) {
	for i := 0; i < 8; i++ {
		r := newSetupRig(t)
		id, res := appOnlyDone(t, r)
		acc := NewAccountService(r.b)
		in := AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: res.TenantID, ClientID: res.ClientID,
			Username: "user@contoso.example", Categories: []string{"user"}, SecretExpires: res.SecretExpires, UseStoredSecret: true}
		var addErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); addErr = acc.Add(in) }()
		go func() { defer wg.Done(); r.svc.Cancel(id) }()
		wg.Wait()
		cfg, _, _ := r.b.Ops.LoadConfigOrDefault()
		_, saved := cfg.Find("app1")
		v, err := r.b.Ops.OpenVault(cfg)
		if err != nil {
			t.Fatal(err)
		}
		has, _ := v.Has(vault.SecretKey("app1"))
		switch {
		case saved && (!has || liveSecrets(r) != 1):
			t.Fatalf("round %d: account saved without its secret (vault %v, %d live, add err %v)", i, has, liveSecrets(r), addErr)
		case !saved && (has || liveSecrets(r) != 0):
			t.Fatalf("round %d: no account but a secret remains (vault %v, %d live)", i, has, liveSecrets(r))
		case !saved && addErr == nil:
			t.Fatalf("round %d: Add reported success for nothing", i)
		}
	}
}

func TestAppSetupCancelRightAfterApplyStillUndoesTheSecret(t *testing.T) {
	r := newSetupRig(t)
	// The context dies between Apply's return and the result (only the context, not Cancel).
	r.b.afterApply = func(sess *setupSession) { sess.cancel() }
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "app1", AppOnly: true, Username: "user@contoso.example"}); err != nil {
		t.Fatal(err)
	}
	r.wait(id, "plan")
	if err := r.svc.Apply(id, AppSetupChoice{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { r.b.mu.Lock(); defer r.b.mu.Unlock(); return len(r.b.setups) == 0 })
	if liveSecrets(r) != 0 {
		t.Error("the secret is still live in Entra")
	}
	cfg, _, _ := r.b.Ops.LoadConfigOrDefault()
	v, _ := r.b.Ops.OpenVault(cfg)
	if has, _ := v.Has(vault.SecretKey("app1")); has {
		t.Error("the secret is still in the vault")
	}
}

func TestAppSetupBackendCloseUndoesUnusedSecrets(t *testing.T) {
	r := newSetupRig(t)
	appOnlyDone(t, r)
	r.b.Close()
	if liveSecrets(r) != 0 {
		t.Error("the secret is still live after Close")
	}
	r.b.mu.Lock()
	n := len(r.b.setups)
	r.b.mu.Unlock()
	if n != 0 {
		t.Errorf("%d sessions left", n)
	}
}

func TestAppSetupIdleSessionsExpire(t *testing.T) {
	r := newSetupRig(t)
	clock := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	r.b.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }

	// A plan nobody answers.
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "new1", Auth: config.AuthDeviceCode}); err != nil {
		t.Fatal(err)
	}
	r.wait(id, "plan")
	advance(20 * time.Minute)
	r.b.expireSetups()
	if r.b.setups[id] == nil || r.b.setups[id].ctx.Err() != nil {
		t.Fatal("expired too early")
	}
	// Using it keeps it alive.
	if _, err := r.svc.Preview(id, AppSetupChoice{}); err != nil {
		t.Fatal(err)
	}
	advance(20 * time.Minute)
	r.b.expireSetups()
	if r.b.setups[id] == nil || r.b.setups[id].ctx.Err() != nil {
		t.Fatal("activity did not extend the session")
	}
	advance(11 * time.Minute)
	r.b.expireSetups()
	if ev := last(r.wait(id, "error")); ev.Code != "cancelled" {
		t.Fatalf("event: %+v", ev)
	}
	waitFor(t, func() bool { r.b.mu.Lock(); defer r.b.mu.Unlock(); return len(r.b.setups) == 0 })

	// A result nobody used: the secret goes.
	id2, _ := appOnlyDone(t, r)
	advance(31 * time.Minute)
	r.b.expireSetups()
	waitFor(t, func() bool { r.b.mu.Lock(); defer r.b.mu.Unlock(); return len(r.b.setups) == 0 })
	if liveSecrets(r) != 0 {
		t.Errorf("session %s: the secret is still live", id2)
	}
}

func TestAppSetupRefusesAnIdInUse(t *testing.T) {
	r := newSetupRig(t)
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "new1", Auth: config.AuthDeviceCode}); err != nil {
		t.Fatal(err)
	}
	r.wait(id, "plan")
	if err := r.svc.Begin(id, AppSetupStart{Name: "new2", Auth: config.AuthDeviceCode}); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("err = %v", err)
	}
	r.svc.Cancel(id)
}

func TestAppSetupCancelDuringPreviewIsRaceFree(t *testing.T) {
	r := newSetupRig(t)
	r.g.AddApp(map[string]any{"displayName": "e5renew"})
	id := r.id()
	if err := r.svc.Begin(id, AppSetupStart{Name: "new1", Auth: config.AuthDeviceCode}); err != nil {
		t.Fatal(err)
	}
	plan := last(r.wait(id, "plan")).Plan
	choice := AppSetupChoice{Adopt: plan.Candidates[0].ClientID, Rename: true}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_, _ = r.svc.Preview(id, choice)
			}
		}()
	}
	time.Sleep(3 * time.Millisecond)
	r.svc.Cancel(id)
	wg.Wait()
	waitFor(t, func() bool { r.b.mu.Lock(); defer r.b.mu.Unlock(); return len(r.b.setups) == 0 })
}

func TestRepairFlowAddsPermissionsAndEndsWithFinish(t *testing.T) {
	r := newSetupRig(t)
	ctx := context.Background()
	objID := r.g.AddApp(map[string]any{"displayName": "E5 Developer Extend (app-only)", "appId": testGUID, "tags": []string{"e5extend", "e5extend:app"}})
	if err := r.b.Ops.AddAccount(ctx, AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: "tid-123", ClientID: testGUID, Secret: "OLD-SECRET-VALUE"}); err != nil {
		t.Fatal(err)
	}
	id := r.id()
	if err := r.svc.Repair(id, "app1", ""); err != nil {
		t.Fatal(err)
	}
	evs := r.wait(id, "plan", "error")
	plan := last(evs).Plan
	if plan == nil || plan.NeedsChoice || plan.Existing == nil || plan.Existing.ClientID != testGUID || len(plan.Missing) == 0 {
		t.Fatalf("plan: %+v / %+v", plan, last(evs))
	}
	for _, a := range plan.Actions {
		if a.Code == "create_secret" {
			t.Errorf("the repair plans a secret: %+v", plan.Actions)
		}
	}
	if err := r.svc.Apply(id, AppSetupChoice{}); err != nil {
		t.Fatal(err)
	}
	res := last(r.wait(id, "result", "error")).Result
	if res == nil || res.CreatedApp || res.SecretCreated || !res.ConsentGranted {
		t.Fatalf("result: %+v", res)
	}
	if err := r.svc.Finish(id); err != nil {
		t.Fatal(err)
	}
	if n := r.g.PasswordNames(objID); len(n) != 0 {
		t.Errorf("a secret was created: %v", n)
	}
	cfg, _, _ := r.b.Ops.LoadConfig()
	if len(cfg.Accounts) != 1 {
		t.Errorf("accounts = %+v", cfg.Accounts)
	}
	r.noLeak()
	if strings.Contains(r.ev.json(), "OLD-SECRET-VALUE") {
		t.Error("the stored secret leaked through an event")
	}
}

func TestRepairFlowRefusesAnotherAppAndUnknownAccount(t *testing.T) {
	r := newSetupRig(t)
	ctx := context.Background()
	r.g.AddApp(map[string]any{"displayName": "E5 Developer Extend", "appId": "99999999-0000-4000-8000-000000000000", "tags": []string{"e5extend", "e5extend:delegated"}})
	if err := r.b.Ops.AddAccount(ctx, AccountInput{Name: "dev", Auth: config.AuthDeviceCode, Tenant: "tid-123", ClientID: testGUID}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Repair(r.id(), "nobody", ""); err == nil {
		t.Error("an unknown account must be refused")
	}
	if err := r.svc.Repair(r.id(), "dev", "magic"); err == nil {
		t.Error("an unknown sign-in method must be refused")
	}
	id := r.id()
	if err := r.svc.Repair(id, "dev", ""); err != nil {
		t.Fatal(err)
	}
	ev := last(r.wait(id, "plan", "error"))
	if ev.Kind != "error" || len(r.g.Writes()) != 0 {
		t.Errorf("event %+v writes %v", ev, r.g.Writes())
	}
}
