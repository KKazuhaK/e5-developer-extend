package ops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/appsetup"
	"github.com/KKazuhaK/e5-developer-extend/internal/appsetup/fakegraph"
	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

type setupEnv struct {
	*testEnv
	g      *fakegraph.Server
	scopes []string
	method string
	ui     auth.LoginUI
	fail   error
	testT  *testing.T
}

func newSetupEnv(t *testing.T) *setupEnv {
	t.Helper()
	e := &setupEnv{testEnv: newTestEnv(t), g: fakegraph.New(t)}
	e.GraphBase = e.g.Base()
	e.SetupBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	e.SetupHostname = "dev-box"
	e.Bootstrap = func(_ context.Context, method, tenant, clientID string, scopes []string, ui auth.LoginUI, _ *http.Client) (auth.Token, error) {
		if e.fail != nil {
			return auth.Token{}, e.fail
		}
		if clientID != auth.MicrosoftGraphCLIClientID || tenant != "organizations" {
			t.Errorf("bootstrap client %s tenant %s", clientID, tenant)
		}
		e.scopes, e.method, e.ui = scopes, method, ui
		ui.DeviceCode("go to the page and enter ABCD")
		return auth.Token{AccessToken: fakegraph.Token, Account: "admin@contoso.test", TenantID: "tid-123"}, nil
	}
	return e
}

type recUI struct{ msgs []string }

func (u *recUI) DeviceCode(m string)      { u.msgs = append(u.msgs, m) }
func (u *recUI) OpenBrowser(string) error { return nil }

func TestAppSetupDelegatedEndToEnd(t *testing.T) {
	e := newSetupEnv(t)
	ctx := context.Background()
	ui := &recUI{}
	var progress []string
	s, err := e.BeginAppSetup(ctx, AppSetupOptions{}, ui, func(m string) { progress = append(progress, m) })
	if err != nil {
		t.Fatal(err)
	}
	if s.Admin() != "admin@contoso.test" || s.TenantID() != "tid-123" || len(ui.msgs) != 1 {
		t.Fatalf("session: %s %s %v", s.Admin(), s.TenantID(), ui.msgs)
	}
	if strings.Join(e.scopes, " ") != "Application.ReadWrite.All DelegatedPermissionGrant.ReadWrite.All" || e.method != config.AuthDeviceCode {
		t.Errorf("scopes %v method %s", e.scopes, e.method)
	}
	plan, err := s.Plan(ctx)
	if err != nil || len(plan.Permissions) != len(catalog.RecommendedDelegated) {
		t.Fatalf("plan %+v %v", plan, err)
	}
	res, err := s.Apply(ctx, appsetup.Choice{})
	if err != nil || !res.CreatedApp || !res.ConsentGranted || res.TenantID != "tid-123" {
		t.Fatalf("result %+v %v", res, err)
	}
	s.Close()

	// The administrator's token is nowhere on disk.
	for _, dir := range []string{e.cfgDir, e.dataDir} {
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, _ error) error {
			if info != nil && !info.IsDir() {
				if b, _ := os.ReadFile(p); strings.Contains(string(b), fakegraph.Token) {
					t.Errorf("token written to %s", p)
				}
			}
			return nil
		})
	}
}

// vaultSecret reads secret/<account> from the vault ("" when absent).
func vaultSecret(t *testing.T, e *setupEnv, account string) string {
	t.Helper()
	cfg, _, _ := e.LoadConfigOrDefault()
	v, err := e.OpenVault(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Get(vault.SecretKey(account))
	if err != nil {
		return ""
	}
	return string(got)
}

func TestAppSetupApplicationIdentityStoresTheSecretInTheVault(t *testing.T) {
	e := newSetupEnv(t)
	ctx := context.Background()
	// The tenant already has the app, and the account uses exactly that app.
	e.g.AddApp(map[string]any{"displayName": "E5 Developer Extend (app-only)", "appId": testGUID, "tags": []string{"e5extend", "e5extend:app"}})
	if err := e.AddAccount(ctx, AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: "tid-123", ClientID: testGUID, Secret: "OLD"}); err != nil {
		t.Fatal(err)
	}
	s, err := e.BeginAppSetup(ctx, AppSetupOptions{AppOnly: true, Account: "app1", Method: config.AuthBrowser}, &recUI{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(e.scopes, " ") != "Application.ReadWrite.All AppRoleAssignment.ReadWrite.All" || e.method != config.AuthBrowser {
		t.Errorf("scopes %v method %s", e.scopes, e.method)
	}
	if _, err := s.Plan(ctx); err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := s.Apply(ctx, appsetup.Choice{})
	if err != nil {
		t.Fatal(err)
	}
	if got := vaultSecret(t, e, "app1"); got != e.g.Secrets[0] {
		t.Fatalf("vault secret: %q", got)
	}
	cfg, _, _ := e.LoadConfig()
	acct, _ := cfg.Find("app1")
	if acct.SecretExpires != res.SecretExpires || res.SecretExpires == "" {
		t.Errorf("secret_expires = %q, result %q", acct.SecretExpires, res.SecretExpires)
	}
	if strings.Contains(string(mustRead(t, filepath.Join(e.cfgDir, "config.toml"))), e.g.Secrets[0]) {
		t.Error("secret in the config file")
	}
}

func TestAppSetupDelegatedAsksOnlyForTheDelegatedGrantScope(t *testing.T) {
	if got := strings.Join(AppSetupScopes(false), " "); got != "Application.ReadWrite.All DelegatedPermissionGrant.ReadWrite.All" {
		t.Errorf("delegated: %s", got)
	}
	if got := strings.Join(AppSetupScopes(true), " "); got != "Application.ReadWrite.All AppRoleAssignment.ReadWrite.All" {
		t.Errorf("app-only: %s", got)
	}
}

func TestAppSetupApplicationIdentityForANewAccountName(t *testing.T) {
	e := newSetupEnv(t)
	ctx := context.Background()
	s, err := e.BeginAppSetup(ctx, AppSetupOptions{AppOnly: true, Account: "fresh"}, &recUI{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Plan(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, appsetup.Choice{}); err != nil {
		t.Fatal(err)
	}
	if got := vaultSecret(t, e, "fresh"); got != e.g.Secrets[0] {
		t.Errorf("vault secret: %q", got)
	}
}

// refused runs app-only setup for an account that must not be overwritten and
// checks that neither Plan nor Apply created a secret or touched the vault.
func refused(t *testing.T, e *setupEnv, account, vaultBefore string, want ...string) {
	t.Helper()
	ctx := context.Background()
	s, err := e.BeginAppSetup(ctx, AppSetupOptions{AppOnly: true, Account: account}, &recUI{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, perr := s.Plan(ctx)
	_, aerr := s.Apply(ctx, appsetup.Choice{})
	for _, err := range []error{perr, aerr} {
		if err == nil {
			t.Fatalf("expected a refusal (plan %v, apply %v)", perr, aerr)
		}
		for _, w := range want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("error lacks %q: %v", w, err)
			}
		}
	}
	if len(e.g.Secrets) != 0 {
		t.Error("a client secret was created before the refusal")
	}
	if got := vaultSecret(t, e, account); got != vaultBefore {
		t.Errorf("vault entry changed: %q", got)
	}
}

func TestAppSetupRefusesToOverwriteAPasswordAccountsSecret(t *testing.T) {
	e := newSetupEnv(t)
	if err := e.AddAccount(context.Background(), AccountInput{Name: "pw", Auth: config.AuthPassword, Tenant: "tid-123",
		ClientID: testGUID, Username: "u@contoso.test", Secret: "HUMAN-PASSWORD"}); err != nil {
		t.Fatal(err)
	}
	refused(t, e, "pw", "HUMAN-PASSWORD", `"pw"`, config.AuthPassword)
}

func TestAppSetupRefusesAnAccountOfAnotherApp(t *testing.T) {
	e := newSetupEnv(t)
	const other = "99999999-8888-7777-6666-555555555555"
	e.g.AddApp(map[string]any{"displayName": "E5 Developer Extend (app-only)", "tags": []string{"e5extend", "e5extend:app"}})
	if err := e.AddAccount(context.Background(), AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: "tid-123", ClientID: other, Secret: "OTHER-APPS-SECRET"}); err != nil {
		t.Fatal(err)
	}
	refused(t, e, "app1", "OTHER-APPS-SECRET", other, "app1")
}

func TestAppSetupRefusesAnAccountOfAnotherTenant(t *testing.T) {
	e := newSetupEnv(t)
	e.g.AddApp(map[string]any{"displayName": "x", "appId": testGUID, "tags": []string{"e5extend:app"}})
	if err := e.AddAccount(context.Background(), AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: "other-tenant", ClientID: testGUID, Secret: "S"}); err != nil {
		t.Fatal(err)
	}
	refused(t, e, "app1", "S", "other-tenant", "tid-123")
}

func TestAppSetupExistingAccountWhenAHandMadeAppWouldBeAdopted(t *testing.T) {
	e := newSetupEnv(t)
	ctx := context.Background()
	e.g.AddApp(map[string]any{"displayName": "e5renew", "appId": testGUID})
	if err := e.AddAccount(ctx, AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: "tid-123", ClientID: testGUID, Secret: "OLD"}); err != nil {
		t.Fatal(err)
	}
	s, err := e.BeginAppSetup(ctx, AppSetupOptions{AppOnly: true, Account: "app1"}, &recUI{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The account already uses the candidate, so adopting it is fine.
	if _, err := s.Plan(ctx); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := s.Apply(ctx, appsetup.Choice{Adopt: testGUID}); err != nil {
		t.Fatal(err)
	}
	if got := vaultSecret(t, e, "app1"); got != e.g.Secrets[0] {
		t.Errorf("vault secret: %q", got)
	}
}

func TestAppSetupRevokeSecret(t *testing.T) {
	e := newSetupEnv(t)
	ctx := context.Background()
	s, err := e.BeginAppSetup(ctx, AppSetupOptions{AppOnly: true, Account: "fresh"}, &recUI{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Apply(ctx, appsetup.Choice{})
	if err != nil {
		t.Fatal(err)
	}
	if res.SecretKeyID == "" || len(e.g.PasswordNames(res.ObjectID)) != 1 {
		t.Fatalf("result %+v", res)
	}
	if err := s.RevokeSecret(ctx, res); err != nil {
		t.Fatal(err)
	}
	if len(e.g.PasswordNames(res.ObjectID)) != 0 {
		t.Error("the secret is still in Entra")
	}
	e.g.Forbidden["POST /applications/"+res.ObjectID+"/removePassword"] = true
	res.SecretKeyID = "k-live"
	if err := s.RevokeSecret(ctx, res); err == nil || !strings.Contains(err.Error(), "still live in Entra") || !strings.Contains(err.Error(), "k-live") {
		t.Errorf("err = %v", err)
	}
}

func TestAppSetupApplicationIdentityNeedsAnAccountName(t *testing.T) {
	e := newSetupEnv(t)
	if _, err := e.BeginAppSetup(context.Background(), AppSetupOptions{AppOnly: true, Account: "Bad Name"}, &recUI{}, nil); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := e.BeginAppSetup(context.Background(), AppSetupOptions{Method: "password"}, &recUI{}, nil); err == nil {
		t.Fatal("password is not a bootstrap method")
	}
}

func TestAppSetupExplainsABlockedTenant(t *testing.T) {
	e := newSetupEnv(t)
	for _, code := range []string{"AADSTS65001", "AADSTS53003", "AADSTS90094", "AADSTS7000112", "AADSTS700016"} {
		e.fail = errors.New(code + ": the user or administrator has not consented to use the application")
		_, err := e.BeginAppSetup(context.Background(), AppSetupOptions{}, &recUI{}, nil)
		if !errors.Is(err, ErrBootstrapBlocked) || !strings.Contains(err.Error(), "docs/user/app-registration.md") {
			t.Errorf("%s: err = %v", code, err)
		}
	}
	e.fail = errors.New("AADSTS900144: The request body must contain the following parameter")
	if _, err := e.BeginAppSetup(context.Background(), AppSetupOptions{}, &recUI{}, nil); errors.Is(err, ErrBootstrapBlocked) {
		t.Errorf("AADSTS900144 is a request bug, not a blocked tenant: %v", err)
	}
	e.fail = context.Canceled
	if _, err := e.BeginAppSetup(context.Background(), AppSetupOptions{}, &recUI{}, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}
}

func TestAddAccountWithStoredSecret(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	in := AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: "t.example", ClientID: testGUID, UseStoredSecret: true}
	if err := e.AddAccount(ctx, in); err == nil {
		t.Fatal("nothing is stored yet")
	}
	cfg, _, _ := e.LoadConfigOrDefault()
	v, _ := e.OpenVault(cfg)
	if err := v.Put(vault.SecretKey("app1"), []byte("stored")); err != nil {
		t.Fatal(err)
	}
	if err := e.AddAccount(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got, _ := v.Get(vault.SecretKey("app1")); string(got) != "stored" {
		t.Errorf("secret = %q", got)
	}
	if err := e.PrepareAddAccount(in); err == nil {
		t.Error("PrepareAddAccount accepted a taken name")
	}
	if err := e.PrepareAddAccount(AccountInput{Name: "new", Auth: config.AuthDeviceCode}); err != nil {
		t.Error(err)
	}
}

type flakyAuth struct {
	fakeAuth
	failures int
}

func (f *flakyAuth) Login(ctx context.Context, ui auth.LoginUI) (auth.Token, error) {
	if f.failures > 0 {
		f.failures--
		return auth.Token{}, errors.New("AADSTS700016: Application with identifier was not found in the directory")
	}
	return f.fakeAuth.Login(ctx, ui)
}

func TestLoginAfterSetupWaitsForReplication(t *testing.T) {
	e := newSetupEnv(t)
	e.addDevice(t, "dev")
	fa := &flakyAuth{fakeAuth: *e.fa, failures: 2}
	e.NewAuth = func(config.Account, auth.Store, *http.Client) (auth.Authenticator, error) { return fa, nil }
	var progress []string
	res, err := e.LoginAfterSetup(context.Background(), "dev", &recUI{}, func(m string) { progress = append(progress, m) })
	if err != nil || !res.Delegated {
		t.Fatalf("login: %+v %v", res, err)
	}
	if len(progress) != 2 || !strings.Contains(progress[0], "replicate") {
		t.Errorf("progress = %q", progress)
	}
	// Gives up after the backoff list is used up.
	fa.failures = 10
	if _, err := e.LoginAfterSetup(context.Background(), "dev", &recUI{}, nil); err == nil || !strings.Contains(err.Error(), "AADSTS700016") {
		t.Errorf("err = %v", err)
	}
	// Other errors are not retried.
	fa.failures = 0
	fa.fakeAuth.err = errors.New("AADSTS50126: wrong password")
	progress = nil
	if _, err := e.LoginAfterSetup(context.Background(), "dev", &recUI{}, func(m string) { progress = append(progress, m) }); err == nil || len(progress) != 0 {
		t.Errorf("err = %v, progress %q", err, progress)
	}
}

func TestAppSetupLegacySecretsAreOnlyRemovedForTheSoleAccountOfTheApp(t *testing.T) {
	e := newSetupEnv(t)
	ctx := context.Background()
	id := e.g.AddApp(map[string]any{"displayName": "x", "appId": testGUID, "tags": []string{"e5extend", "e5extend:app"}})
	legacy := map[string]any{"keyId": "k-legacy", "displayName": "e5extend dev-box 2026-01-01", "endDateTime": "2027-01-01T00:00:00Z"}
	run := func() *appsetup.Result {
		e.g.Do(func() { e.g.Passwords[id] = []map[string]any{legacy} })
		s, err := e.BeginAppSetup(ctx, AppSetupOptions{AppOnly: true, Account: "app1"}, &recUI{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.Apply(ctx, appsetup.Choice{})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	add := func(name string) {
		if err := e.AddAccount(ctx, AccountInput{Name: name, Auth: config.AuthClientSecret, Tenant: "tid-123", ClientID: testGUID, Secret: "S"}); err != nil {
			t.Fatal(err)
		}
	}
	add("app1")
	if res := run(); fmt.Sprint(res.RemovedSecrets) != "[k-legacy]" {
		t.Errorf("sole account: removed %v, not removed %v", res.RemovedSecrets, res.NotRemovedSecrets)
	}
	add("app2")
	if res := run(); len(res.RemovedSecrets) != 0 || fmt.Sprint(res.NotRemovedSecrets) != "[k-legacy]" {
		t.Errorf("two accounts: removed %v, not removed %v", res.RemovedSecrets, res.NotRemovedSecrets)
	}
}

// repairEnv signs the administrator in to the tenant of the account being repaired.
func (e *setupEnv) repairBootstrap(tenant string) {
	e.Bootstrap = func(_ context.Context, _, got, _ string, _ []string, _ auth.LoginUI, _ *http.Client) (auth.Token, error) {
		if got != tenant {
			e.t().Errorf("administrator signed in to %q, want the account's tenant %q", got, tenant)
		}
		return auth.Token{AccessToken: fakegraph.Token, Account: "admin@contoso.test", TenantID: tenant}, nil
	}
}

func (e *setupEnv) t() *testing.T { return e.testT }

func TestRepairAppOnlyAddsPermissionsWithoutANewSecret(t *testing.T) {
	e := newSetupEnv(t)
	e.testT = t
	e.repairBootstrap("tid-123")
	ctx := context.Background()
	objID := e.g.AddApp(map[string]any{"displayName": "E5 Developer Extend (app-only)", "appId": testGUID, "tags": []string{"e5extend", "e5extend:app"}})
	if err := e.AddAccount(ctx, AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: "tid-123", ClientID: testGUID, Secret: "OLD"}); err != nil {
		t.Fatal(err)
	}
	s, err := e.BeginAppSetup(ctx, AppSetupOptions{Repair: true, Account: "app1"}, &recUI{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	plan, err := s.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range plan.Actions {
		if a.Code == "create_secret" {
			t.Errorf("a repair plans a secret: %+v", plan.Actions)
		}
	}
	if len(plan.Missing) == 0 {
		t.Errorf("nothing missing in %+v", plan)
	}
	res, err := s.Apply(ctx, appsetup.Choice{CreateNew: true}) // the caller's choice does not matter
	if err != nil {
		t.Fatal(err)
	}
	if res.ClientID != testGUID || res.CreatedApp || res.SecretKeyID != "" || len(res.AddedPermissions) == 0 || !res.ConsentGranted {
		t.Errorf("result %+v", res)
	}
	if got := vaultSecret(t, e, "app1"); got != "OLD" {
		t.Errorf("the stored secret changed to %q", got)
	}
	if n := e.g.PasswordNames(objID); len(n) != 0 {
		t.Errorf("a client secret was created: %v", n)
	}
	for _, w := range e.g.Writes() {
		if strings.Contains(w, "addPassword") || strings.Contains(w, "removePassword") || w == "POST /v1.0/applications" {
			t.Errorf("unexpected write %s", w)
		}
	}
	cfg, _, _ := e.LoadConfig()
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].SecretExpires != "" {
		t.Errorf("the account was changed: %+v", cfg.Accounts)
	}
}

func TestRepairRefusesAnotherAppRegistration(t *testing.T) {
	e := newSetupEnv(t)
	e.testT = t
	e.repairBootstrap("tid-123")
	ctx := context.Background()
	e.g.AddApp(map[string]any{"displayName": "E5 Developer Extend (app-only)", "appId": "99999999-0000-4000-8000-000000000000", "tags": []string{"e5extend", "e5extend:app"}})
	if err := e.AddAccount(ctx, AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: "tid-123", ClientID: testGUID, Secret: "OLD"}); err != nil {
		t.Fatal(err)
	}
	s, err := e.BeginAppSetup(ctx, AppSetupOptions{Repair: true, Account: "app1"}, &recUI{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Plan(ctx); !errors.Is(err, ErrRepairTarget) {
		t.Fatalf("plan: %v", err)
	}
	if _, err := s.Apply(ctx, appsetup.Choice{}); !errors.Is(err, ErrRepairTarget) {
		t.Fatalf("apply: %v", err)
	}
	if w := e.g.Writes(); len(w) != 0 {
		t.Errorf("a refused repair wrote: %v", w)
	}
	// An app that is not there at all is refused too, and nothing is created.
	e2 := newSetupEnv(t)
	e2.testT = t
	e2.repairBootstrap("tid-123")
	if err := e2.AddAccount(ctx, AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: "tid-123", ClientID: testGUID, Secret: "OLD"}); err != nil {
		t.Fatal(err)
	}
	s2, err := e2.BeginAppSetup(ctx, AppSetupOptions{Repair: true, Account: "app1"}, &recUI{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := s2.Apply(ctx, appsetup.Choice{}); !errors.Is(err, ErrRepairTarget) || len(e2.g.Writes()) != 0 {
		t.Errorf("missing app: %v %v", err, e2.g.Writes())
	}
	if _, err := e2.BeginAppSetup(ctx, AppSetupOptions{Repair: true, Account: "nobody"}, &recUI{}, nil); err == nil {
		t.Error("an unknown account must be refused")
	}
}

func TestRepairDelegatedAdoptsTheAccountsOwnApp(t *testing.T) {
	e := newSetupEnv(t)
	e.testT = t
	e.repairBootstrap("tid-123")
	ctx := context.Background()
	objID := e.g.AddApp(map[string]any{"displayName": "e5extend", "appId": testGUID})
	e.g.AddApp(map[string]any{"displayName": "E5 Renew", "appId": "88888888-0000-4000-8000-000000000000"})
	if err := e.AddAccount(ctx, AccountInput{Name: "dev", Auth: config.AuthDeviceCode, Tenant: "tid-123", ClientID: testGUID}); err != nil {
		t.Fatal(err)
	}
	s, err := e.BeginAppSetup(ctx, AppSetupOptions{Repair: true, Account: "dev"}, &recUI{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	plan, err := s.Plan(ctx)
	if err != nil || plan.NeedsChoice() || plan.Existing == nil || plan.Existing.ClientID != testGUID {
		t.Fatalf("plan %+v %v", plan, err)
	}
	res, err := s.Apply(ctx, appsetup.Choice{})
	if err != nil || res.ClientID != testGUID || res.CreatedApp || res.Renamed || len(res.AddedPermissions) == 0 {
		t.Fatalf("result %+v %v", res, err)
	}
	if got := e.g.App(objID)["displayName"]; got != "e5extend" {
		t.Errorf("the app was renamed to %v", got)
	}
	if e.g.Count("POST /v1.0/applications") != 0 {
		t.Error("a repair created an app")
	}
}

func TestRepairNeverCreatesAnAppWhenTheTaggedOneVanishes(t *testing.T) {
	e := newSetupEnv(t)
	e.testT = t
	e.repairBootstrap("tid-123")
	ctx := context.Background()
	e.g.AddApp(map[string]any{"displayName": "E5 Developer Extend (app-only)", "appId": testGUID, "tags": []string{"e5extend", "e5extend:app"}})
	if err := e.AddAccount(ctx, AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: "tid-123", ClientID: testGUID, Secret: "OLD"}); err != nil {
		t.Fatal(err)
	}
	s, err := e.BeginAppSetup(ctx, AppSetupOptions{Repair: true, Account: "app1"}, &recUI{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, choice, err := s.repairPlan(ctx)
	if err != nil || choice.Adopt != testGUID || choice.CreateNew {
		t.Fatalf("choice %+v %v", choice, err)
	}
	e.g.Do(func() { e.g.Apps = nil }) // deleted between the plan and the apply
	if _, err := s.setup.Apply(ctx, choice); err == nil {
		t.Fatal("setup went on without the app")
	}
	if e.g.Count("POST /v1.0/applications") != 0 || len(e.g.Apps) != 0 {
		t.Errorf("an app was created: %v", e.g.Writes())
	}
}
