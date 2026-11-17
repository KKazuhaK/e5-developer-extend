package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/appsetup/fakegraph"
	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// setupEnv returns an env wired to a fake Graph and a fake administrator sign-in.
func setupEnv(t *testing.T) (*env, *fakegraph.Server, *[]string) {
	t.Helper()
	e := newEnv(t)
	g := fakegraph.New(t)
	e.graphBase = g.Base()
	var scopes []string
	e.bootstrap = func(_ context.Context, method, tenant, clientID string, sc []string, ui auth.LoginUI, _ *http.Client) (auth.Token, error) {
		scopes = sc
		ui.DeviceCode("sign in at https://example.invalid/device with ADMINCODE")
		return auth.Token{AccessToken: fakegraph.Token, Account: "admin@contoso.test", TenantID: "tid-123"}, nil
	}
	return e, g, &scopes
}

func TestAppSetupYesCreatesThenIsIdempotent(t *testing.T) {
	e, g, scopes := setupEnv(t)
	out, errOut, err := e.run("", "app", "setup", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, errOut)
	}
	for _, want := range []string{"signed in as admin@contoso.test", "create the app registration", "tenant ID: tid-123", "client ID: ", "created the app registration", "consent is granted"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "Microsoft Graph Command Line Tools") || !strings.Contains(errOut, "ADMINCODE") {
		t.Errorf("stderr:\n%s", errOut)
	}
	if strings.Contains(strings.Join(*scopes, " "), "AppRoleAssignment") {
		t.Errorf("delegated setup asked for %v", *scopes)
	}
	if len(g.Apps) != 1 {
		t.Fatalf("%d apps", len(g.Apps))
	}
	g.ResetRequests()
	out, _, err = e.run("", "app", "setup", "--yes")
	if err != nil || !strings.Contains(out, "already in place; nothing to change") || len(g.Writes()) != 0 || len(g.Apps) != 1 {
		t.Fatalf("second run: %v writes %v\n%s", err, g.Writes(), out)
	}
}

func TestAppSetupWithoutTerminalNeedsYes(t *testing.T) {
	e, g, _ := setupEnv(t)
	_, _, err := e.run("", "app", "setup")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	if len(g.Requests) != 0 {
		t.Error("talked to Graph before refusing")
	}
}

func TestAppSetupAsksForConfirmation(t *testing.T) {
	e, g, _ := setupEnv(t)
	e.interactive = true
	_, _, err := e.run("n\n", "app", "setup")
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") || len(g.Apps) != 0 || len(g.Writes()) != 0 {
		t.Fatalf("declined: err = %v, apps %d", err, len(g.Apps))
	}
	if _, _, err := e.run("y\n", "app", "setup"); err != nil || len(g.Apps) != 1 {
		t.Fatalf("accepted: err = %v, apps %d", err, len(g.Apps))
	}
}

func TestAppSetupAdoptsTheHandMadeApp(t *testing.T) {
	e, g, _ := setupEnv(t)
	g.AddApp(map[string]any{"displayName": "e5renew", "appId": "c98746e0-17d4-412b-8ce8-33c7e49ba8a4"})

	// --yes alone refuses to guess.
	_, _, err := e.run("", "app", "setup", "--yes")
	if err == nil || !strings.Contains(err.Error(), "--adopt") || len(g.Writes()) != 0 {
		t.Fatalf("err = %v writes %v", err, g.Writes())
	}
	out, _, err := e.run("", "app", "setup", "--yes", "--adopt", "c98746e0-17d4-412b-8ce8-33c7e49ba8a4")
	if err != nil || !strings.Contains(out, "adopted the app registration \"E5 Developer Extend\"") || !strings.Contains(out, "client ID: c98746e0-17d4-412b-8ce8-33c7e49ba8a4") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if len(g.Apps) != 1 {
		t.Errorf("%d apps", len(g.Apps))
	}
}

func TestAppSetupInteractiveChoiceAndNoRename(t *testing.T) {
	e, g, _ := setupEnv(t)
	e.interactive = true
	g.AddApp(map[string]any{"displayName": "E5Renew"}) // not a candidate
	id := g.AddApp(map[string]any{"displayName": "E5 Renew"})
	// 1 = adopt, n = keep the name, y = go ahead
	out, errOut, err := e.run("1\nn\ny\n", "app", "setup")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, errOut)
	}
	if g.App(id)["displayName"] != "E5 Renew" || len(g.Apps) != 2 {
		t.Errorf("app = %v (%d apps)", g.App(id), len(g.Apps))
	}
	if !strings.Contains(out, "Found a similar app registration") {
		t.Errorf("candidates not shown:\n%s", out)
	}
}

func TestAppSetupInteractiveCreateNew(t *testing.T) {
	e, g, _ := setupEnv(t)
	e.interactive = true
	g.AddApp(map[string]any{"displayName": "e5extend"})
	if _, _, err := e.run("2\ny\n", "app", "setup"); err != nil || len(g.Apps) != 2 {
		t.Fatalf("err = %v apps = %d", err, len(g.Apps))
	}
}

func TestAppSetupApplicationIdentity(t *testing.T) {
	e, g, scopes := setupEnv(t)
	if _, _, err := e.run("", "app", "setup", "--app-only", "--yes"); err == nil || !strings.Contains(err.Error(), "--account") {
		t.Fatalf("missing --account: %v", err)
	}
	out, errOut, err := e.run("", "app", "setup", "--app-only", "--account", "app1", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, errOut)
	}
	if !strings.Contains(strings.Join(*scopes, " "), "AppRoleAssignment.ReadWrite.All") {
		t.Errorf("scopes = %v", *scopes)
	}
	secret := g.Secrets[0]
	if strings.Contains(out+errOut, secret) {
		t.Error("the secret was printed")
	}
	for _, want := range []string{"client secret \"e5extend dev-box ", "stored in the vault; expires ", "--stored-secret"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	e.mustRun("", "config", "init")
	cfg := e.loadCfg()
	v, err := vault.Open(cfg.Vault.Backend)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.Get(vault.SecretKey("app1")); err != nil || string(got) != secret {
		t.Fatalf("vault: %v", err)
	}
	// The printed next step works without a prompt.
	out = e.mustRun("", "account", "add", "app1", "--auth", "client_secret", "--tenant", "tid-123", "--client-id", g.Apps[0]["appId"].(string), "--stored-secret")
	if !strings.Contains(out, "added account") {
		t.Errorf("out = %s", out)
	}
}

func TestAccountAddAutoDelegated(t *testing.T) {
	e, g, _ := setupEnv(t)
	fa := &fakeAuth{tok: goodToken()}
	useAuth(e, fa)
	out, errOut, err := e.run("", "account", "add", "main", "--auto", "--auth", "device_code", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, errOut)
	}
	cfg := e.loadCfg()
	acct, ok := cfg.Find("main")
	if !ok || acct.Tenant != "tid-123" || acct.ClientID != g.Apps[0]["appId"] || acct.Auth != "device_code" {
		t.Fatalf("account = %+v", acct)
	}
	if fa.logins != 1 || !strings.Contains(out, "signed in as me@contoso.com") {
		t.Errorf("logins = %d\n%s", fa.logins, out)
	}
}

func TestAccountAddAutoApplicationIdentity(t *testing.T) {
	e, g, _ := setupEnv(t)
	fa := &fakeAuth{tok: goodToken()}
	useAuth(e, fa)
	out, errOut, err := e.run("", "account", "add", "app1", "--auto", "--auth", "client_secret", "--username", "u@contoso.test", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, errOut)
	}
	acct, ok := e.loadCfg().Find("app1")
	if !ok || acct.Auth != "client_secret" || acct.SecretExpires == "" || acct.Username != "u@contoso.test" || acct.Tenant != "tid-123" {
		t.Fatalf("account = %+v", acct)
	}
	if strings.Contains(out+errOut, g.Secrets[0]) {
		t.Error("the secret was printed")
	}
	cfg := e.loadCfg()
	v, _ := vault.Open(cfg.Vault.Backend)
	if got, err := v.Get(vault.SecretKey(acct.Key())); err != nil || string(got) != g.Secrets[0] {
		t.Errorf("vault secret: %v", err)
	}
	if !strings.Contains(out, "client secret verified") {
		t.Errorf("out = %s", out)
	}
}

func TestAccountAddAutoChecksTheNameFirst(t *testing.T) {
	e, g, _ := setupEnv(t)
	called := false
	e.bootstrap = func(context.Context, string, string, string, []string, auth.LoginUI, *http.Client) (auth.Token, error) {
		called = true
		return auth.Token{}, nil
	}
	addDelegated(e, "main")
	_, _, err := e.run("", "account", "add", "main", "--auto", "--auth", "device_code", "--yes")
	if err == nil || !strings.Contains(err.Error(), "already exists") || called || len(g.Requests) != 0 {
		t.Fatalf("err = %v called = %v", err, called)
	}
	for _, args := range [][]string{
		{"account", "add", "x", "--auto", "--auth", "password", "--yes"},
		{"account", "add", "x", "--auto", "--auth", "device_code", "--client-id", testGUID, "--yes"},
	} {
		if _, _, err := e.run("", args...); err == nil || called {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestAccountAddWithoutAutoStillNeedsTenantAndClient(t *testing.T) {
	e := newEnv(t)
	_, _, err := e.run("", "account", "add", "x", "--auth", "device_code")
	if err == nil || !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("err = %v", err)
	}
}

func TestAccountAddAutoForbiddenExplainsTheRoles(t *testing.T) {
	e, g, _ := setupEnv(t)
	g.Forbidden["POST /applications"] = true
	_, _, err := e.run("", "account", "add", "main", "--auto", "--auth", "device_code", "--yes")
	if err == nil || !strings.Contains(err.Error(), "Application Administrator") || !strings.Contains(err.Error(), "docs/user/app-registration.md") {
		t.Fatalf("err = %v", err)
	}
	e.mustRun("", "config", "init")
	if _, ok := e.loadCfg().Find("main"); ok {
		t.Error("an account was saved")
	}
}

func TestAccountAddAutoConsentFailureKeepsTheAccount(t *testing.T) {
	e, g, _ := setupEnv(t)
	g.Forbidden["POST /oauth2PermissionGrants"] = true
	fa := &fakeAuth{tok: goodToken()}
	useAuth(e, fa)
	out, errOut, err := e.run("", "account", "add", "main", "--auto", "--auth", "device_code", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, errOut)
	}
	if _, ok := e.loadCfg().Find("main"); !ok {
		t.Error("the account was not saved")
	}
	clientID := g.Apps[0]["appId"].(string)
	if !strings.Contains(errOut, "https://login.microsoftonline.com/tid-123/adminconsent?client_id="+clientID) || fa.logins != 0 {
		t.Errorf("logins %d\n%s", fa.logins, errOut)
	}
}

type flakyLogin struct {
	fakeAuth
	failures int
}

func (f *flakyLogin) Login(ctx context.Context, ui auth.LoginUI) (auth.Token, error) {
	if f.failures > 0 {
		f.failures--
		return auth.Token{}, errors.New("AADSTS700016: Application with identifier was not found in the directory")
	}
	return f.fakeAuth.Login(ctx, ui)
}

func TestAccountAddAutoWaitsForReplicationOnFirstLogin(t *testing.T) {
	e, _, _ := setupEnv(t)
	fa := &flakyLogin{fakeAuth: fakeAuth{tok: goodToken()}, failures: 2}
	e.newAuth = func(config.Account, auth.Store, *http.Client) (auth.Authenticator, error) { return fa, nil }
	out, errOut, err := e.run("", "account", "add", "main", "--auto", "--auth", "device_code", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, errOut)
	}
	if strings.Count(errOut, "replicate") != 2 || !strings.Contains(out, "signed in as") {
		t.Errorf("stderr:\n%s\nstdout:\n%s", errOut, out)
	}
}

func TestInitCreatesTheAppAutomatically(t *testing.T) {
	e, g, _ := setupEnv(t)
	e.interactive = true
	t.Setenv("DISPLAY", ":0")
	fa := &fakeAuth{tok: goodToken()}
	useAuth(e, fa)
	answers := strings.Join([]string{
		"1",    // master key: system keyring
		"y",    // add an account
		"1",    // create the app registration automatically
		"main", // name
		"1",    // device code
		"y",    // plan: go ahead
		"n",    // sign in again (the fake sign-in leaves no token cache, so it is offered)
		"n",    // first round
		"n",    // webhook
		"n",    // background job
	}, "\n") + "\n"
	out, errOut, err := e.run(answers, "init")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, errOut)
	}
	acct, ok := e.loadCfg().Find("main")
	if !ok || acct.ClientID != g.Apps[0]["appId"] || acct.Tenant != "tid-123" {
		t.Fatalf("account = %+v\n%s", acct, out)
	}
	if fa.logins != 1 {
		t.Errorf("signed in %d times, want the one from account add --auto", fa.logins)
	}
}

func TestAppSetupAdoptWhenATaggedAppExists(t *testing.T) {
	e, g, _ := setupEnv(t)
	e.mustRun("", "app", "setup", "--yes")
	own := g.Apps[0]["appId"].(string)
	hand := g.AddApp(map[string]any{"displayName": "e5renew"})
	handClient := g.App(hand)["appId"].(string)
	g.ResetRequests()

	_, _, err := e.run("", "app", "setup", "--yes", "--adopt", handClient)
	if err == nil || !strings.Contains(err.Error(), own) || !strings.Contains(err.Error(), "drop --adopt") || len(g.Writes()) != 0 {
		t.Fatalf("err = %v, writes %v", err, g.Writes())
	}
	// Naming the tagged app itself is only a reuse.
	if _, _, err := e.run("", "app", "setup", "--yes", "--adopt", own); err != nil {
		t.Fatalf("adopting the tagged app: %v", err)
	}
}

func TestAppSetupAdoptAndNewAlwaysExcludeEachOther(t *testing.T) {
	e, g, _ := setupEnv(t) // no candidates at all
	_, _, err := e.run("", "app", "setup", "--yes", "--adopt", testGUID, "--new")
	if err == nil || !strings.Contains(err.Error(), "exclude each other") || len(g.Writes()) != 0 {
		t.Fatalf("err = %v, writes %v", err, g.Writes())
	}
}

func TestAccountAddAutoRevokesTheSecretWhenAddingTheAccountFails(t *testing.T) {
	e, g, _ := setupEnv(t)
	fa := &fakeAuth{tok: goodToken()}
	useAuth(e, fa)
	e.mustRun("", "config", "init") // the hook below opens the vault through it
	// An earlier secret of this computer: removing it comes right after the new
	// secret was stored, and there the vault entry vanishes, so adding the account fails.
	id := g.AddApp(map[string]any{"displayName": "x", "tags": []string{"e5extend", "e5extend:app"}})
	g.Do(func() {
		g.Passwords[id] = []map[string]any{{"keyId": "k-old", "displayName": "e5extend dev-box app1 2026-01-01", "endDateTime": "2027-01-01T00:00:00Z"}}
	})
	once := false
	g.OnRequest = func(method, path string) {
		if once || !strings.HasSuffix(path, "/removePassword") {
			return
		}
		once = true
		cfg := e.loadCfg()
		v, err := vault.Open(cfg.Vault.Backend)
		if err != nil {
			t.Error(err)
			return
		}
		_ = v.Delete(vault.SecretKey("app1"))
	}
	out, errOut, err := e.run("", "account", "add", "app1", "--auto", "--auth", "client_secret", "--username", "u@contoso.test", "--yes")
	if err == nil || !strings.Contains(err.Error(), "adding the account failed") {
		t.Fatalf("err = %v\n%s\n%s", err, out, errOut)
	}
	if !strings.Contains(err.Error(), "deleted again") {
		t.Errorf("err = %v", err)
	}
	if names := g.PasswordNames(id); len(names) != 0 {
		// k-old was removed by the first removePassword, the new secret by the revoke.
		t.Errorf("a secret is still in Entra: %v", names)
	}
	if strings.Contains(err.Error()+out+errOut, g.Secrets[0]) {
		t.Error("the secret was printed")
	}
	if _, ok := e.loadCfg().Find("app1"); ok {
		t.Error("the account was added")
	}
}

func TestAppSetupNoNewSecretRepairsTheAccountsOwnApp(t *testing.T) {
	e, g, _ := setupEnv(t)
	objID := g.AddApp(map[string]any{"displayName": "E5 Developer Extend (app-only)", "appId": testGUID, "tags": []string{"e5extend", "e5extend:app"}})
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	e.mustRun("OLD-SECRET\n", "account", "add", "app1", "--auth", "client_secret", "--tenant", "tid-123", "--client-id", testGUID, "--username", "u@t.example", "--secret-expires", "2027-06-30")
	for _, bad := range [][]string{
		{"app", "setup", "--no-new-secret", "--yes"},
		{"app", "setup", "--app-only", "--no-new-secret", "--yes"},
		{"app", "setup", "--app-only", "--account", "nobody", "--no-new-secret", "--yes"},
	} {
		if _, _, err := e.run("", bad...); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
	if len(g.Writes()) != 0 {
		t.Fatalf("a refused command wrote: %v", g.Writes())
	}
	out, errOut, err := e.run("", "app", "setup", "--app-only", "--account", "app1", "--no-new-secret", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, errOut)
	}
	if strings.Contains(out, "create a client secret") || strings.Contains(out, "next: e5extend account add") || len(g.PasswordNames(objID)) != 0 {
		t.Errorf("a repair touched the secret:\n%s", out)
	}
	if !strings.Contains(out, "app1") {
		t.Errorf("output:\n%s", out)
	}
}

func TestAppSetupRepairWorksForBothKindsAndRejectsCreateFlags(t *testing.T) {
	e, g, _ := setupEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	g.AddApp(map[string]any{"displayName": "E5 Developer Extend", "appId": testGUID, "tags": []string{"e5extend", "e5extend:delegated"}})
	e.mustRun("", "account", "add", "dev", "--auth", "device_code", "--tenant", "tid-123", "--client-id", testGUID)
	for _, bad := range [][]string{
		{"app", "setup", "--repair", "--yes"},
		{"app", "setup", "--repair", "--account", "dev", "--tenant", "x", "--yes"},
		{"app", "setup", "--repair", "--account", "dev", "--adopt", testGUID, "--yes"},
		{"app", "setup", "--repair", "--account", "dev", "--new", "--yes"},
		{"app", "setup", "--repair", "--account", "dev", "--no-rename", "--yes"},
		{"app", "setup", "--repair", "--app-only", "--account", "dev", "--yes"},
		{"app", "setup", "--no-new-secret", "--account", "dev", "--yes"},
	} {
		if _, _, err := e.run("", bad...); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
	if len(g.Writes()) != 0 {
		t.Fatalf("a refused command wrote: %v", g.Writes())
	}
	out, errOut, err := e.run("", "app", "setup", "--repair", "--account", "dev", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, errOut)
	}
	if !strings.Contains(out, `account "dev"`) || len(g.Apps) != 1 {
		t.Errorf("out:\n%s (apps %d)", out, len(g.Apps))
	}
}
