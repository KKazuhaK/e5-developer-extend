package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/KKazuhaK/e5-renew/internal/auth"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/service"
	"github.com/KKazuhaK/e5-renew/internal/vault"
)

const testGUID = "11111111-2222-3333-4444-555555555555"

type env struct {
	t         *testing.T
	cfgDir    string
	dataDir   string
	newAuth   func(acct config.Account, store auth.Store, c *http.Client) (auth.Authenticator, error)
	graphBase string

	runner      *service.FakeRunner // never a real systemctl, launchctl, schtasks or loginctl
	exe         string
	home        string
	interactive bool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	keyring.MockInit()
	e := &env{t: t, cfgDir: t.TempDir(), dataDir: t.TempDir(), home: t.TempDir(),
		runner: &service.FakeRunner{}, exe: filepath.Join(string(filepath.Separator), "opt", "e5extend", "e5extend")}
	t.Setenv("E5EXTEND_CONFIG_DIR", e.cfgDir)
	t.Setenv("E5EXTEND_DATA_DIR", e.dataDir)
	t.Setenv(vault.EnvMasterKey, "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	// Results must not depend on whether the tests run over SSH.
	for _, v := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		t.Setenv(v, "")
	}
	return e
}

func (e *env) run(stdin string, args ...string) (string, string, error) {
	e.t.Helper()
	return e.runCtx(context.Background(), stdin, args...)
}

func (e *env) runCtx(ctx context.Context, stdin string, args ...string) (string, string, error) {
	e.t.Helper()
	a := newApp()
	a.graphBase = e.graphBase
	a.noCallGap = true
	a.runner = e.runner
	a.exe = func() (string, error) { return e.exe, nil }
	a.svc = svcIdentity{Home: e.home, UID: "501", User: "tester"}
	a.interactive = func(io.Reader) bool { return e.interactive }
	a.webhookBackoff = []time.Duration{time.Millisecond}
	if e.newAuth != nil {
		a.newAuth = e.newAuth
	}
	cmd := newRootCmdWith(a)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return out.String(), errOut.String(), err
}

func (e *env) mustRun(stdin string, args ...string) string {
	e.t.Helper()
	out, errOut, err := e.run(stdin, args...)
	if err != nil {
		e.t.Fatalf("%v failed: %v\nstdout: %s\nstderr: %s", args, err, out, errOut)
	}
	return out
}

func (e *env) loadCfg() *config.Config {
	e.t.Helper()
	cfg, err := config.Load(filepath.Join(e.cfgDir, "config.toml"))
	if err != nil {
		e.t.Fatal(err)
	}
	return cfg
}

func TestConfigPath(t *testing.T) {
	e := newEnv(t)
	out := e.mustRun("", "config", "path")
	if !strings.Contains(out, filepath.Join(e.cfgDir, "config.toml")) || !strings.Contains(out, e.dataDir) {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestConfigInitAndValidate(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.run("", "config", "validate"); err == nil {
		t.Fatal("validate before init should fail")
	}
	out := e.mustRun("", "config", "init")
	if !strings.Contains(out, "created") {
		t.Fatalf("init output: %q", out)
	}
	path := filepath.Join(e.cfgDir, "config.toml")
	if err := os.WriteFile(path, append([]byte("# mine\n"), mustRead(t, path)...), 0o600); err != nil {
		t.Fatal(err)
	}
	out = e.mustRun("", "config", "init")
	if !strings.Contains(out, "already exists") || !strings.HasPrefix(string(mustRead(t, path)), "# mine") {
		t.Fatalf("init must not touch an existing file: %q", out)
	}
	if out = e.mustRun("", "config", "validate"); !strings.Contains(out, "valid") {
		t.Fatalf("validate output: %q", out)
	}
}

func TestConfigValidateListsProblems(t *testing.T) {
	e := newEnv(t)
	bad := "version = 1\n[schedule]\ninterval_min = \"x\"\nwork_start = \"9\"\n[[accounts]]\nname = \"a b\"\nauth = \"nope\"\n"
	if err := os.WriteFile(filepath.Join(e.cfgDir, "config.toml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := e.run("", "config", "validate")
	if err == nil {
		t.Fatal("expected failure")
	}
	for _, want := range []string{"schedule.interval_min", "schedule.work_start", "accounts[0].name", "accounts[0].auth", "accounts[0].client_id"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAccountLifecycle(t *testing.T) {
	e := newEnv(t)
	e.mustRun("", "config", "init")

	e.mustRun("", "account", "add", "main", "--auth", "device_code", "--tenant", "contoso.onmicrosoft.com", "--client-id", testGUID)
	e.mustRun("s3cr3t-value\n", "account", "add", "svc", "--auth", "client_secret",
		"--tenant", "contoso.onmicrosoft.com", "--client-id", testGUID, "--username", "u@contoso.com", "--secret-expires", "2099-01-01")

	cfg := e.loadCfg()
	if len(cfg.Accounts) != 2 {
		t.Fatalf("accounts = %d", len(cfg.Accounts))
	}
	raw := mustRead(t, filepath.Join(e.cfgDir, "config.toml"))
	if bytes.Contains(raw, []byte("s3cr3t-value")) {
		t.Fatal("secret leaked into config")
	}
	if !bytes.HasPrefix(raw, []byte("# Managed by e5extend")) {
		t.Fatal("saved config lacks the header comment")
	}

	// The secret is in the vault, encrypted.
	key, _, err := vault.ResolveMasterKey("auto", "")
	if err != nil {
		t.Fatal(err)
	}
	v := vault.New(filepath.Join(e.dataDir, "vault"), func() ([]byte, error) { return key, nil })
	got, err := v.Get("secret/svc")
	if err != nil || string(got) != "s3cr3t-value" {
		t.Fatalf("vault secret = %q, %v", got, err)
	}

	out := e.mustRun("", "account", "list")
	for _, want := range []string{"main", "device_code", "not logged in", "svc", "client_secret", "secret stored", "2099-01-01"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "s3cr3t-value") {
		t.Fatal("list leaked the secret")
	}

	if _, _, err := e.run("", "account", "add", "main", "--auth", "device_code", "--tenant", "t", "--client-id", testGUID); err == nil {
		t.Fatal("duplicate add should fail")
	}

	// Without --yes and without confirmation nothing is removed.
	if _, _, err := e.run("n\n", "account", "remove", "svc"); err == nil {
		t.Fatal("declined remove should fail")
	}
	if len(e.loadCfg().Accounts) != 2 {
		t.Fatal("account removed despite refusal")
	}
	if err := v.Put("tokencache/svc", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	e.mustRun("", "account", "remove", "svc", "--yes")
	if _, err := v.Get("secret/svc"); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("secret should be deleted: %v", err)
	}
	if _, err := v.Get("tokencache/svc"); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("token cache should be deleted: %v", err)
	}
	e.mustRun("y\n", "account", "remove", "main")
	if n := len(e.loadCfg().Accounts); n != 0 {
		t.Fatalf("accounts left: %d", n)
	}
	if _, _, err := e.run("", "account", "remove", "main", "--yes"); err == nil {
		t.Fatal("removing a missing account should fail")
	}
}

func TestAccountAddValidation(t *testing.T) {
	e := newEnv(t)
	cases := [][]string{
		{"UpperCase", "--auth", "device_code", "--tenant", "t", "--client-id", testGUID},
		{"con", "--auth", "device_code", "--tenant", "t", "--client-id", testGUID},
		{"lpt3", "--auth", "device_code", "--tenant", "t", "--client-id", testGUID},
		{"bad name", "--auth", "device_code", "--tenant", "t", "--client-id", testGUID},
		{"x", "--auth", "device_code", "--tenant", "t", "--client-id", "nope"},
		{"x", "--auth", "client_secret", "--tenant", "common", "--client-id", testGUID},
		{"x", "--auth", "password", "--tenant", "t", "--client-id", testGUID},
		{"x", "--auth", "weird", "--tenant", "t", "--client-id", testGUID},
	}
	for _, c := range cases {
		args := append([]string{"account", "add"}, c...)
		if _, _, err := e.run("pw\n", args...); err == nil {
			t.Errorf("expected failure for %v", c)
		}
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, "config.toml")); err == nil {
		t.Error("config written despite failures")
	}
	if _, _, err := e.run("\n", "account", "add", "x", "--auth", "client_secret", "--tenant", "t", "--client-id", testGUID); err == nil {
		t.Error("empty secret accepted")
	}
}

type fakeAuth struct {
	tok    auth.Token
	err    error
	logins int
}

func (f *fakeAuth) Token(context.Context) (auth.Token, error) { return f.tok, f.err }
func (f *fakeAuth) Login(_ context.Context, ui auth.LoginUI) (auth.Token, error) {
	f.logins++
	ui.DeviceCode("go to https://example.invalid/device and enter CODE123")
	return f.tok, f.err
}
func (f *fakeAuth) Logout(context.Context) error { return nil }

func TestLoginWritesHomeAccountID(t *testing.T) {
	e := newEnv(t)
	fa := &fakeAuth{tok: auth.Token{
		AccessToken: "SUPER-SECRET-TOKEN", HomeAccountID: "uid.tid", Account: "me@contoso.com",
		ExpiresOn: time.Now().Add(time.Hour), Scopes: []string{"Mail.Read", "User.Read"},
	}}
	e.newAuth = func(config.Account, auth.Store, *http.Client) (auth.Authenticator, error) { return fa, nil }

	e.mustRun("", "account", "add", "main", "--auth", "device_code", "--tenant", "t.example", "--client-id", testGUID)
	out, errOut, err := e.run("", "account", "login", "main")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "CODE123") || !strings.Contains(out, "me@contoso.com") || !strings.Contains(out, "Mail.Read") {
		t.Fatalf("out=%q err=%q", out, errOut)
	}
	if strings.Contains(out+errOut, "SUPER-SECRET-TOKEN") {
		t.Fatal("token printed")
	}
	if got := e.loadCfg().Accounts[0].HomeAccountID; got != "uid.tid" {
		t.Fatalf("home_account_id = %q", got)
	}

	out = e.mustRun("", "account", "test", "main")
	if !strings.Contains(out, "me@contoso.com") || strings.Contains(out, "SUPER-SECRET-TOKEN") {
		t.Fatalf("test output: %q", out)
	}
}

func TestAccountTestHints(t *testing.T) {
	e := newEnv(t)
	fa := &fakeAuth{}
	e.newAuth = func(config.Account, auth.Store, *http.Client) (auth.Authenticator, error) { return fa, nil }
	e.mustRun("", "account", "add", "main", "--auth", "browser", "--tenant", "t.example", "--client-id", testGUID)

	tests := []struct {
		err  error
		want string
	}{
		{auth.ErrLoginRequired, "run: e5extend account login main"},
		{auth.ErrConsentRequired, "grant consent"},
		{auth.ErrInvalidClient, "client secret"},
		{auth.ErrTransient, "connectivity"},
		{vault.ErrNoMasterKey, "vault init"},
	}
	for _, tt := range tests {
		fa.err = tt.err
		_, _, err := e.run("", "account", "test", "main")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("err %v: got %v, want hint %q", tt.err, err, tt.want)
		}
	}
	if _, _, err := e.run("", "account", "test", "missing"); err == nil {
		t.Error("unknown account should fail")
	}
}

func TestAccountTestRealAuthenticatorNotLoggedIn(t *testing.T) {
	e := newEnv(t)
	e.mustRun("", "account", "add", "main", "--auth", "device_code", "--tenant", "t.example", "--client-id", testGUID)
	_, _, err := e.run("", "account", "test", "main")
	if err == nil || !errors.Is(err, auth.ErrLoginRequired) || !strings.Contains(err.Error(), "e5extend account login main") {
		t.Fatalf("got %v", err)
	}
}

func TestVaultInit(t *testing.T) {
	e := newEnv(t)
	out, errOut, err := e.run("", "vault", "init", "--print-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.DecodeKey(strings.TrimSpace(out)); err != nil {
		t.Fatalf("stdout must be only the key: %q (%v)", out, err)
	}
	if !strings.Contains(errOut, vault.EnvMasterKey) {
		t.Errorf("stderr advice missing: %q", errOut)
	}
	if _, _, err := vault.ResolveMasterKey("keyring", ""); err == nil {
		t.Fatal("--print-key must not store anything")
	}

	if out = e.mustRun("", "vault", "init"); !strings.Contains(out, "stored") {
		t.Fatalf("init: %q", out)
	}
	if out = e.mustRun("", "vault", "init"); !strings.Contains(out, "already exists") {
		t.Fatalf("second init: %q", out)
	}
}

func TestPrintTemplate(t *testing.T) {
	e := newEnv(t)
	out := e.mustRun("", "config", "init", "--print-template")
	if !strings.Contains(out, "[schedule]") {
		t.Fatalf("template: %q", out)
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, "config.toml")); err == nil {
		t.Fatal("--print-template must not write a file")
	}
}

func TestVaultReset(t *testing.T) {
	e := newEnv(t)
	// A vault with data but no master key anywhere.
	other, _ := vault.GenerateKey()
	vdir := filepath.Join(e.dataDir, "vault")
	if err := vault.New(vdir, func() ([]byte, error) { return other, nil }).Put("secret/x", []byte("1")); err != nil {
		t.Fatal(err)
	}
	e.mustRun("", "config", "init")
	_, _, err := e.run("pw\n", "account", "add", "svc", "--auth", "client_secret", "--tenant", "t.example", "--client-id", testGUID)
	if !errors.Is(err, vault.ErrNoMasterKey) || !strings.Contains(err.Error(), "vault reset") {
		t.Fatalf("add with orphaned vault: %v", err)
	}
	if _, _, err := e.run("", "vault", "init"); !errors.Is(err, vault.ErrNoMasterKey) {
		t.Fatalf("vault init with data: %v", err)
	}
	if _, _, err := e.run("n\n", "vault", "reset"); err == nil {
		t.Fatal("declined reset must fail")
	}
	e.mustRun("", "vault", "reset", "--yes")
	e.mustRun("pw\n", "account", "add", "svc", "--auth", "client_secret", "--tenant", "t.example", "--client-id", testGUID)
}

func TestSetSecret(t *testing.T) {
	e := newEnv(t)
	e.mustRun("old\n", "account", "add", "svc", "--auth", "client_secret", "--tenant", "t.example", "--client-id", testGUID)
	e.mustRun("new-value\n", "account", "set-secret", "svc", "--secret-expires", "2098-05-06")
	if got := e.loadCfg().Accounts[0].SecretExpires; got != "2098-05-06" {
		t.Fatalf("secret_expires = %q", got)
	}
	key, _, _ := vault.ResolveMasterKey("auto", "")
	v := vault.New(filepath.Join(e.dataDir, "vault"), func() ([]byte, error) { return key, nil })
	if got, _ := v.Get("secret/svc"); string(got) != "new-value" {
		t.Fatalf("secret = %q", got)
	}
	if _, _, err := e.run("\n", "account", "set-secret", "svc"); err == nil {
		t.Fatal("empty secret accepted")
	}
	e.mustRun("", "account", "add", "dev", "--auth", "device_code", "--tenant", "t.example", "--client-id", testGUID)
	if _, _, err := e.run("x\n", "account", "set-secret", "dev"); err == nil {
		t.Fatal("set-secret on an account without secret must fail")
	}
}

func TestInterruptedPromptStoresNothing(t *testing.T) {
	e := newEnv(t)
	pr, pw := io.Pipe()
	defer pw.Close()
	cmd := newRootCmdWith(newApp())
	cmd.SetIn(pr)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"account", "add", "svc", "--auth", "client_secret", "--tenant", "t.example", "--client-id", testGUID})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, errInterrupted) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt did not abort")
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, "config.toml")); err == nil {
		t.Fatal("config written")
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, "vault")); err == nil {
		t.Fatal("vault written")
	}
}

func TestMFAAndPasswordHints(t *testing.T) {
	e := newEnv(t)
	fa := &fakeAuth{}
	e.newAuth = func(config.Account, auth.Store, *http.Client) (auth.Authenticator, error) { return fa, nil }
	e.mustRun("pw\n", "account", "add", "p", "--auth", "password", "--username", "u@t.example", "--tenant", "t.example", "--client-id", testGUID)
	body := func(code string) error {
		return auth.Classify(errors.New(`{"error":"invalid_grant","error_description":"AADSTS` + code + `: x","error_codes":[` + code + `]}`))
	}
	for code, want := range map[string]string{"50126": "set-secret p", "50076": "requires MFA", "50079": "requires MFA"} {
		fa.err = body(code)
		_, _, err := e.run("", "account", "test", "p")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("AADSTS%s: got %v, want hint %q", code, err, want)
		}
	}
	fa.err = fmt.Errorf("%w: cache: %w", auth.ErrLoginRequired, vault.ErrWrongKey)
	if _, _, err := e.run("", "account", "test", "p"); err == nil || !strings.Contains(err.Error(), "discards the unreadable cache") {
		t.Errorf("wrong-key hint: %v", err)
	}
}

// Windows scheduled tasks cannot set environment variables, so the job gets
// --config-dir and --data-dir instead; they must win over the environment.
func TestDirFlagsOverrideEnvironment(t *testing.T) {
	e := newEnv(t)
	cfg, data := t.TempDir(), t.TempDir()
	out, _, err := e.run("", "--config-dir", cfg, "--data-dir", data, "config", "path")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, filepath.Join(cfg, "config.toml")) || !strings.Contains(out, data) {
		t.Errorf("flags not applied:\n%s", out)
	}
}
