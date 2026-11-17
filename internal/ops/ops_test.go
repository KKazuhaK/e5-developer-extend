package ops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/doctor"
	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

const testGUID = "11111111-2222-3333-4444-555555555555"

type fakeAuth struct {
	tok  auth.Token
	err  error
	ui   string
	logs int
}

func (f *fakeAuth) Token(context.Context) (auth.Token, error) { return f.tok, f.err }
func (f *fakeAuth) Login(_ context.Context, ui auth.LoginUI) (auth.Token, error) {
	f.logs++
	if f.ui != "" {
		ui.DeviceCode(f.ui)
	}
	return f.tok, f.err
}
func (f *fakeAuth) Logout(context.Context) error { return nil }

type testEnv struct {
	*Ops
	cfgDir, dataDir string
	fa              *fakeAuth
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	keyring.MockInit()
	e := &testEnv{cfgDir: t.TempDir(), dataDir: t.TempDir(), fa: &fakeAuth{tok: auth.Token{
		AccessToken: "TOKEN-SECRET", HomeAccountID: "uid.tid", Account: "me@contoso.com",
		ExpiresOn: time.Now().Add(time.Hour), Scopes: []string{"User.Read", "Mail.Read"},
	}}}
	t.Setenv("E5EXTEND_CONFIG_DIR", e.cfgDir)
	t.Setenv("E5EXTEND_DATA_DIR", e.dataDir)
	t.Setenv(vault.EnvMasterKey, "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	e.Ops = &Ops{
		NewAuth:   func(config.Account, auth.Store, *http.Client) (auth.Authenticator, error) { return e.fa, nil },
		NoCallGap: true, WebhookBackoff: []time.Duration{time.Millisecond},
	}
	return e
}

func (e *testEnv) addDevice(t *testing.T, name string) {
	t.Helper()
	in := AccountInput{Name: name, Auth: config.AuthDeviceCode, Tenant: "t.example", ClientID: testGUID}
	if err := e.AddAccount(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func TestConfigInitIsIdempotent(t *testing.T) {
	e := newTestEnv(t)
	path, created, err := e.ConfigInit(context.Background())
	if err != nil || !created {
		t.Fatalf("first init: created=%v err=%v", created, err)
	}
	if err := os.WriteFile(path, append([]byte("# mine\n"), mustRead(t, path)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, created, err = e.ConfigInit(context.Background()); err != nil || created {
		t.Fatalf("second init: created=%v err=%v", created, err)
	}
	if !strings.HasPrefix(string(mustRead(t, path)), "# mine") {
		t.Fatal("init rewrote an existing file")
	}
	rep, err := e.ValidateConfig()
	if err != nil || len(rep.Problems) != 0 {
		t.Fatalf("validate: %+v %v", rep, err)
	}
}

func TestValidateConfigListsProblems(t *testing.T) {
	e := newTestEnv(t)
	if _, err := e.ValidateConfig(); err == nil {
		t.Fatal("missing config must be an error")
	}
	bad := "version = 1\n[schedule]\ninterval_min = \"x\"\nwork_start = \"9\"\n"
	if err := os.WriteFile(filepath.Join(e.cfgDir, "config.toml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := e.ValidateConfig()
	if err != nil || len(rep.Problems) < 2 {
		t.Fatalf("report %+v err %v", rep, err)
	}
}

func TestSettingsRoundTripAndValidation(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	s, err := e.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.Schedule.IntervalMin != "40m" || s.Notify.WebhookConfigured || s.Schedule.WorkDays == nil {
		t.Fatalf("defaults: %+v", s)
	}

	s.Schedule.IntervalMin = "1m"
	s.Schedule.WorkDays = []string{"mon", "xyz"}
	s.Schedule.PeriodStart = "not-a-date"
	s.Notify.StaleAfter = "10m"
	bad, err := e.SaveSettings(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	for _, b := range bad {
		fields[b.Field] = true
		if b.Message == "" {
			t.Errorf("empty message for %s", b.Field)
		}
	}
	for _, want := range []string{"schedule.intervalMin", "schedule.workDays", "schedule.periodStart", "notify.staleAfter"} {
		if !fields[want] {
			t.Errorf("missing validation error for %s: %+v", want, bad)
		}
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, "config.toml")); err == nil {
		t.Fatal("an invalid save must not write the file")
	}

	e.addDevice(t, "main")
	s, _ = e.LoadSettings()
	s.Schedule.IntervalMin, s.Schedule.IntervalMax = "30m", "3h"
	s.Schedule.PeriodStart, s.Schedule.PeriodEnd = "2026-12-01", "2027-12-01"
	s.Network.Proxy = "http://127.0.0.1:3128"
	s.Notify.Events = []string{"stale"}
	if bad, err = e.SaveSettings(ctx, s); err != nil || len(bad) != 0 {
		t.Fatalf("save: %v %v", bad, err)
	}
	got, _ := e.LoadSettings()
	if got.Schedule.IntervalMin != "30m" || got.Schedule.PeriodEnd != "2027-12-01" || got.Network.Proxy != s.Network.Proxy || !slices.Equal(got.Notify.Events, []string{"stale"}) {
		t.Fatalf("round trip: %+v", got)
	}
	cfg, _, _ := e.LoadConfig()
	if len(cfg.Accounts) != 1 {
		t.Fatal("saving settings lost the accounts")
	}
}

func TestAccountLifecycle(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()

	// A bad account is rejected before anything is written.
	if err := e.AddAccount(ctx, AccountInput{Name: "Bad Name", Auth: "nope"}); err == nil {
		t.Fatal("expected a validation error")
	}
	if err := e.AddAccount(ctx, AccountInput{Name: "app", Auth: config.AuthClientSecret, Tenant: "t.example", ClientID: testGUID}); err == nil {
		t.Fatal("a client_secret account needs a secret")
	}
	if err := e.AddAccount(ctx, AccountInput{Name: "app", Auth: config.AuthClientSecret, Tenant: "t.example", ClientID: testGUID, Secret: "S3CRET", Categories: []string{"nope"}}); err == nil {
		t.Fatal("an unknown category must be refused")
	}
	if err := e.AddAccount(ctx, AccountInput{Name: "app", Auth: config.AuthClientSecret, Tenant: "t.example", ClientID: testGUID, Secret: "S3CRET"}); err != nil {
		t.Fatal(err)
	}
	e.addDevice(t, "dev")
	if err := e.AddAccount(ctx, AccountInput{Name: "dev", Auth: config.AuthDeviceCode, Tenant: "t.example", ClientID: testGUID}); err == nil {
		t.Fatal("duplicate must fail")
	}

	list, err := e.ListAccounts()
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	if !list[0].Credentials || list[0].Status != "secret stored" || list[1].Credentials || list[1].Status != "not logged in" {
		t.Fatalf("status: %+v", list)
	}

	// The secret is in the vault and never in the config or in what Get returns.
	if raw := string(mustRead(t, filepath.Join(e.cfgDir, "config.toml"))); strings.Contains(raw, "S3CRET") {
		t.Fatal("secret in the config file")
	}
	in, err := e.GetAccount("app")
	if err != nil || in.Secret != "" || in.Tenant != "t.example" {
		t.Fatalf("get: %+v %v", in, err)
	}

	// Login stores the home account id and returns no token.
	res, err := e.Login(ctx, "dev", nopUI{})
	if err != nil || !res.Delegated || res.Account != "me@contoso.com" {
		t.Fatalf("login: %+v %v", res, err)
	}
	cfg, _, _ := e.LoadConfig()
	if a, _ := cfg.Find("dev"); a.HomeAccountID != "uid.tid" {
		t.Fatalf("home account id = %q", a.HomeAccountID)
	}
	ti, err := e.TestAccount(ctx, "dev")
	if err != nil || !slices.Contains(ti.Scopes, "Mail.Read") {
		t.Fatalf("test: %+v %v", ti, err)
	}
	e.fa.err = auth.ErrLoginRequired
	if _, err := e.TestAccount(ctx, "dev"); err == nil || !strings.Contains(err.Error(), "account login dev") {
		t.Fatalf("hint missing: %v", err)
	}
	e.fa.err = nil

	// Update: a changed tenant drops the saved sign-in.
	in, _ = e.GetAccount("dev")
	in.Tenant = "other.example"
	in.Categories = []string{"mail"}
	if err := e.UpdateAccount(ctx, in); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ = e.LoadConfig()
	a, _ := cfg.Find("dev")
	if a.Tenant != "other.example" || a.HomeAccountID != "" || len(a.Categories) != 1 {
		t.Fatalf("after update: %+v", a)
	}
	if err := e.UpdateAccount(ctx, AccountInput{Name: "ghost", Auth: config.AuthDeviceCode, Tenant: "t", ClientID: testGUID}); err == nil {
		t.Fatal("updating an unknown account must fail")
	}

	// SetSecret replaces the secret and may set the expiry.
	if err := e.SetSecret(ctx, "app", "N3W", "2030-01-02"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetSecret(ctx, "dev", "x", ""); err == nil {
		t.Fatal("device_code accounts have no secret")
	}
	if err := e.SetSecret(ctx, "app", "", ""); err == nil {
		t.Fatal("empty secret must fail")
	}
	if err := e.SetSecret(ctx, "app", "x", "yesterday"); err == nil {
		t.Fatal("bad expiry must fail")
	}
	v, _ := e.OpenVault(cfg)
	if b, err := v.Get(vault.SecretKey("app")); err != nil || string(b) != "N3W" {
		t.Fatalf("stored secret: %q %v", b, err)
	}

	// Pause and resume.
	if _, err := e.Pause(ctx, "app"); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ = e.LoadConfig()
	if a, _ := cfg.Find("app"); !a.Paused || a.SecretExpires != "2030-01-02" {
		t.Fatalf("paused: %+v", a)
	}
	if _, err := e.Resume(ctx, "app"); err != nil {
		t.Fatal(err)
	}

	// Remove deletes the secret as well.
	warns, err := e.RemoveAccount(ctx, "app")
	if err != nil || len(warns) != 0 {
		t.Fatalf("remove: %v %v", warns, err)
	}
	if ok, _ := v.Has(vault.SecretKey("app")); ok {
		t.Fatal("secret survived the removal")
	}
	if _, err := e.RemoveAccount(ctx, "app"); err == nil {
		t.Fatal("removing twice must fail")
	}
}

type nopUI struct{}

func (nopUI) DeviceCode(string)        {}
func (nopUI) OpenBrowser(string) error { return nil }

func TestSnapshotStatuses(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	snap, err := e.Snapshot()
	if err != nil || len(snap.Accounts) != 0 || snap.Engine.Running || snap.Engine.Host != HostNone {
		t.Fatalf("empty snapshot: %+v %v", snap, err)
	}

	for _, n := range []string{"ok", "attn", "paused", "fresh"} {
		e.addDevice(t, n)
	}
	if err := e.AddAccount(ctx, AccountInput{Name: "expired", Auth: config.AuthClientSecret, Tenant: "t.example", ClientID: testGUID, Secret: "s", SecretExpires: "2020-01-01"}); err != nil {
		t.Fatal(err)
	}
	// Everything except "fresh" counts as signed in.
	for _, n := range []string{"ok", "attn", "paused"} {
		if _, err := e.Login(ctx, n, nopUI{}); err != nil {
			t.Fatal(err)
		}
		// A real sign-in leaves a token cache behind; the fake one does not.
		cfg, _, _ := e.LoadConfig()
		v, _ := e.OpenVault(cfg)
		if err := v.Put(vault.TokenCacheKey(n), []byte("cache")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Pause(ctx, "paused"); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	st := store.New(e.dataDir)
	state := store.NewState()
	state.Accounts["ok"] = &store.AccountState{Status: store.StatusOK, LastSuccess: now.Add(-time.Hour), LastRun: now.Add(-time.Hour),
		NextRun: now.Add(time.Hour), GrantedScopes: []string{"User.Read", "Mail.Read"}}
	state.Accounts["attn"] = &store.AccountState{Status: store.StatusNeedsAttention, Reason: "login required; run: e5extend account login attn"}
	if err := st.SaveState(state); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(store.Entry{TS: now, Account: "ok", Round: "r1", API: "mail.list", Status: 200, Class: "ok", LatencyMS: 12}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(store.Entry{TS: now, Account: "ok", Round: "r2", API: "mail.list", Status: 500, Class: "server", LatencyMS: 12}); err != nil {
		t.Fatal(err)
	}

	snap, err = e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]AccountSummary{}
	for _, a := range snap.Accounts {
		by[a.Name] = a
	}
	want := map[string]string{"ok": StatusOK, "attn": StatusNeedsAttention, "paused": StatusPaused, "fresh": StatusNotSignedIn, "expired": StatusNeedsAttention}
	for n, w := range want {
		if by[n].Status != w {
			t.Errorf("%s: status %q, want %q (reason %q)", n, by[n].Status, w, by[n].Reason)
		}
	}
	if by["attn"].Reason != "login required" || !strings.Contains(by["attn"].Fix, "account login attn") {
		t.Errorf("reason/fix split: %q / %q", by["attn"].Reason, by["attn"].Fix)
	}
	ok := by["ok"]
	if ok.TodayOK != 1 || ok.TodayFailed != 1 || ok.NextRun == nil || ok.LastSuccess == nil || ok.UsableAPIs == 0 || ok.TotalAPIs <= ok.UsableAPIs {
		t.Errorf("ok summary: %+v", ok)
	}
	if by["fresh"].LastRun != nil || by["fresh"].NextRun != nil {
		t.Errorf("fresh must have no times: %+v", by["fresh"])
	}
	if !by["paused"].Paused || by["paused"].NextRun != nil {
		t.Errorf("paused: %+v", by["paused"])
	}
}

func TestEngineHostMark(t *testing.T) {
	e := newTestEnv(t)
	if got := EngineHost(e.dataDir); got != HostService {
		t.Fatalf("no mark: %q", got)
	}
	remove, err := MarkEngineHost(e.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := EngineHost(e.dataDir); got != HostGUI {
		t.Fatalf("with mark: %q", got)
	}
	remove()
	if got := EngineHost(e.dataDir); got != HostService {
		t.Fatalf("after remove: %q", got)
	}
	// A mark of a process that is gone is ignored.
	if err := os.WriteFile(filepath.Join(e.dataDir, hostFile), []byte("gui 2147483000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := EngineHost(e.dataDir); got != HostService {
		t.Fatalf("stale mark: %q", got)
	}
}

func TestSnapshotReportsRunningEngine(t *testing.T) {
	e := newTestEnv(t)
	release, err := engine.AcquireLock(e.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := engine.WriteHeartbeat(e.dataDir, time.Now().Add(-5*time.Second)); err != nil {
		t.Fatal(err)
	}
	snap, err := e.Snapshot()
	if err != nil || !snap.Engine.Running || snap.Engine.Host != HostService || snap.Engine.HeartbeatAgeSec == nil || *snap.Engine.HeartbeatAgeSec < 4 {
		t.Fatalf("snapshot: %+v %v", snap.Engine, err)
	}
}

func TestRunNowQueuesWhenEngineRuns(t *testing.T) {
	e := newTestEnv(t)
	if _, err := e.RunNow(context.Background(), nil, RunNowOptions{}); err == nil {
		t.Fatal("no config must fail")
	}
	e.addDevice(t, "main")
	if _, err := e.RunNow(context.Background(), []string{"ghost"}, RunNowOptions{}); err == nil {
		t.Fatal("unknown account must fail")
	}
	release, err := engine.AcquireLock(e.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	res, err := e.RunNow(context.Background(), nil, RunNowOptions{OnLocalRun: func() { t.Error("must not run locally") }})
	if err != nil || !res.Queued || !slices.Equal(res.Targets, []string{"main"}) {
		t.Fatalf("queued: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, "trigger", "main")); err != nil {
		t.Fatalf("trigger file: %v", err)
	}
}

func TestRunNowRunsLocallyWithoutEngine(t *testing.T) {
	e := newTestEnv(t)
	e.addDevice(t, "main")
	e.fa.err = auth.ErrLoginRequired
	local := false
	var entries []store.Entry
	_, err := e.RunNow(context.Background(), []string{"main"}, RunNowOptions{
		OnLocalRun: func() { local = true },
		OnEntry:    func(en store.Entry) { entries = append(entries, en) },
	})
	if !local || len(entries) == 0 {
		t.Fatalf("local=%v entries=%d", local, len(entries))
	}
	if err == nil || !strings.Contains(err.Error(), "needs attention") {
		t.Fatalf("a failed round must be an error: %v", err)
	}
}

func TestWebhookLifecycleNeverReturnsTheAddress(t *testing.T) {
	e := newTestEnv(t)
	const addr = "https://hooks.example.invalid/services/T000/B000/TOPSECRET"
	if e.WebhookConfigured() {
		t.Fatal("nothing is set yet")
	}
	if _, err := e.SetWebhook(context.Background(), "not a url"); err == nil || strings.Contains(err.Error(), "not a url") {
		t.Fatalf("bad address: %v", err)
	}
	if _, err := e.SetWebhook(context.Background(), addr); err != nil {
		t.Fatal(err)
	}
	if !e.WebhookConfigured() {
		t.Fatal("not configured after set")
	}
	s, _ := e.LoadSettings()
	if !s.Notify.WebhookConfigured {
		t.Fatal("settings do not show it")
	}
	// The address is encrypted in the vault, never in the config file.
	if entries, _ := os.ReadDir(e.cfgDir); len(entries) > 0 {
		for _, en := range entries {
			if strings.Contains(string(mustRead(t, filepath.Join(e.cfgDir, en.Name()))), "TOPSECRET") {
				t.Fatalf("address found in %s", en.Name())
			}
		}
	}
	removed, err := e.ClearWebhook(context.Background())
	if err != nil || !removed || e.WebhookConfigured() {
		t.Fatalf("clear: %v %v", removed, err)
	}
	if err := e.TestWebhook(context.Background()); err == nil || !strings.Contains(err.Error(), "no webhook") {
		t.Fatalf("test without webhook: %v", err)
	}
}

func TestCatalogAndCategories(t *testing.T) {
	e := newTestEnv(t)
	all, err := e.Catalog("")
	if err != nil || len(all.Entries) == 0 {
		t.Fatal(err)
	}
	cats, err := e.Categories()
	if err != nil || len(cats) == 0 {
		t.Fatal(err)
	}
	e.addDevice(t, "main")
	res, err := e.Catalog("main")
	if err != nil || !res.NoGrants || res.Account == nil {
		t.Fatalf("%+v %v", res, err)
	}
	if res.Entries[0].Availability == "" {
		t.Fatal("availability missing")
	}
	if _, err := e.Catalog("ghost"); err == nil {
		t.Fatal("unknown account must fail")
	}
}

func TestToValidationError(t *testing.T) {
	for in, want := range map[string]ValidationError{
		"schedule.interval_min: bad":        {"schedule.intervalMin", "bad"},
		"schedule.work_days[2]: bad day":    {"schedule.workDays", "bad day"},
		"accounts[0].client_id: not a GUID": {"accounts[0].clientId", "not a GUID"},
		"no colon here":                     {"", "no colon here"},
	} {
		if got := toValidationError(errors.New(in)); got != want {
			t.Errorf("%q: %+v, want %+v", in, got, want)
		}
	}
}

func TestHintSuggestsNextStep(t *testing.T) {
	if h := Hint(auth.ErrLoginRequired, "x"); !strings.Contains(h, "account login x") {
		t.Fatal(h)
	}
	if h := Hint(errors.New("plain"), "x"); h != "" {
		t.Fatal(h)
	}
	// The exact error a real tenant with a sign-in frequency policy returned.
	ca := fmt.Errorf("%w: AADSTS70043: The refresh token has expired or is invalid due to sign-in frequency checks by conditional access.", auth.ErrLoginRequired)
	if h := Hint(ca, "x"); !strings.Contains(h, "Conditional Access") || !strings.Contains(h, "account login x") ||
		!strings.Contains(h, "account add x-app --auto --auth client_secret") || !strings.Contains(h, "(a)") || !strings.Contains(h, "(b)") || !strings.Contains(h, "(c)") {
		t.Fatalf("sign-in frequency hint: %q", h)
	}
}

// Every suggestion has a code the doctor knows, and every code is reachable.
func TestHintCodes(t *testing.T) {
	errs := []error{
		errors.New("AADSTS7000218"), errors.New("AADSTS70043"), errors.New("AADSTS50126"), errors.New("AADSTS53003"),
		errors.New("AADSTS50053"), errors.New("AADSTS50055"), errors.New("AADSTS50057"), errors.New("AADSTS50076"),
		fmt.Errorf("%w: %w", auth.ErrLoginRequired, vault.ErrWrongKey), auth.ErrLoginRequired, auth.ErrInvalidClient,
		auth.ErrConsentRequired, auth.ErrTransient, vault.ErrRemoteSession, vault.ErrNoMasterKey, vault.ErrWrongKey,
	}
	seen := map[string]bool{}
	for _, err := range errs {
		code := HintCode(err)
		if code == "" || !slices.Contains(doctor.HintCodes, code) {
			t.Errorf("%v: code %q", err, code)
		}
		if Hint(err, "x") == "" {
			t.Errorf("%v: no text", err)
		}
		seen[code] = true
	}
	for _, c := range doctor.HintCodes {
		if !seen[c] {
			t.Errorf("hint code %s is never returned", c)
		}
	}
	if HintCode(errors.New("plain")) != "" {
		t.Error("an unknown error has no code")
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

func TestServicePlanDoesNotTouchTheSystem(t *testing.T) {
	e := newTestEnv(t)
	fr := &service.FakeRunner{}
	e.Runner = fr
	e.Exe = func() (string, error) {
		return filepath.Join(string(filepath.Separator), "opt", "e5extend", "e5extend"), nil
	}
	e.Svc = SvcIdentity{Home: t.TempDir(), UID: "501", User: "tester"}
	for _, platform := range []string{"", "linux", "darwin", "windows"} {
		plan, _, err := e.ServicePlan(platform, false, false)
		if err != nil || plan.Format() == "" {
			t.Errorf("%q: %v", platform, err)
		}
	}
	if _, _, err := e.ServicePlan("plan9", false, false); err == nil {
		t.Error("an unknown platform must be refused")
	}
	if len(fr.Calls) != 0 {
		t.Errorf("planning ran commands: %v", fr.Calls)
	}
}

func TestServiceHeadlessIsWindowsOnly(t *testing.T) {
	e := newTestEnv(t)
	fr := &service.FakeRunner{}
	e.Runner = fr
	e.Exe = func() (string, error) {
		return filepath.Join(string(filepath.Separator), "opt", "e5extend", "e5extend"), nil
	}
	e.Svc = SvcIdentity{Home: t.TempDir(), UID: "501", User: "tester"}
	plan, _, err := e.ServicePlanWith("windows", InstallFlags{Headless: true})
	if err != nil || !strings.Contains(plan.Format(), "S4U") {
		t.Fatalf("windows: %v\n%s", err, plan.Format())
	}
	for _, platform := range []string{"linux", "darwin"} {
		if _, _, err := e.ServicePlanWith(platform, InstallFlags{Headless: true}); err == nil {
			t.Errorf("%s accepted --headless", platform)
		}
	}
	// The real install is refused before anything runs on Linux and macOS,
	// whatever host the test runs on.
	for _, platform := range []string{service.Linux, service.Darwin} {
		e.Platform = platform
		if _, err := e.ServiceInstallWith(InstallFlags{Headless: true}); err == nil || len(fr.Calls) != 0 {
			t.Errorf("%s install: %v %v", platform, err, fr.Calls)
		}
	}
}

// On a Windows host the headless install really writes the DPAPI key file; the
// scheduler is the fake runner.
func TestServiceHeadlessInstallOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("needs DPAPI")
	}
	e := newTestEnv(t)
	fr := &service.FakeRunner{}
	e.Runner = fr
	e.Platform = service.Windows
	e.Exe = func() (string, error) { return `C:\Program Files\e5extend\e5extend.exe`, nil }
	e.Svc = SvcIdentity{Home: t.TempDir(), UID: "0", User: `PC\tester`}
	t.Setenv("E5EXTEND_MASTER_KEY", vault.EncodeKey(make([]byte, 32)))
	if _, err := e.ServiceInstallWith(InstallFlags{Headless: true}); err != nil {
		t.Fatal(err)
	}
	cfgDir, _ := paths.ConfigDir()
	if _, err := os.Stat(filepath.Join(cfgDir, vault.DPAPIKeyFileName)); err != nil {
		t.Errorf("no key file: %v", err)
	}
	if got := strings.Join(fr.Commands(), "|"); !strings.Contains(got, "schtasks /Create") {
		t.Errorf("commands = %s", got)
	}
}

func TestDoctorRunsOffline(t *testing.T) {
	e := newTestEnv(t)
	e.Runner = &service.FakeRunner{}
	e.Svc = SvcIdentity{Home: t.TempDir(), UID: "501", User: "tester"}
	checks, err := e.Doctor(context.Background(), true)
	if err != nil || len(checks) == 0 {
		t.Fatalf("%v %v", checks, err)
	}
}

func TestClearEngineHostRemovesAForeignMark(t *testing.T) {
	e := newTestEnv(t)
	// A mark that names a live process, but not one that hosts the engine now
	// (the process ID may have been reused): the tray clears it at start.
	if err := os.WriteFile(filepath.Join(e.dataDir, hostFile), []byte("gui "+strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if EngineHost(e.dataDir) != HostGUI {
		t.Fatal("setup: the mark should look live")
	}
	ClearEngineHost(e.dataDir)
	if EngineHost(e.dataDir) != HostService {
		t.Fatal("the mark survived")
	}
}

func TestEngineStoppedHint(t *testing.T) {
	e := newTestEnv(t)
	fr := &service.FakeRunner{Replies: map[string]service.Output{
		"systemctl --user is-enabled": {Stdout: "enabled\n"},
		"systemctl --user is-active":  {Stdout: "inactive\n", ExitCode: 3},
	}}
	e.Runner = fr
	e.Platform = service.Linux // the fake runner and unit file below are systemd's
	e.Svc = SvcIdentity{Home: t.TempDir(), UID: "501", User: "tester"}
	if h := e.EngineStoppedHint(); !strings.Contains(h, "e5extend service install") || !strings.Contains(h, "e5extend run") {
		t.Fatalf("not installed: %q", h)
	}
	unit := filepath.Join(e.Svc.Home, ".config", "systemd", "user", service.UnitName)
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if h := e.EngineStoppedHint(); !strings.Contains(h, "installed but its engine is not running") {
		t.Fatalf("installed: %q", h)
	}
}
