package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

const testGUID = "11111111-2222-3333-4444-555555555555"

// Values that must never come back from a service or ride on an event.
const (
	secretValue  = "S3CRET-CLIENT-VALUE-123"
	tokenValue   = "TOKEN-SECRET-ABC"
	webhookToken = "WEBHOOKTOKEN987"
	webhookURL   = "https://hooks.example.invalid/services/" + webhookToken
	upn          = "someone.private@contoso.example"
)

type fakeAuth struct {
	mu     sync.Mutex
	tok    auth.Token
	err    error
	block  chan struct{} // when set, Login waits for it or for the context
	prompt string
}

func (f *fakeAuth) Token(context.Context) (auth.Token, error) { return f.tok, f.err }
func (f *fakeAuth) Login(ctx context.Context, ui auth.LoginUI) (auth.Token, error) {
	if f.prompt != "" {
		ui.DeviceCode(f.prompt)
	}
	_ = ui.OpenBrowser("https://login.example.invalid/authorize?state=STATE-VALUE")
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return auth.Token{}, ctx.Err()
		}
	}
	return f.tok, f.err
}
func (f *fakeAuth) Logout(context.Context) error { return nil }

type fakeAutostart struct{ on bool }

func (a *fakeAutostart) Enabled() (bool, error) { return a.on, nil }
func (a *fakeAutostart) Set(on bool) error      { a.on = on; return nil }

type events struct {
	mu   sync.Mutex
	list []struct {
		Name string
		Data any
	}
}

func (e *events) emit(name string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, struct {
		Name string
		Data any
	}{name, data})
}

func (e *events) named(name string) []LoginEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []LoginEvent
	for _, ev := range e.list {
		if le, ok := ev.Data.(LoginEvent); ok && ev.Name == name {
			out = append(out, le)
		}
	}
	return out
}

func (e *events) json() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, _ := json.Marshal(e.list)
	return string(b)
}

type rig struct {
	t       *testing.T
	b       *Backend
	fa      *fakeAuth
	ev      *events
	auto    *fakeAutostart
	cfgDir  string
	dataDir string
	opened  []string
	// returned collects the JSON of everything a service handed out.
	returned []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	keyring.MockInit()
	r := &rig{t: t, ev: &events{}, auto: &fakeAutostart{}, cfgDir: t.TempDir(), dataDir: t.TempDir()}
	r.fa = &fakeAuth{tok: auth.Token{
		AccessToken: tokenValue, HomeAccountID: "uid.tid", Account: upn,
		ExpiresOn: time.Now().Add(time.Hour), Scopes: []string{"User.Read", "Mail.Read"},
	}, prompt: "To sign in, use a web browser to open the page https://microsoft.com/devicelogin and enter the code ABCD1234EF to authenticate."}
	t.Setenv("E5EXTEND_CONFIG_DIR", r.cfgDir)
	t.Setenv("E5EXTEND_DATA_DIR", r.dataDir)
	t.Setenv(vault.EnvMasterKey, "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	o := &ops.Ops{
		NewAuth:   func(config.Account, auth.Store, *http.Client) (auth.Authenticator, error) { return r.fa, nil },
		NoCallGap: true, WebhookBackoff: []time.Duration{time.Millisecond},
		Runner: &service.FakeRunner{}, Exe: func() (string, error) { return "/opt/e5extend/e5extend", nil },
		Svc: ops.SvcIdentity{Home: t.TempDir(), UID: "501", User: "tester"},
	}
	r.b = NewBackend(&Backend{Ops: o, ConfigDir: r.cfgDir, Emit: r.ev.emit, Autostart: r.auto,
		OpenURL: func(u string) error { r.opened = append(r.opened, u); return nil }})
	t.Cleanup(r.b.Close)
	return r
}

// keep records the JSON form of a value a service returned.
func (r *rig) keep(v any) {
	r.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		r.t.Fatal(err)
	}
	r.returned = append(r.returned, string(b))
}

func (r *rig) waitLogin(name, kind string) []LoginEvent {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs := r.ev.named(LoginEventName(name))
		if n := len(evs); n > 0 && evs[n-1].Kind == kind {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("no %q event for %s; got %+v", kind, name, r.ev.named(LoginEventName(name)))
	return nil
}

func TestNothingSecretLeavesTheServices(t *testing.T) {
	r := newRig(t)
	acc, status := NewAccountService(r.b), NewStatusService(r.b)
	set, logs := NewSettingsService(r.b), NewLogService(r.b)
	health, cat := NewHealthService(r.b), NewCatalogService(r.b)

	if err := acc.Add(AccountInput{Name: "app", Auth: config.AuthClientSecret, Tenant: "t.example", ClientID: testGUID,
		Secret: secretValue, SecretExpires: "2031-01-02"}); err != nil {
		t.Fatal(err)
	}
	if err := acc.Add(AccountInput{Name: "me", Auth: config.AuthDeviceCode, Tenant: "t.example", ClientID: testGUID,
		Username: "configured.user@contoso.example"}); err != nil {
		t.Fatal(err)
	}
	if err := acc.SetSecret("app", secretValue+"-NEW"); err != nil {
		t.Fatal(err)
	}
	if err := set.SetWebhook(webhookURL); err != nil {
		t.Fatal(err)
	}
	if err := set.SetWebhook("nonsense " + webhookToken); err == nil || strings.Contains(err.Error(), webhookToken) {
		t.Fatalf("a rejected address must not be echoed: %v", err)
	}

	// Sign in: the events carry the device code and nothing else of value.
	if err := acc.Login("me"); err != nil {
		t.Fatal(err)
	}
	evs := r.waitLogin("me", "done")
	var sawCode, sawBrowser bool
	for _, ev := range evs {
		if ev.Kind == "device_code" {
			sawCode = ev.UserCode == "ABCD1234EF" && ev.VerificationURI == "https://microsoft.com/devicelogin"
		}
		sawBrowser = sawBrowser || ev.Kind == "browser_opened"
	}
	if !sawCode || !sawBrowser {
		t.Fatalf("login events: %+v", evs)
	}
	if len(r.opened) != 1 {
		t.Fatalf("the browser should be opened once: %v", r.opened)
	}

	test, err := acc.Test("me")
	if err != nil {
		t.Fatal(err)
	}
	r.keep(test)
	if test.UsableAPIs == 0 || test.TotalAPIs < test.UsableAPIs {
		t.Fatalf("test result: %+v", test)
	}
	for _, name := range []string{"app", "me"} {
		in, err := acc.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if in.Secret != "" {
			t.Fatal("Get returned a secret")
		}
		r.keep(in)
	}
	if err := acc.Pause("me"); err != nil {
		t.Fatal(err)
	}
	if err := acc.Resume("me"); err != nil {
		t.Fatal(err)
	}

	// Some history to read back.
	st := store.New(r.dataDir)
	if err := st.Append(store.Entry{TS: time.Now(), Account: "me", API: "mail.list", Status: 200, Class: "ok", LatencyMS: 9}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(store.Entry{TS: time.Now(), Account: "me", Event: "round_end", OK: 1, Message: "fine"}); err != nil {
		t.Fatal(err)
	}

	snap, err := status.Snapshot()
	if err != nil || len(snap.Accounts) != 2 {
		t.Fatalf("snapshot: %+v %v", snap, err)
	}
	r.keep(snap)
	entries, err := logs.Read("", 24, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("logs: %+v %v", entries, err)
	}
	if entries[0].Event != "round_end" || entries[1].API != "mail.list" {
		t.Fatalf("logs must be newest first: %+v", entries)
	}
	r.keep(entries)

	s, err := set.Get()
	if err != nil || !s.Notify.WebhookConfigured || s.UI.Language != "auto" || s.UI.Theme != "system" {
		t.Fatalf("settings: %+v %v", s, err)
	}
	r.keep(s)
	s.UI.Language, s.UI.Theme = "zh", "dark"
	s.Schedule.IntervalMin = "45m"
	if bad, err := set.Save(s); err != nil || len(bad) != 0 {
		t.Fatalf("save: %v %v", bad, err)
	}
	if got, _ := set.Get(); got.UI.Language != "zh" || got.Schedule.IntervalMin != "45m" {
		t.Fatalf("after save: %+v", got)
	}
	if err := set.SetStartAtLogin(true); err != nil || !r.auto.on {
		t.Fatalf("start at login not applied: %v", err)
	}
	if got, _ := set.Get(); !got.UI.StartAtLogin {
		t.Fatalf("after set: %+v", got)
	}
	// Save must not undo it, even with a stale value in the form.
	s.UI.StartAtLogin = false
	if bad, err := set.Save(s); err != nil || len(bad) != 0 || !r.auto.on {
		t.Fatalf("save touched start at login: %v %v", bad, err)
	}
	s.UI.Language = "klingon"
	s.Schedule.IntervalMax = "soon"
	bad, err := set.Save(s)
	if err != nil || len(bad) != 1 || bad[0].Field != "ui.language" {
		t.Fatalf("expected the ui error alone: %v %v", bad, err)
	}
	r.keep(bad)
	s.UI.Language = "en"
	bad, err = set.Save(s)
	if err != nil || len(bad) != 1 || bad[0].Field != "schedule.intervalMax" {
		t.Fatalf("expected the schedule error: %v %v", bad, err)
	}
	r.keep(bad)

	checks, err := health.Doctor(true)
	if err != nil || len(checks) == 0 {
		t.Fatalf("doctor: %v %v", checks, err)
	}
	for _, c := range checks {
		if len(c.Parts) == 0 || c.Parts[0].Code == "" || c.Parts[0].Params == nil {
			t.Errorf("check %s has no detail codes: %+v", c.ID, c)
		}
		if c.Fix != "" && len(c.FixParts) == 0 {
			t.Errorf("check %s has a fix without codes: %+v", c.ID, c)
		}
	}
	r.keep(checks)
	info, err := health.ServiceInfo()
	if err != nil {
		t.Fatal(err)
	}
	r.keep(info)
	res, err := health.InstallService()
	if err != nil {
		t.Fatal(err)
	}
	r.keep(res)
	res, err = health.UninstallService()
	if err != nil {
		t.Fatal(err)
	}
	r.keep(res)

	list, err := cat.List("me")
	if err != nil || len(list) == 0 {
		t.Fatalf("catalog: %v", err)
	}
	r.keep(list)
	cats, err := cat.Categories()
	if err != nil || len(cats) == 0 {
		t.Fatal(err)
	}
	r.keep(cats)

	if err := set.ClearWebhook(); err != nil {
		t.Fatal(err)
	}
	if err := set.TestWebhook(); err == nil || strings.Contains(err.Error(), webhookToken) {
		t.Fatalf("test webhook after clear: %v", err)
	}
	if err := acc.Remove("app"); err != nil {
		t.Fatal(err)
	}

	everything := strings.Join(r.returned, "\n") + "\n" + r.ev.json()
	for _, banned := range []string{secretValue, tokenValue, webhookToken, "hooks.example.invalid", upn, "STATE-VALUE", "login.example.invalid"} {
		if strings.Contains(everything, banned) {
			t.Errorf("%q leaked through a return value or an event", banned)
		}
	}
	// And nothing is written in clear on disk apart from what the user typed into the config.
	filepath.WalkDir(r.cfgDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			for _, banned := range []string{secretValue, tokenValue, webhookToken} {
				if strings.Contains(string(b), banned) {
					t.Errorf("%q found in %s", banned, p)
				}
			}
		}
		return nil
	})
}

func TestLoginCanBeCancelled(t *testing.T) {
	r := newRig(t)
	acc := NewAccountService(r.b)
	if err := acc.Add(AccountInput{Name: "me", Auth: config.AuthDeviceCode, Tenant: "t.example", ClientID: testGUID}); err != nil {
		t.Fatal(err)
	}
	r.fa.block = make(chan struct{})
	if err := acc.Login("me"); err != nil {
		t.Fatal(err)
	}
	r.waitLogin("me", "browser_opened")
	if err := acc.Login("me"); err == nil {
		t.Fatal("a second sign-in for the same account must be refused")
	}
	acc.CancelLogin("me")
	evs := r.waitLogin("me", "error")
	if !strings.Contains(evs[len(evs)-1].Message, "cancel") {
		t.Fatalf("last event: %+v", evs[len(evs)-1])
	}
	// The slot is free again.
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.fa.block = nil
		if err := acc.Login("me"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sign-in slot never freed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.waitLogin("me", "done")
	if err := acc.Login("ghost"); err == nil {
		t.Fatal("unknown account must fail")
	}
}

func TestLoginFailureEventKeepsTheHint(t *testing.T) {
	r := newRig(t)
	acc := NewAccountService(r.b)
	if err := acc.Add(AccountInput{Name: "me", Auth: config.AuthDeviceCode, Tenant: "t.example", ClientID: testGUID}); err != nil {
		t.Fatal(err)
	}
	r.fa.err = auth.ErrConsentRequired
	if err := acc.Login("me"); err != nil {
		t.Fatal(err)
	}
	evs := r.waitLogin("me", "error")
	if !strings.Contains(evs[len(evs)-1].Message, "grant consent") {
		t.Fatalf("error event: %+v", evs[len(evs)-1])
	}
}

func TestSettingsValidationLeavesNothingBehind(t *testing.T) {
	r := newRig(t)
	set := NewSettingsService(r.b)
	s, err := set.Get()
	if err != nil {
		t.Fatal(err)
	}
	s.Schedule.WorkStart = "nine"
	bad, err := set.Save(s)
	if err != nil || len(bad) != 1 || bad[0].Field != "schedule.workStart" {
		t.Fatalf("bad: %+v %v", bad, err)
	}
	if _, err := os.Stat(filepath.Join(r.cfgDir, "gui.json")); err == nil {
		t.Fatal("gui.json written despite the error")
	}
}

func TestInstallServiceAsksTheTrayToHandOver(t *testing.T) {
	r := newRig(t)
	var asked bool
	r.b.RequestHandover = func(context.Context) error { asked = true; return nil }
	if _, err := NewHealthService(r.b).InstallService(); err != nil {
		t.Fatal(err)
	}
	if !asked {
		t.Fatal("handover not requested")
	}
}

func TestRunNowQueuesForARunningEngine(t *testing.T) {
	r := newRig(t)
	acc := NewAccountService(r.b)
	if err := acc.Add(AccountInput{Name: "me", Auth: config.AuthDeviceCode, Tenant: "t.example", ClientID: testGUID}); err != nil {
		t.Fatal(err)
	}
	release, err := engine.AcquireLock(r.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := NewStatusService(r.b).RunNow([]string{"me"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.dataDir, "trigger", "me")); err != nil {
		t.Fatalf("no trigger: %v", err)
	}
}

func TestRunNowWithoutEngineReportsTheEnd(t *testing.T) {
	r := newRig(t)
	acc := NewAccountService(r.b)
	if err := acc.Add(AccountInput{Name: "me", Auth: config.AuthDeviceCode, Tenant: "t.example", ClientID: testGUID}); err != nil {
		t.Fatal(err)
	}
	r.fa.err = auth.ErrLoginRequired
	if err := NewStatusService(r.b).RunNow(nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.ev.mu.Lock()
		n := len(r.ev.list)
		var last any
		if n > 0 {
			last = r.ev.list[n-1]
		}
		r.ev.mu.Unlock()
		if n > 0 {
			if !strings.Contains(r.ev.json(), RunDoneEvent) || !strings.Contains(r.ev.json(), "needs attention") {
				t.Fatalf("event: %+v", last)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no run:done event")
}

func TestDeviceCodeParsing(t *testing.T) {
	var got LoginEvent
	u := &loginUI{emit: func(e LoginEvent) { got = e }, open: func(string) error { return nil }}
	u.DeviceCode("To sign in, use a web browser to open the page https://microsoft.com/devicelogin and enter the code H7K2LMNQP to authenticate.")
	if got.UserCode != "H7K2LMNQP" || got.VerificationURI != "https://microsoft.com/devicelogin" || got.Kind != "device_code" {
		t.Fatalf("%+v", got)
	}
	u.DeviceCode("no structured content")
	if got.UserCode != "" || got.VerificationURI != "" || got.Message == "" {
		t.Fatalf("%+v", got)
	}
}

func TestInstallChecksBeforeHandover(t *testing.T) {
	r := newRig(t)
	var handovers, cancels int
	r.b.RequestHandover = func(context.Context) error { handovers++; return nil }
	r.b.CancelHandover = func() { cancels++ }
	h := NewHealthService(r.b)

	// The CLI binary is missing: nothing is disturbed.
	r.b.Ops.Exe = func() (string, error) { return "", os.ErrNotExist }
	if _, err := h.InstallService(); err == nil {
		t.Fatal("a missing CLI must fail")
	}
	if handovers != 0 || cancels != 0 {
		t.Fatalf("handover asked (%d) or cancelled (%d) although the checks failed first", handovers, cancels)
	}

	// The checks pass but the install fails: the handover is withdrawn.
	r.b.Ops.Exe = func() (string, error) { return "/opt/e5extend/e5extend", nil }
	r.b.Ops.Platform = service.Linux // make systemctl the command that fails, on every host
	r.b.Ops.Runner = &service.FakeRunner{FailOn: []string{"systemctl"}}
	if _, err := h.InstallService(); err == nil {
		t.Fatal("expected the install to fail")
	}
	if handovers != 1 || cancels != 1 {
		t.Fatalf("handovers=%d cancels=%d, want 1 and 1", handovers, cancels)
	}
	if !r.b.Idle() {
		t.Fatal("the install must not stay marked as running")
	}
}

func TestSettingsUIErrorsSaveNothing(t *testing.T) {
	r := newRig(t)
	set := NewSettingsService(r.b)
	s, _ := set.Get()
	s.UI.Theme = "neon"
	s.Schedule.IntervalMin = "45m"
	bad, err := set.Save(s)
	if err != nil || len(bad) != 1 || bad[0].Field != "ui.theme" {
		t.Fatalf("%+v %v", bad, err)
	}
	if _, err := os.Stat(filepath.Join(r.cfgDir, "config.toml")); err == nil {
		t.Fatal("config.toml saved although ui had errors")
	}
}

type brokenAutostart struct{}

func (brokenAutostart) Enabled() (bool, error) { return false, nil }
func (brokenAutostart) Set(bool) error         { return os.ErrPermission }

func TestAutostartFailureIsAnError(t *testing.T) {
	r := newRig(t)
	r.b.Autostart = brokenAutostart{}
	set := NewSettingsService(r.b)
	if err := set.SetStartAtLogin(true); err == nil {
		t.Fatal("want the failure back")
	}
}

func TestProxyPasswordIsHiddenAndKept(t *testing.T) {
	r := newRig(t)
	set := NewSettingsService(r.b)
	s, _ := set.Get()
	s.Network.Proxy = "http://bob:hunter2pass@proxy.example:3128"
	if bad, err := set.Save(s); err != nil || len(bad) != 0 {
		t.Fatalf("%v %v", bad, err)
	}
	got, _ := set.Get()
	if strings.Contains(got.Network.Proxy, "hunter2pass") || !strings.Contains(got.Network.Proxy, "proxy.example") {
		t.Fatalf("proxy shown as %q", got.Network.Proxy)
	}
	// Saving the form again with the hidden form keeps the real value.
	got.Schedule.IntervalMin = "50m"
	if bad, err := set.Save(got); err != nil || len(bad) != 0 {
		t.Fatalf("%v %v", bad, err)
	}
	cfg, _, _ := r.b.Ops.LoadConfig()
	if cfg.Network.Proxy != "http://bob:hunter2pass@proxy.example:3128" {
		t.Fatalf("stored proxy changed: %q", cfg.Network.Proxy)
	}
	// A new value replaces it.
	got.Network.Proxy = "http://other.example:8080"
	set.Save(got)
	cfg, _, _ = r.b.Ops.LoadConfig()
	if cfg.Network.Proxy != "http://other.example:8080" {
		t.Fatalf("proxy = %q", cfg.Network.Proxy)
	}
}

func TestStartServiceNeedsAnInstalledServiceAndWithdrawsHandover(t *testing.T) {
	r := newRig(t)
	r.b.Ops.Platform = service.Linux
	fr := &service.FakeRunner{}
	r.b.Ops.Runner = fr
	var asked, cancels int
	r.b.RequestHandover = func(context.Context) error { asked++; return nil }
	r.b.CancelHandover = func() { cancels++ }
	h := NewHealthService(r.b)
	// Not installed: nothing is started and the tray is not even asked.
	if _, err := h.StartService(); !errors.Is(err, service.ErrNotInstalled) || asked != 0 || cancels != 0 {
		t.Fatalf("not installed: err=%v asked=%d cancels=%d", err, asked, cancels)
	}
	if _, err := h.InstallService(); err != nil {
		t.Fatal(err)
	}
	fr.Replies = map[string]service.Output{
		"systemctl --user is-enabled": {Stdout: "enabled\n"}, "systemctl --user is-active": {Stdout: "inactive\n"}, "loginctl": {Stdout: "yes\n"},
	}
	fr.Calls = nil
	cancels = 0
	res, err := h.StartService()
	if err != nil || len(res.Actions) != 1 || cancels != 0 || asked != 2 {
		t.Fatalf("start: %+v %v cancels=%d asked=%d", res, err, cancels, asked)
	}
	if got := strings.Join(fr.Commands(), "|"); !strings.Contains(got, "systemctl --user start e5extend.service") {
		t.Fatalf("commands = %s", got)
	}
	// A failing start after the handover withdraws it.
	fr.FailOn = []string{"systemctl --user start"}
	cancels = 0
	if _, err := h.StartService(); err == nil || cancels != 1 {
		t.Fatalf("failed start: err=%v cancels=%d", err, cancels)
	}
}

func TestRecommendProbesTheSignedInAccount(t *testing.T) {
	r := newRig(t)
	acc := NewAccountService(r.b)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			t.Errorf("probe used %s", req.Method)
		}
		if req.URL.Path == "/me/drive" {
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	r.b.Ops.GraphBase = srv.URL
	r.fa.tok.Scopes = catalog.RecommendedDelegated
	if err := acc.Add(AccountInput{Name: "me", Auth: config.AuthDeviceCode, Tenant: "t.example", ClientID: testGUID}); err != nil {
		t.Fatal(err)
	}

	res, err := acc.Recommend("me")
	if err != nil || res.NeedsLogin {
		t.Fatalf("%+v %v", res, err)
	}
	r.keep(res)
	status := map[string]string{}
	for _, c := range res.Categories {
		status[c.Category] = c.Status + "/" + c.Reason
	}
	if status["files"] != "unavailable/no_onedrive" || status["mail"] != "recommended/" {
		t.Errorf("status %v", status)
	}
	if slices.Contains(res.Recommended, "files") || !slices.Contains(res.Recommended, "mail") {
		t.Errorf("recommended %v", res.Recommended)
	}
	for _, leaked := range []string{tokenValue, upn} {
		if strings.Contains(r.returned[0], leaked) {
			t.Errorf("result leaks %q", leaked)
		}
	}

	// Not signed in: no advice, no error.
	r.fa.err = auth.ErrLoginRequired
	res, err = acc.Recommend("me")
	if err != nil || !res.NeedsLogin || len(res.Categories) != 0 || res.Recommended == nil {
		t.Fatalf("not signed in: %+v %v", res, err)
	}

	// Any other failure is an error.
	r.fa.err = auth.ErrTransient
	if _, err := acc.Recommend("me"); err == nil {
		t.Error("a transient sign-in failure must be an error")
	}
	if _, err := acc.Recommend("nobody"); err == nil {
		t.Error("unknown account must be an error")
	}
}

func TestAccountRenameMovesTheAccountAndAllowsUpdate(t *testing.T) {
	r := newRig(t)
	acc := NewAccountService(r.b)
	if err := acc.Add(AccountInput{Name: "app", Auth: config.AuthClientSecret, Tenant: "t.example", ClientID: testGUID, Secret: secretValue}); err != nil {
		t.Fatal(err)
	}
	if err := acc.Add(AccountInput{Name: "taken", Auth: config.AuthDeviceCode, Tenant: "t.example", ClientID: testGUID}); err != nil {
		t.Fatal(err)
	}
	if err := acc.Rename("app", "taken"); err == nil {
		t.Fatal("renamed onto an existing account")
	}
	if err := acc.Rename("app", "Not Valid"); err == nil {
		t.Fatal("renamed to an invalid name")
	}
	if err := acc.Rename("app", "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.Get("app"); err == nil {
		t.Error("the old name still exists")
	}
	in, err := acc.Get("work")
	if err != nil || in.Secret != "" || in.Tenant != "t.example" {
		t.Fatalf("get: %+v %v", in, err)
	}
	// The edit flow: rename first, then the usual update under the new name.
	in.ClientID = "99999999-2222-3333-4444-555555555555"
	if err := acc.Update(in); err != nil {
		t.Fatal(err)
	}
	v := vault.New(filepath.Join(r.dataDir, "vault"), nil)
	if ok, _ := v.Has("secret/work"); !ok {
		t.Error("the secret did not follow the account")
	}
	if ok, _ := v.Has("secret/app"); ok {
		t.Error("the old secret was left")
	}
}
