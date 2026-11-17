package ops

import (
	"context"
	"errors"
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
