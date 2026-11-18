package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

type hookRecv struct {
	mu   sync.Mutex
	got  []map[string]string
	path []string
	code int
}

func (h *hookRecv) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]string
	_ = json.Unmarshal(b, &m)
	h.mu.Lock()
	h.got = append(h.got, m)
	h.path = append(h.path, r.URL.Path)
	code := h.code
	h.mu.Unlock()
	if code != 0 {
		w.WriteHeader(code)
	}
}

func TestNotifySetTestClear(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	rc := &hookRecv{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	hookURL := srv.URL + "/hook/SECRETTOKEN123"

	if _, _, err := e.run("", "notify", "test"); err == nil || !strings.Contains(err.Error(), "no notification channel") {
		t.Fatalf("test without webhook: %v", err)
	}
	if _, _, err := e.run("", "notify", "set-webhook", "not a url"); err == nil || strings.Contains(err.Error(), "not a url") {
		t.Fatalf("bad address: %v", err)
	}

	// Legacy plaintext entry in the config is removed by set-webhook.
	e.mustRun("", "config", "init")
	cfgPath := filepath.Join(e.cfgDir, "config.toml")
	cfg := e.loadCfg()
	cfg.Notify.WebhookURL = hookURL
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}
	// The legacy address works for notify test before migration.
	out := e.mustRun("", "notify", "test")
	if !strings.Contains(out, "delivered") || len(rc.got) != 1 {
		t.Fatalf("legacy delivery: %q hits=%d", out, len(rc.got))
	}

	out = e.mustRun("", "notify", "set-webhook", hookURL)
	if strings.Contains(out, "SECRETTOKEN123") {
		t.Errorf("set-webhook printed the address: %s", out)
	}
	if !strings.Contains(out, "removed notify.webhook_url") || e.loadCfg().Notify.WebhookURL != "" {
		t.Errorf("config not cleaned: %s", out)
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "SECRETTOKEN123") {
		t.Error("address still in config.toml")
	}
	// The vault file does not hold the address in plain text.
	_ = filepath.Walk(e.dataDir, func(p string, fi os.FileInfo, _ error) error {
		if fi != nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "SECRETTOKEN123") {
				t.Errorf("address readable in %s", p)
			}
		}
		return nil
	})

	out = e.mustRun("", "notify", "test")
	if !strings.Contains(out, "delivered") || strings.Contains(out, "SECRETTOKEN123") {
		t.Errorf("test output: %s", out)
	}
	if len(rc.got) != 2 || rc.path[1] != "/hook/SECRETTOKEN123" || rc.got[1]["event"] != "test" || !strings.HasPrefix(rc.got[1]["text"], "[e5extend]") {
		t.Errorf("received: %+v", rc.got)
	}

	// A failing receiver: the error is clear and does not contain the address.
	rc.mu.Lock()
	rc.code = 500
	rc.mu.Unlock()
	_, errOut, err := e.run("", "notify", "test")
	if err == nil || strings.Contains(err.Error()+errOut, "SECRETTOKEN123") || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("failure: %v", err)
	}

	out = e.mustRun("", "notify", "clear")
	if !strings.Contains(out, "alerts are off") {
		t.Errorf("clear: %s", out)
	}
	if out = e.mustRun("", "notify", "clear"); !strings.Contains(out, "no webhook was set") {
		t.Errorf("second clear: %s", out)
	}
}

func TestSetWebhookReadsFromStdin(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	rc := &hookRecv{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e.mustRun(srv.URL+"/x\n", "notify", "set-webhook")
	e.mustRun("", "notify", "test")
	if len(rc.got) != 1 {
		t.Errorf("hits = %d", len(rc.got))
	}
	v, _ := vault.Open("auto")
	if ok, _ := v.Has(notify.ChannelSecretKey("webhook", "url")); !ok {
		t.Error("not stored in the vault")
	}
}

func TestNotifyChannelCommands(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	rc := &hookRecv{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e.mustRun("", "config", "init")

	if out := e.mustRun("", "notify", "channel", "list"); !strings.Contains(out, "no channels") {
		t.Errorf("empty list: %s", out)
	}

	// Secrets come from stdin, one line each; feishu has an optional sign secret.
	out := e.mustRun(srv.URL+"/feishu/TOPTOKEN\nSIGNSECRET\n", "notify", "channel", "add", "fs", "--type", "feishu", "--min-level", "warning", "--language", "zh")
	if strings.Contains(out, "TOPTOKEN") || strings.Contains(out, "SIGNSECRET") {
		t.Errorf("add printed a secret: %s", out)
	}
	e.mustRun(srv.URL+"/slack/OTHERTOKEN\n", "notify", "channel", "add", "sl", "--type", "slack", "--events", "failure_spike,recovered")
	cfg := e.loadCfg()
	if len(cfg.Notify.Channels) != 2 || cfg.Notify.Channels[0].MinLevel != "warning" || len(cfg.Notify.Channels[1].Events) != 2 {
		t.Fatalf("config: %+v", cfg.Notify.Channels)
	}
	raw, _ := os.ReadFile(filepath.Join(e.cfgDir, "config.toml"))
	for _, s := range []string{"TOPTOKEN", "SIGNSECRET", "OTHERTOKEN"} {
		if strings.Contains(string(raw), s) {
			t.Errorf("%s is in config.toml", s)
		}
	}

	list := e.mustRun("", "notify", "channel", "list")
	for _, want := range []string{"fs", "feishu", "warning", "url: set", "secret: set", "sl", "failure_spike,recovered", "-"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}
	for _, s := range []string{"TOPTOKEN", "SIGNSECRET", "127.0.0.1"} {
		if strings.Contains(list, s) {
			t.Errorf("list shows %q", s)
		}
	}

	// Bad input names the field and saves nothing.
	if _, _, err := e.run("", "notify", "channel", "add", "x", "--type", "telegram"); err == nil {
		t.Error("telegram without a chat id accepted")
	}
	_, _, err := e.run("", "notify", "channel", "add", "x", "--type", "email", "--preset", "nope")
	if err == nil || !strings.Contains(err.Error(), "preset") {
		t.Errorf("unknown preset: %v", err)
	}
	if _, _, err := e.run(srv.URL+"/y\n", "notify", "channel", "add", "sl", "--type", "slack"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate: %v", err)
	}
	if _, _, err := e.run("", "notify", "channel", "add", "x"); err == nil || !strings.Contains(err.Error(), "--type") {
		t.Errorf("missing type: %v", err)
	}

	// Test: all channels, one channel, and a failing one.
	out = e.mustRun("", "notify", "test")
	if strings.Count(out, "test alert delivered") != 2 || len(rc.got) != 2 {
		t.Errorf("test all: %q hits=%d", out, len(rc.got))
	}
	// The channels are tried at the same time, so the order is not fixed.
	if !((rc.path[0] == "/feishu/TOPTOKEN" && rc.path[1] == "/slack/OTHERTOKEN") || (rc.path[1] == "/feishu/TOPTOKEN" && rc.path[0] == "/slack/OTHERTOKEN")) {
		t.Errorf("requests: %v", rc.path)
	}
	e.mustRun("", "notify", "test", "sl")
	if len(rc.got) != 3 || rc.path[2] != "/slack/OTHERTOKEN" {
		t.Errorf("test one: %v", rc.path)
	}
	if _, _, err := e.run("", "notify", "test", "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("unknown channel: %v", err)
	}
	rc.mu.Lock()
	rc.code = 500
	rc.mu.Unlock()
	_, _, err = e.run("", "notify", "test")
	if err == nil || !strings.Contains(err.Error(), "2 of 2") || strings.Contains(err.Error(), "TOPTOKEN") || strings.Contains(err.Error(), "OTHERTOKEN") {
		t.Errorf("failing test: %v", err)
	}
	if list = e.mustRun("", "notify", "channel", "list"); !strings.Contains(list, "failed") || strings.Contains(list, "OTHERTOKEN") {
		t.Errorf("list after failure:\n%s", list)
	}
	rc.mu.Lock()
	rc.code = 0
	rc.mu.Unlock()

	// Edit: change a setting, keep the secrets; re-enter one with --secret.
	e.mustRun("", "notify", "channel", "edit", "sl", "--min-level", "critical", "--events", "")
	cfg = e.loadCfg()
	if c, _ := cfg.Notify.FindChannel("sl"); c.MinLevel != "critical" || len(c.Events) != 0 {
		t.Errorf("edit: %+v", c)
	}
	e.mustRun("", "notify", "test", "sl")
	if rc.path[len(rc.path)-1] != "/slack/OTHERTOKEN" {
		t.Errorf("the secret changed by an edit: %v", rc.path)
	}
	e.mustRun(srv.URL+"/slack/NEWTOKEN\n", "notify", "channel", "edit", "sl", "--secret", "url")
	e.mustRun("", "notify", "test", "sl")
	if rc.path[len(rc.path)-1] != "/slack/NEWTOKEN" {
		t.Errorf("--secret url: %v", rc.path)
	}
	if _, _, err := e.run("", "notify", "channel", "edit", "sl", "--secret", "bot_token"); err == nil {
		t.Error("a secret the type does not have was accepted")
	}
	e.mustRun("", "notify", "channel", "edit", "fs", "--clear-secret", "secret")
	if list = e.mustRun("", "notify", "channel", "list"); strings.Contains(list, "secret: set") {
		t.Errorf("sign secret not cleared:\n%s", list)
	}

	// Remove.
	out = e.mustRun("", "notify", "channel", "remove", "fs")
	if !strings.Contains(out, "removed") || len(e.loadCfg().Notify.Channels) != 1 {
		t.Errorf("remove: %s", out)
	}
	if _, _, err := e.run("", "notify", "channel", "remove", "fs"); err == nil {
		t.Error("removed twice")
	}
}

func TestNotifyChannelTemplateFileAndPreview(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	e.mustRun("", "config", "init")

	// Preview needs no channel and sends nothing.
	out := e.mustRun("", "notify", "preview", "failure_spike", "--type", "telegram", "--language", "zh")
	for _, want := range []string{"--- telegram, html ---", "调用失败率过高", "<b>", "失败率 75%"} {
		if !strings.Contains(out, want) {
			t.Errorf("preview lacks %q:\n%s", want, out)
		}
	}
	out = e.mustRun("", "notify", "preview", "needs_attention", "--type", "email", "--language", "en")
	if !strings.Contains(out, "title: [E5 Developer Extend] Critical: Needs attention") {
		t.Errorf("email preview:\n%s", out)
	}
	if _, _, err := e.run("", "notify", "preview", "boom", "--type", "slack"); err == nil {
		t.Error("unknown event")
	}
	tmpl := filepath.Join(t.TempDir(), "t.tmpl")
	if err := os.WriteFile(tmpl, []byte("ALERT {{.Level}} {{.Account}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	out = e.mustRun("", "notify", "preview", "needs_attention", "--type", "slack", "--template-file", tmpl)
	if !strings.Contains(out, "ALERT critical demo") {
		t.Errorf("custom preview:\n%s", out)
	}

	// A broken template is refused when saving the channel.
	bad := filepath.Join(t.TempDir(), "bad.tmpl")
	_ = os.WriteFile(bad, []byte("{{.Nope}}"), 0o600)
	_, _, err := e.run("https://h.example/x\n", "notify", "channel", "add", "sl", "--type", "slack", "--template-file", bad)
	if err == nil || !strings.Contains(err.Error(), "template") {
		t.Errorf("broken template saved: %v", err)
	}
	// A good one is kept and used by the channel preview.
	e.mustRun("https://h.example/x\n", "notify", "channel", "add", "sl", "--type", "slack", "--template-file", tmpl, "--language", "en")
	out = e.mustRun("", "notify", "preview", "stale")
	if !strings.Contains(out, "--- sl (slack)") || !strings.Contains(out, "ALERT warning demo") {
		t.Errorf("channel preview:\n%s", out)
	}
	e.mustRun("", "notify", "channel", "edit", "sl", "--clear-template")
	if c, _ := e.loadCfg().Notify.FindChannel("sl"); c.Template != "" {
		t.Error("template not cleared")
	}
}

func TestNotifyEmailPresetFillsServer(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	e.mustRun("", "config", "init")
	e.mustRun("APP-PASSWORD-1\n", "notify", "channel", "add", "mail", "--type", "email", "--preset", "qq",
		"--username", "me@qq.com", "--from", "me@qq.com", "--to", "me@qq.com,you@qq.com")
	c, ok := e.loadCfg().Notify.FindChannel("mail")
	if !ok || c.SMTPHost != "smtp.qq.com" || c.SMTPPort != 465 || c.Security != "tls" || len(c.To) != 2 {
		t.Fatalf("channel: %+v", c)
	}
	raw, _ := os.ReadFile(filepath.Join(e.cfgDir, "config.toml"))
	if strings.Contains(string(raw), "APP-PASSWORD-1") {
		t.Error("password in config.toml")
	}
	// An explicit flag wins over the preset.
	e.mustRun("", "notify", "channel", "edit", "mail", "--smtp-port", "587", "--security", "starttls")
	if c, _ := e.loadCfg().Notify.FindChannel("mail"); c.SMTPPort != 587 || c.Security != "starttls" || c.SMTPHost != "smtp.qq.com" {
		t.Errorf("after edit: %+v", c)
	}
}

func TestSetWebhookIsAnAliasOfTheWebhookChannel(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	rc := &hookRecv{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	e.mustRun(srv.URL+"/a\n", "notify", "channel", "add", "webhook", "--type", "webhook")
	e.mustRun("", "notify", "test", "webhook")
	e.mustRun("", "notify", "set-webhook", srv.URL+"/b")
	e.mustRun("", "notify", "test")
	if len(rc.path) != 2 || rc.path[0] != "/a" || rc.path[1] != "/b" {
		t.Errorf("paths: %v", rc.path)
	}
	list := e.mustRun("", "notify", "channel", "list")
	if strings.Count(list, "webhook") < 2 || len(e.loadCfg().Notify.Channels) != 1 {
		t.Errorf("list:\n%s", list)
	}
}

func TestNotifyTestEventAndDigest(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()
	e.mustRun("", "config", "init")
	e.mustRun(srv.URL+"/wc/TOKEN123\n", "notify", "channel", "add", "wc", "--type", "wecom", "--language", "en")
	e.mustRun(srv.URL+"/hook\n", "notify", "channel", "add", "hook", "--type", "webhook")
	before, _ := os.ReadFile(filepath.Join(e.dataDir, "state.json"))

	out := e.mustRun("", "notify", "test", "wc", "--event", "failure_spike")
	if !strings.Contains(out, "wc: test alert delivered") {
		t.Errorf("output: %s", out)
	}
	mu.Lock()
	if len(bodies) != 1 || !strings.Contains(bodies[0], "［Test］ High failure rate") || !strings.Contains(bodies[0], "75%") {
		t.Errorf("event sample: %v", bodies)
	}
	bodies = nil
	mu.Unlock()

	// A big digest takes the real path: split into several WeCom messages, each within 4096 bytes.
	e.mustRun("", "notify", "test", "wc", "--digest", "40")
	mu.Lock()
	if len(bodies) < 2 {
		t.Fatalf("40 items went out in %d message(s)", len(bodies))
	}
	for _, b := range bodies {
		var m struct {
			Markdown struct{ Content string } `json:"markdown"`
		}
		if err := json.Unmarshal([]byte(b), &m); err != nil || len(m.Markdown.Content) > 4096 || !strings.Contains(m.Markdown.Content, "Test") {
			t.Errorf("message of %d bytes: %v %.120s", len(m.Markdown.Content), err, b)
		}
	}
	bodies = nil
	mu.Unlock()

	// The webhook JSON says it is a test, for every kind of sample.
	e.mustRun("", "notify", "test", "hook", "--event", "all_failing")
	mu.Lock()
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"test":true`) || !strings.Contains(bodies[0], `"event":"all_failing"`) {
		t.Errorf("webhook: %v", bodies)
	}
	mu.Unlock()

	after, _ := os.ReadFile(filepath.Join(e.dataDir, "state.json"))
	if string(before) != string(after) {
		t.Error("a test changed the alert state")
	}
	for _, args := range [][]string{{"--event", "boom"}, {"--digest", "-1"}, {"--event", "stale", "--digest", "3"}} {
		if _, _, err := e.run("", append([]string{"notify", "test"}, args...)...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestChannelEditCanClearAllowHTTP(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	cfg := config.Default()
	cfg.Notify.WebhookURL = "http://nas.lan/hook"
	if err := cfg.Save(filepath.Join(e.cfgDir, "config.toml")); err != nil {
		t.Fatal(err)
	}
	e.mustRun("", "notify", "channel", "edit", "webhook", "--min-level", "warning")
	if c, _ := e.loadCfg().Notify.FindChannel("webhook"); !c.AllowHTTP {
		t.Fatal("migration did not keep the exception")
	}
	if _, _, err := e.run("", "notify", "channel", "edit", "webhook", "--allow-http=true"); err == nil {
		t.Error("the exception can be turned on")
	}
	e.mustRun("", "notify", "channel", "edit", "webhook", "--allow-http=false")
	if c, _ := e.loadCfg().Notify.FindChannel("webhook"); c.AllowHTTP {
		t.Error("not cleared")
	}
}
