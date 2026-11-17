package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

const guid = "11111111-2222-3333-4444-555555555555"

var now = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

type fakeVault struct {
	has map[string]bool
	err map[string]error
}

func (f fakeVault) Has(k string) (bool, error) { return f.has[k], nil }
func (f fakeVault) Get(k string) ([]byte, error) {
	if e := f.err[k]; e != nil {
		return nil, e
	}
	return []byte("x"), nil
}

type fakeAuth struct {
	tok auth.Token
	err error
}

func (f fakeAuth) Token(context.Context) (auth.Token, error)               { return f.tok, f.err }
func (f fakeAuth) Login(context.Context, auth.LoginUI) (auth.Token, error) { return f.tok, f.err }
func (f fakeAuth) Logout(context.Context) error                            { return nil }

type rig struct {
	t      *testing.T
	dir    string
	cfg    *config.Config
	cfgErr error
	v      fakeVault
	au     fakeAuth
	svc    service.Status
	svcErr error
	graph  *httptest.Server
	date   func() string
}

var manyScopes = []string{"User.Read", "Mail.Read", "Calendars.Read", "Contacts.Read", "Tasks.Read", "Files.Read"}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, dir: t.TempDir(), cfg: config.Default(),
		v:   fakeVault{has: map[string]bool{}, err: map[string]error{}},
		au:  fakeAuth{tok: auth.Token{AccessToken: "TOKEN", Scopes: manyScopes, ExpiresOn: now.Add(time.Hour), Account: "alice@example.com"}},
		svc: service.Status{Platform: "linux", Installed: true, Enabled: service.Yes, Running: service.Yes, Linger: service.Yes}}
	r.cfg.Accounts = []config.Account{{Name: "main", Auth: config.AuthDeviceCode, Tenant: "organizations", ClientID: guid}}
	r.date = func() string { return now.UTC().Format(http.TimeFormat) }
	r.graph = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Date", r.date())
		w.WriteHeader(401)
	}))
	t.Cleanup(r.graph.Close)
	return r
}

func (r *rig) deps() Deps {
	return Deps{
		Now: func() time.Time { return now }, GOOS: "linux", DataDir: r.dir, GraphBase: r.graph.URL,
		LoadConfig: func() (*config.Config, error) { return r.cfg, r.cfgErr },
		OpenVault:  func(*config.Config) (Vault, error) { return r.v, nil },
		NewAuth:    func(*config.Config, config.Account) (auth.Authenticator, error) { return r.au, nil },
		HTTP:       func(*config.Config) *http.Client { return r.graph.Client() },
		Service:    func() (service.Status, error) { return r.svc, r.svcErr },
		Hint:       func(error, string) string { return "hint" },
	}
}

func (r *rig) state(f func(as *store.AccountState)) {
	st := store.NewState()
	as := &store.AccountState{Status: store.StatusOK, LastSuccess: now.Add(-time.Hour)}
	f(as)
	st.Accounts["main"] = as
	if err := store.New(r.dir).SaveState(st); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) run(offline bool) map[string]Check {
	r.t.Helper()
	out := map[string]Check{}
	for _, c := range Run(context.Background(), r.deps(), Options{Offline: offline}) {
		out[c.ID] = c
	}
	return out
}

func expect(t *testing.T, got map[string]Check, id string, lvl Level, detail string) {
	t.Helper()
	c, ok := got[id]
	if !ok {
		t.Fatalf("no check %q in %v", id, ids(got))
	}
	if c.Level != lvl || !strings.Contains(c.Detail, detail) {
		t.Errorf("%s = %s %q, want %s containing %q", id, c.Level, c.Detail, lvl, detail)
	}
}

func ids(m map[string]Check) []string {
	var s []string
	for k := range m {
		s = append(s, k)
	}
	return s
}

func TestAllGood(t *testing.T) {
	r := newRig(t)
	rel, err := engine.AcquireLock(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	_ = engine.WriteHeartbeat(r.dir, now.Add(-10*time.Second))
	r.v.has[notifyKey] = true
	r.state(func(*store.AccountState) {})
	got := r.run(false)
	for id, c := range got {
		if c.Level != OK {
			t.Errorf("%s = %s %q", id, c.Level, c.Detail)
		}
	}
	for _, id := range []string{"config", "vault", "engine", "service", "linger", "account.main.token", "account.main.scopes",
		"account.main.activity", "account.main.state", "clock", "network", "notify"} {
		if _, ok := got[id]; !ok {
			t.Errorf("missing %s", id)
		}
	}
	if ExitCode(flatten(got)) != 0 {
		t.Error("exit code not 0")
	}
}

const notifyKey = "notify/webhook"

func flatten(m map[string]Check) []Check {
	var out []Check
	for _, c := range m {
		out = append(out, c)
	}
	return out
}

func TestConfigChecks(t *testing.T) {
	r := newRig(t)
	r.cfgErr = fmt.Errorf("%w: x", config.ErrNotFound)
	got := r.run(true)
	expect(t, got, "config", Fail, "no configuration")
	if _, ok := got["vault"]; ok {
		t.Error("vault checked without a configuration")
	}
	r.cfgErr = errors.New("accounts[0].client_id: not a GUID\naccounts[0].tenant: required")
	expect(t, r.run(true), "config", Fail, "not a GUID")
	r.cfgErr = nil
	r.cfg.Accounts = nil
	expect(t, r.run(true), "config", Warn, "no accounts")
}

func TestVaultChecks(t *testing.T) {
	r := newRig(t)
	expect(t, r.run(true), "vault", OK, "no encrypted data")
	r.v.has[vault.TokenCacheKey("main")] = true
	expect(t, r.run(true), "vault", OK, "decrypt")
	r.v.err[vault.TokenCacheKey("main")] = vault.ErrWrongKey
	expect(t, r.run(true), "vault", Fail, "wrong master key")
	r.v.err[vault.TokenCacheKey("main")] = vault.ErrNoMasterKey
	expect(t, r.run(true), "vault", Fail, "no master key")
	r.v.err = map[string]error{}
	r.cfg.Accounts[0].Auth = config.AuthClientSecret
	expect(t, r.run(true), "vault", Fail, "secret is missing")
}

func TestEngineChecks(t *testing.T) {
	r := newRig(t)
	expect(t, r.run(true), "engine", Fail, "not running")
	rel, err := engine.AcquireLock(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	_ = engine.WriteHeartbeat(r.dir, now.Add(-10*time.Minute))
	expect(t, r.run(true), "engine", Warn, "older than 2 minutes")
	_ = engine.WriteHeartbeat(r.dir, now.Add(-time.Minute))
	expect(t, r.run(true), "engine", OK, "running")
}

func TestServiceAndLinger(t *testing.T) {
	r := newRig(t)
	r.svc = service.Status{}
	got := r.run(true)
	expect(t, got, "service", Warn, "not installed")
	if _, ok := got["linger"]; ok {
		t.Error("linger checked without a service")
	}
	r.svc = service.Status{Installed: true, Enabled: service.No}
	expect(t, r.run(true), "service", Warn, "disabled")
	r.svc = service.Status{Installed: true, Enabled: service.Yes, Running: service.No, Linger: service.No}
	got = r.run(true)
	expect(t, got, "service", Warn, "not running")
	expect(t, got, "linger", Warn, "stops when you log out")
	r.svc = service.Status{Installed: true, Enabled: service.Yes, Running: service.Unknown}
	expect(t, r.run(true), "service", OK, "enabled")
	r.svcErr = errors.New("no bus")
	expect(t, r.run(true), "service", Warn, "cannot ask")
	// Linger is a Linux-only check.
	r.svcErr = nil
	r.svc = service.Status{Installed: true, Enabled: service.Yes, Linger: service.No}
	d := r.deps()
	d.GOOS = "darwin"
	for _, c := range Run(context.Background(), d, Options{Offline: true}) {
		if c.ID == "linger" {
			t.Error("linger on darwin")
		}
	}
}

func TestTokenChecks(t *testing.T) {
	r := newRig(t)
	cases := []struct {
		err    error
		lvl    Level
		detail string
	}{
		{auth.ErrLoginRequired, Fail, "login required"},
		{fmt.Errorf("%w: AADSTS70043: sign-in frequency checks", auth.ErrLoginRequired), Fail, "sign-in frequency policy"},
		{auth.ErrInvalidClient, Fail, "credentials rejected"},
		{auth.ErrConsentRequired, Fail, "consent"},
		{fmt.Errorf("dial: %w", auth.ErrTransient), Warn, "network"},
		{vault.ErrNoMasterKey, Fail, "master key"},
		{errors.New("AADSTS99999 tenant contoso.com user bob@contoso.com"), Fail, "sign-in failed"},
	}
	for _, c := range cases {
		r.au.err = c.err
		got := r.run(false)
		expect(t, got, "account.main.token", c.lvl, c.detail)
		if strings.Contains(got["account.main.token"].Detail, "bob@") || strings.Contains(got["account.main.token"].Detail, "contoso") {
			t.Errorf("detail leaks provider text: %q", got["account.main.token"].Detail)
		}
	}
}

func TestOfflineSkipsNetworkChecks(t *testing.T) {
	r := newRig(t)
	r.au.err = auth.ErrLoginRequired
	got := r.run(true)
	for _, id := range []string{"account.main.token", "clock", "network"} {
		if _, ok := got[id]; ok {
			t.Errorf("%s ran offline", id)
		}
	}
	if r.graph.Config.Handler == nil {
		t.Fatal("unreachable")
	}
	// Scopes fall back to what the last round recorded.
	if _, ok := got["account.main.scopes"]; ok {
		t.Error("scopes without any record")
	}
	r.state(func(as *store.AccountState) { as.GrantedScopes = manyScopes })
	expect(t, r.run(true), "account.main.scopes", OK, "recorded")
}

func TestScopes(t *testing.T) {
	r := newRig(t)
	r.au.tok.Scopes = []string{"Unrelated.Thing"}
	expect(t, r.run(false), "account.main.scopes", Fail, "no API")
	r.au.tok.Scopes = []string{"User.Read"}
	expect(t, r.run(false), "account.main.scopes", Warn, "usable")
	r.au.tok.Scopes = manyScopes
	expect(t, r.run(false), "account.main.scopes", OK, "usable")
}

func TestActivity(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		age  time.Duration
		zero bool
		lvl  Level
	}{{time.Hour, false, OK}, {80 * time.Hour, false, Warn}, {8 * 24 * time.Hour, false, Fail}, {0, true, Warn}} {
		r.state(func(as *store.AccountState) {
			as.LastSuccess = now.Add(-c.age)
			if c.zero {
				as.LastSuccess = time.Time{}
			}
		})
		expect(t, r.run(true), "account.main.activity", c.lvl, "")
	}
	r.cfg.Accounts[0].Paused = true
	expect(t, r.run(true), "account.main.activity", OK, "paused")
}

func TestAccountState(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		status string
		lvl    Level
	}{{store.StatusOK, OK}, {store.StatusNeedsAttention, Fail}, {store.StatusPaused, Fail}, {store.StatusPeriodEnded, Warn}} {
		r.state(func(as *store.AccountState) { as.Status, as.Reason = c.status, "because" })
		got := r.run(true)
		expect(t, got, "account.main.state", c.lvl, "")
		if c.lvl != OK && got["account.main.state"].Fix == "" {
			t.Errorf("%s has no fix", c.status)
		}
	}
	r.cfg.Accounts[0].Paused = true
	expect(t, r.run(true), "account.main.state", Warn, "configuration")
}

func TestSecretExpiry(t *testing.T) {
	r := newRig(t)
	r.cfg.Accounts[0].Auth = config.AuthClientSecret
	r.v.has[vault.SecretKey("main")] = true
	for _, c := range []struct {
		date string
		lvl  Level
	}{{"2027-06-01", OK}, {"2026-12-10", Warn}, {"2026-12-17", Warn}, {"2026-11-24", Fail}, {"2026-11-17", Fail}, {"2026-11-01", Fail}} {
		r.cfg.Accounts[0].SecretExpires = c.date
		expect(t, r.run(true), "account.main.secret", c.lvl, "")
	}
	r.cfg.Accounts[0].SecretExpires = ""
	if _, ok := r.run(true)["account.main.secret"]; ok {
		t.Error("secret check without a date")
	}
}

func TestNetworkAndClock(t *testing.T) {
	r := newRig(t)
	got := r.run(false)
	expect(t, got, "network", OK, "HTTP 401")
	expect(t, got, "clock", OK, "within")
	r.date = func() string { return now.Add(-3 * time.Minute).UTC().Format(http.TimeFormat) }
	expect(t, r.run(false), "clock", Warn, "ahead")
	r.date = func() string { return now.Add(10 * time.Minute).UTC().Format(http.TimeFormat) }
	expect(t, r.run(false), "clock", Fail, "behind")
	r.date = func() string { return "" }
	expect(t, r.run(false), "clock", Warn, "Date")

	r.graph.Close()
	got = r.run(false)
	expect(t, got, "network", Fail, "cannot reach")
	if strings.Contains(got["network"].Detail, "127.0.0.1:") && strings.Contains(got["network"].Detail, "http://") {
		t.Errorf("detail repeats the URL: %q", got["network"].Detail)
	}
}

func TestNotifyCheck(t *testing.T) {
	r := newRig(t)
	expect(t, r.run(true), "notify", Warn, "no alert destination")
	r.v.has[notifyKey] = true
	expect(t, r.run(true), "notify", OK, "vault")
	r.v.err[notifyKey] = vault.ErrWrongKey
	got0 := r.run(true)
	expect(t, got0, "notify", Warn, "cannot be decrypted")
	if strings.Contains(got0["notify"].Detail+got0["notify"].Fix, "SECRET") {
		t.Error("address shown")
	}
	r.v.err = map[string]error{}
	r.cfg.Notify.WebhookURL = "https://hooks.example/SECRETTOKEN"
	got := r.run(true)
	expect(t, got, "notify", Warn, "plain text")
	if strings.Contains(got["notify"].Detail+got["notify"].Fix, "SECRETTOKEN") {
		t.Error("webhook address shown")
	}
}

func TestOutputAndExitCodes(t *testing.T) {
	checks := []Check{{ID: "a", Title: "A", Level: OK, Detail: "fine"}, {ID: "b", Title: "B", Level: Warn, Detail: "hmm", Fix: "do x"}}
	if ExitCode(checks) != 1 {
		t.Error("warn must give 1")
	}
	if ExitCode(append(checks, Check{Level: Fail})) != 2 {
		t.Error("fail must give 2")
	}
	if ExitCode(nil) != 0 {
		t.Error("empty must give 0")
	}
	var buf bytes.Buffer
	WriteText(&buf, checks, false, true)
	out := buf.String()
	for _, want := range []string{"✓ A: fine", "! B: hmm", "    fix: do x", "1 ok, 1 warnings, 0 failures", "offline"} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
	buf.Reset()
	WriteText(&buf, checks, true, false)
	if !strings.Contains(buf.String(), "ok A") || strings.Contains(buf.String(), "✓") {
		t.Errorf("ascii output:\n%s", buf.String())
	}
	buf.Reset()
	if err := WriteJSON(&buf, checks); err != nil {
		t.Fatal(err)
	}
	var back []Check
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil || len(back) != 2 || back[1].Fix != "do x" {
		t.Errorf("json: %v %s", err, buf.String())
	}
	buf.Reset()
	_ = WriteJSON(&buf, nil)
	if strings.TrimSpace(buf.String()) != "[]" {
		t.Errorf("nil json = %q", buf.String())
	}
}

func TestPanicInACheckIsContained(t *testing.T) {
	r := newRig(t)
	d := r.deps()
	d.Service = func() (service.Status, error) { panic("boom") }
	var found bool
	for _, c := range Run(context.Background(), d, Options{Offline: true}) {
		if c.ID == "service" && c.Level == Fail {
			found = true
		}
	}
	if !found {
		t.Error("panic not reported as a failed check")
	}
}
