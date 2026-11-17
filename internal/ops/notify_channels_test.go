package ops

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
)

// recv is a receiver on the loopback interface (plain http is accepted there).
type recv struct {
	*httptest.Server
	mu   sync.Mutex
	got  []string // "path body"
	code int
}

func newRecv(t *testing.T) *recv {
	t.Helper()
	r := &recv{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, req.URL.Path+" "+string(b))
		code := r.code
		r.mu.Unlock()
		if code != 0 {
			w.WriteHeader(code)
		}
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *recv) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.got) }

func slackInput(id, url string) ChannelInput {
	return ChannelInput{ID: id, Type: config.ChannelSlack, Secrets: map[string]string{"url": url}}
}

func mustSave(t *testing.T, e *testEnv, in ChannelInput, create bool) {
	t.Helper()
	bad, err := e.SaveChannel(context.Background(), in, create)
	if err != nil || len(bad) > 0 {
		t.Fatalf("save %s: %v %v", in.ID, bad, err)
	}
}

func TestChannelLifecycleKeepsSecretsOutOfConfigAndViews(t *testing.T) {
	e := newTestEnv(t)
	rc := newRecv(t)
	hook := rc.URL + "/services/TOPSECRETTOKEN"
	in := slackInput("team", hook)
	in.MinLevel, in.Events, in.Language = config.LevelWarning, []string{"failure_spike", "needs_attention"}, "en"
	mustSave(t, e, in, true)

	// Secrets: not in config.toml, not in the view; the view only says set.
	raw, _ := os.ReadFile(filepath.Join(e.cfgDir, "config.toml"))
	if strings.Contains(string(raw), "TOPSECRETTOKEN") || !strings.Contains(string(raw), "id = 'team'") {
		t.Fatalf("config.toml:\n%s", raw)
	}
	chs, err := e.ListChannels()
	if err != nil || len(chs) != 1 {
		t.Fatalf("%v %v", chs, err)
	}
	b, _ := json.Marshal(chs)
	if strings.Contains(string(b), "TOPSECRETTOKEN") || strings.Contains(string(b), "127.0.0.1") {
		t.Errorf("view leaks the address: %s", b)
	}
	v := chs[0]
	if v.ID != "team" || v.MinLevel != "warning" || len(v.SecretFields) != 1 || !v.SecretFields[0].Set || v.SecretFields[0].Name != "url" || v.Legacy {
		t.Errorf("view: %+v", v)
	}
	// The vault holds it under the documented key.
	cfg, _, _ := e.LoadConfigOrDefault()
	vlt, _ := e.OpenVault(cfg)
	if ok, _ := vlt.Has("notify/team/url"); !ok {
		t.Error("secret not stored under notify/team/url")
	}

	// Duplicate id, wrong type on edit, unknown channel.
	if bad, _ := e.SaveChannel(context.Background(), in, true); len(bad) != 1 || bad[0].Field != "id" {
		t.Errorf("duplicate: %v", bad)
	}
	other := in
	other.Type = config.ChannelDiscord
	if bad, _ := e.SaveChannel(context.Background(), other, false); len(bad) != 1 || bad[0].Field != "type" {
		t.Errorf("type change: %v", bad)
	}
	if _, err := e.SaveChannel(context.Background(), slackInput("ghost", hook), false); err == nil {
		t.Error("editing a channel that does not exist")
	}

	// Editing without a secret keeps the stored one.
	got, err := e.GetChannel("team")
	if err != nil {
		t.Fatal(err)
	}
	got.MinLevel = ""
	got.Secrets = nil
	mustSave(t, e, got, false)
	if res, err := e.TestChannels(context.Background(), "team"); err != nil || len(res) != 1 || !res[0].OK {
		t.Fatalf("test after edit: %v %v", res, err)
	}
	if rc.count() != 1 || !strings.HasPrefix(rc.got[0], "/services/TOPSECRETTOKEN ") {
		t.Errorf("received: %v", rc.got)
	}

	removed, err := e.RemoveChannel(context.Background(), "team")
	if err != nil || !removed {
		t.Fatalf("remove: %v %v", removed, err)
	}
	if ok, _ := vlt.Has("notify/team/url"); ok {
		t.Error("secret survived the channel")
	}
	if removed, _ := e.RemoveChannel(context.Background(), "team"); removed {
		t.Error("removed twice")
	}
	if _, err := e.TestChannels(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "no notification channel") {
		t.Errorf("test without channels: %v", err)
	}
}

func TestSaveChannelValidation(t *testing.T) {
	e := newTestEnv(t)
	cases := []struct {
		name  string
		in    ChannelInput
		field string
	}{
		{"missing secret", ChannelInput{ID: "t", Type: config.ChannelTelegram, ChatID: "1"}, "secrets.bot_token"},
		{"missing chat", ChannelInput{ID: "t", Type: config.ChannelTelegram, Secrets: map[string]string{"bot_token": "x"}}, "chatId"},
		{"bad id", slackInput("Bad ID", "https://hooks.example/x"), "id"},
		{"http address", slackInput("s", "http://hooks.example.com/SECRETTOKEN"), "secrets.url"},
		{"not a url", slackInput("s", "SECRETTOKEN nonsense"), "secrets.url"},
		{"unknown secret", ChannelInput{ID: "s", Type: config.ChannelSlack, Secrets: map[string]string{"url": "https://h.example/x", "bot_token": "x"}}, "secrets.bot_token"},
		{"newline", ChannelInput{ID: "s", Type: config.ChannelBark, Secrets: map[string]string{"device_key": "a\nb"}}, "secrets.device_key"},
		{"bad template", func() ChannelInput {
			in := slackInput("s", "https://h.example/x")
			in.Template = "{{.Nope}}"
			return in
		}(), "template"},
		{"bad level", func() ChannelInput { in := slackInput("s", "https://h.example/x"); in.MinLevel = "loud"; return in }(), "minLevel"},
		{"email without recipients", ChannelInput{ID: "m", Type: config.ChannelEmail, SMTPHost: "h", From: "a@b.co"}, "to"},
		{"webhook template", ChannelInput{ID: "w", Type: config.ChannelWebhook, Format: config.FormatTemplate, Template: "plain text", Secrets: map[string]string{"url": "https://h.example/x"}}, "template"},
	}
	for _, c := range cases {
		bad, err := e.SaveChannel(context.Background(), c.in, true)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		found := false
		for _, b := range bad {
			found = found || b.Field == c.field
			if strings.Contains(b.Message, "SECRETTOKEN") {
				t.Errorf("%s: the message echoes the value: %s", c.name, b.Message)
			}
		}
		if !found {
			t.Errorf("%s: want a problem on %q, got %v", c.name, c.field, bad)
		}
	}
	if chs, _ := e.ListChannels(); len(chs) != 0 {
		t.Errorf("invalid channels were saved: %v", chs)
	}
}

func TestOptionalSecretsCanBeClearedAndEmailPassword(t *testing.T) {
	e := newTestEnv(t)
	in := ChannelInput{ID: "dt", Type: config.ChannelDingTalk, Secrets: map[string]string{"url": "https://h.example/x", "secret": "SEC-1"}}
	mustSave(t, e, in, true)
	chs, _ := e.ListChannels()
	set := map[string]bool{}
	for _, f := range chs[0].SecretFields {
		set[f.Name] = f.Set
	}
	if !set["url"] || !set["secret"] {
		t.Fatalf("fields: %+v", chs[0].SecretFields)
	}
	got, _ := e.GetChannel("dt")
	got.ClearSecrets = []string{"secret"}
	mustSave(t, e, got, false)
	chs, _ = e.ListChannels()
	for _, f := range chs[0].SecretFields {
		if f.Name == "secret" && f.Set {
			t.Error("the sign secret was not cleared")
		}
		if f.Name == "url" && !f.Set {
			t.Error("the address was lost")
		}
	}
	// A required secret cannot be cleared.
	got.ClearSecrets = []string{"url"}
	if bad, _ := e.SaveChannel(context.Background(), got, false); len(bad) == 0 {
		t.Error("cleared a required secret")
	}
}

func TestLegacyWebhookStillWorksAndMigratesOnSave(t *testing.T) {
	e := newTestEnv(t)
	rc := newRecv(t)
	cfg, _, _ := e.LoadConfigOrDefault()
	vlt, err := e.OpenVault(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// What an older version left behind: the address under notify/webhook.
	if err := vlt.Put(notify.VaultKey, []byte(rc.URL+"/old/LEGACYTOKEN")); err != nil {
		t.Fatal(err)
	}
	if !e.WebhookConfigured() {
		t.Fatal("legacy webhook not seen")
	}
	chs, _ := e.ListChannels()
	if len(chs) != 1 || chs[0].ID != "webhook" || chs[0].Type != config.ChannelWebhook || !chs[0].Legacy || !chs[0].SecretFields[0].Set {
		t.Fatalf("legacy view: %+v", chs)
	}
	if s, _ := e.LoadSettings(); s.Notify.Channels != 1 || !s.Notify.WebhookConfigured {
		t.Errorf("settings: %+v", s.Notify)
	}
	// It delivers the established JSON body.
	if res, err := e.TestChannels(context.Background(), ""); err != nil || !res[0].OK {
		t.Fatalf("test: %v %v", res, err)
	}
	if !strings.HasPrefix(rc.got[0], "/old/LEGACYTOKEN {") || !strings.Contains(rc.got[0], `"event":"test"`) {
		t.Errorf("received: %v", rc.got)
	}
	// Saving it as a channel moves the secret to the new key and drops the old one.
	in, _ := e.GetChannel("webhook")
	in.MinLevel = config.LevelWarning
	mustSave(t, e, in, false)
	if ok, _ := vlt.Has(notify.VaultKey); ok {
		t.Error("legacy key kept")
	}
	if ok, _ := vlt.Has("notify/webhook/url"); !ok {
		t.Error("secret not moved to notify/webhook/url")
	}
	chs, _ = e.ListChannels()
	if len(chs) != 1 || chs[0].Legacy || chs[0].MinLevel != "warning" {
		t.Fatalf("after migration: %+v", chs)
	}
	if res, err := e.TestChannels(context.Background(), "webhook"); err != nil || !res[0].OK || !strings.HasPrefix(rc.got[1], "/old/LEGACYTOKEN ") {
		t.Fatalf("test after migration: %v %v %v", res, err, rc.got)
	}
}

func TestLegacyPlaintextConfigWebhookIsRead(t *testing.T) {
	e := newTestEnv(t)
	rc := newRecv(t)
	cfg := config.Default()
	cfg.Notify.WebhookURL = rc.URL + "/plain/PLAINTOKEN"
	if err := cfg.Save(filepath.Join(e.cfgDir, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if res, err := e.TestChannels(context.Background(), ""); err != nil || !res[0].OK || !strings.HasPrefix(rc.got[0], "/plain/PLAINTOKEN ") {
		t.Fatalf("%v %v %v", res, err, rc.got)
	}
	// set-webhook semantics: moves it into the vault and clears the plain entry.
	cleared, err := e.SetWebhook(context.Background(), rc.URL+"/new/NEWTOKEN")
	if err != nil || !cleared {
		t.Fatalf("SetWebhook: %v %v", cleared, err)
	}
	back, _, _ := e.LoadConfigOrDefault()
	if back.Notify.WebhookURL != "" || len(back.Notify.Channels) != 1 || back.Notify.Channels[0].ID != "webhook" {
		t.Errorf("config: %+v", back.Notify)
	}
	if err := e.TestWebhook(context.Background()); err != nil || !strings.HasPrefix(rc.got[len(rc.got)-1], "/new/NEWTOKEN ") {
		t.Fatalf("%v %v", err, rc.got)
	}
}

func TestChannelFailuresAreRecordedWithoutSecrets(t *testing.T) {
	e := newTestEnv(t)
	good, bad := newRecv(t), newRecv(t)
	bad.code = 500
	mustSave(t, e, slackInput("good", good.URL+"/g"), true)
	mustSave(t, e, slackInput("bad", bad.URL+"/BADTOKEN"), true)
	res, err := e.TestChannels(context.Background(), "")
	if err != nil || len(res) != 2 {
		t.Fatalf("%v %v", res, err)
	}
	by := map[string]TestResult{res[0].ID: res[0], res[1].ID: res[1]}
	if !by["good"].OK || by["bad"].OK || !strings.Contains(by["bad"].Error, "HTTP 500") || strings.Contains(by["bad"].Error, "BADTOKEN") {
		t.Errorf("results: %+v", res)
	}
	chs, _ := e.ListChannels()
	for _, c := range chs {
		if c.LastResult == nil {
			t.Fatalf("%s: no last result", c.ID)
		}
		if c.ID == "bad" && (c.LastResult.OK || !strings.Contains(c.LastResult.Error, "500") || strings.Contains(c.LastResult.Error, "BADTOKEN")) {
			t.Errorf("bad: %+v", c.LastResult)
		}
		if c.ID == "good" && !c.LastResult.OK {
			t.Errorf("good: %+v", c.LastResult)
		}
	}
	if _, err := e.TestChannels(context.Background(), "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("unknown id: %v", err)
	}
}

func TestRouterFromOpsFiltersAndPicksUpChangesWhileRunning(t *testing.T) {
	e := newTestEnv(t)
	rc := newRecv(t)
	cfg, path, _ := e.LoadConfigOrDefault()
	router := e.Router(cfg, path, nil, false)
	if router.Enabled() {
		t.Fatal("enabled with no channel")
	}
	in := slackInput("crit", rc.URL+"/c")
	in.MinLevel = config.LevelCritical
	mustSave(t, e, in, true)
	// The router built earlier sees the new channel without a restart.
	if !router.Enabled() {
		t.Fatal("not enabled after a channel was added")
	}
	ev := notify.Event{Event: notify.Stale, Account: "a", Message: "m", Time: tNow()}
	if err := router.Send(context.Background(), ev); err != nil || rc.count() != 0 {
		t.Fatalf("a warning reached a critical-only channel: %v %d", err, rc.count())
	}
	ev.Event = notify.NeedsAttention
	if err := router.Send(context.Background(), ev); err != nil || rc.count() != 1 {
		t.Fatalf("critical: %v %d", err, rc.count())
	}
}

func TestPreviewMessage(t *testing.T) {
	e := newTestEnv(t)
	for _, typ := range config.ChannelTypes {
		for _, kind := range notify.SampleKinds() {
			p, err := e.PreviewMessage(typ, "zh", "", "", kind)
			if err != nil || p.Body == "" {
				t.Fatalf("%s/%s: %v %+v", typ, kind, err, p)
			}
		}
	}
	p, _ := e.PreviewMessage(config.ChannelTelegram, "en", "", "", notify.FailureSpike)
	if p.Format != "html" || !strings.Contains(p.Body, "High failure rate") {
		t.Errorf("telegram: %+v", p)
	}
	p, _ = e.PreviewMessage(config.ChannelWebhook, "en", "", "", notify.NeedsAttention)
	if p.Format != "json" || !strings.Contains(p.Body, `"event": "needs_attention"`) {
		t.Errorf("webhook: %+v", p)
	}
	p, err := e.PreviewMessage(config.ChannelSlack, "en", "X {{.Event}}", "", notify.Test)
	if err != nil || p.Body != "X test" {
		t.Errorf("custom: %+v %v", p, err)
	}
	if _, err := e.PreviewMessage(config.ChannelSlack, "en", "{{.Bad}}", "", notify.Test); err == nil {
		t.Error("a broken template was previewed")
	}
	if _, err := e.PreviewMessage("sms", "en", "", "", notify.Test); err == nil {
		t.Error("unknown type")
	}
	if _, err := e.PreviewMessage(config.ChannelSlack, "en", "", "", "boom"); err == nil {
		t.Error("unknown event")
	}
}

func TestNotifySettingsRoundTripAndValidation(t *testing.T) {
	e := newTestEnv(t)
	s, err := e.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.Notify.Cooldown != "6h" || s.Notify.SpikeRounds != 3 || s.Notify.SpikeRate != 0.5 || s.Notify.SpikeMinFailures != 5 || s.Notify.QuietHours != "" {
		t.Fatalf("defaults: %+v", s.Notify)
	}
	s.Notify.Cooldown, s.Notify.QuietHours, s.Notify.SpikeRate = "3h", "23:00-08:00", 0.6
	if bad, err := e.SaveSettings(context.Background(), s); err != nil || len(bad) > 0 {
		t.Fatalf("%v %v", bad, err)
	}
	back, _ := e.LoadSettings()
	if back.Notify.Cooldown != "3h" || back.Notify.QuietHours != "23:00-08:00" || back.Notify.SpikeRate != 0.6 {
		t.Errorf("round trip: %+v", back.Notify)
	}
	s.Notify.QuietHours, s.Notify.Cooldown = "late", "soon"
	bad, _ := e.SaveSettings(context.Background(), s)
	fields := map[string]bool{}
	for _, b := range bad {
		fields[b.Field] = true
	}
	if !fields["notify.quietHours"] || !fields["notify.cooldown"] {
		t.Errorf("problems: %v", bad)
	}
	// Saving the settings must not lose the channels.
	mustSave(t, e, slackInput("keep", "https://h.example/x"), true)
	s, _ = e.LoadSettings()
	if bad, err := e.SaveSettings(context.Background(), s); err != nil || len(bad) > 0 {
		t.Fatal(bad, err)
	}
	if chs, _ := e.ListChannels(); len(chs) != 1 {
		t.Errorf("channels after saving settings: %v", chs)
	}
}

func TestBackupKnowsChannelSecretKeys(t *testing.T) {
	cfg := config.Default()
	cfg.Notify.Channels = []config.Channel{{ID: "tg", Type: config.ChannelTelegram, ChatID: "1"}, {ID: "fs", Type: config.ChannelFeishu}}
	for key, want := range map[string]bool{
		"notify/webhook": true, "notify/tg/bot_token": true, "notify/fs/url": true, "notify/fs/secret": true, "notify/webhook/url": true,
		"notify/tg/url": false, "notify/ghost/url": false, "notify/tg": false, "notify/tg/bot_token/x": false, "secret/main": false,
	} {
		if got := knownVaultKey(cfg, key); got != want && key != "secret/main" {
			t.Errorf("%s: got %v", key, got)
		}
	}
}

func tNow() time.Time { return time.Now() }

func TestLegacyWebhookKeepsAcceptingHTTPButNewChannelsDoNot(t *testing.T) {
	e := newTestEnv(t)
	cfg := config.Default()
	cfg.Notify.WebhookURL = "http://nas.lan/hook/PLAINTOKEN"
	if err := cfg.Save(filepath.Join(e.cfgDir, "config.toml")); err != nil {
		t.Fatal(err)
	}
	c, p, _ := e.LoadConfigOrDefault()
	targets, err := e.channelSource(c, p).Targets()
	if err != nil || len(targets) != 1 || !targets[0].AllowHTTP || targets[0].Secrets["url"] != "http://nas.lan/hook/PLAINTOKEN" {
		t.Fatalf("legacy target: %+v %v", targets, err)
	}
	// A channel saved in the current format must be https.
	if bad, _ := e.SaveChannel(context.Background(), slackInput("n", "http://nas.lan/hook"), true); len(bad) == 0 {
		t.Error("a new channel accepted an http address")
	}
	if _, err := e.SetWebhook(context.Background(), "http://nas.lan/hook"); err == nil {
		t.Error("set-webhook accepted an http address")
	}
}
