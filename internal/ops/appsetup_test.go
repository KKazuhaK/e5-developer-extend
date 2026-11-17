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

func TestAppSetupApplicationIdentityStoresTheSecretInTheVault(t *testing.T) {
	e := newSetupEnv(t)
	ctx := context.Background()
	if err := e.AddAccount(ctx, AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: "tid-123", ClientID: testGUID, Secret: "OLD"}); err != nil {
		t.Fatal(err)
	}
	s, err := e.BeginAppSetup(ctx, AppSetupOptions{AppOnly: true, Account: "app1", Method: config.AuthBrowser}, &recUI{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(e.scopes, " "), "AppRoleAssignment.ReadWrite.All") || e.method != config.AuthBrowser {
		t.Errorf("scopes %v method %s", e.scopes, e.method)
	}
	res, err := s.Apply(ctx, appsetup.Choice{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, _ := e.LoadConfig()
	v, _ := e.OpenVault(cfg)
	got, err := v.Get(vault.SecretKey("app1"))
	if err != nil || string(got) != e.g.Secrets[0] {
		t.Fatalf("vault secret: %q %v", got, err)
	}
	acct, _ := cfg.Find("app1")
	if acct.SecretExpires != res.SecretExpires || res.SecretExpires == "" {
		t.Errorf("secret_expires = %q, result %q", acct.SecretExpires, res.SecretExpires)
	}
	if strings.Contains(string(mustRead(t, filepath.Join(e.cfgDir, "config.toml"))), e.g.Secrets[0]) {
		t.Error("secret in the config file")
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
	for _, code := range []string{"AADSTS65001", "AADSTS53003", "AADSTS90094"} {
		e.fail = errors.New(code + ": the user or administrator has not consented to use the application")
		_, err := e.BeginAppSetup(context.Background(), AppSetupOptions{}, &recUI{}, nil)
		if !errors.Is(err, ErrBootstrapBlocked) || !strings.Contains(err.Error(), "docs/user/app-registration.md") {
			t.Errorf("%s: err = %v", code, err)
		}
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
