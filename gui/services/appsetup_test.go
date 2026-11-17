package services

import (
	"context"
	"errors"
	"net/http"
	"strings"
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
	// The session is over; nothing is left to cancel or apply.
	waitFor(t, func() bool { r.b.mu.Lock(); defer r.b.mu.Unlock(); return len(r.b.setups) == 0 })
	if err := r.svc.Apply(id, AppSetupChoice{}); err == nil {
		t.Error("Apply after the end must fail")
	}
	r.svc.Cancel(id)

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
	if got, err := v.Get(vault.SecretKey("app1")); err != nil || string(got) != r.g.Secrets[0] {
		t.Errorf("vault: %q %v", got, err)
	}
	r.noLeak()
}
