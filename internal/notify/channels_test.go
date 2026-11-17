package notify

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

// captured is one request a fake server saw.
type captured struct {
	Method, Path, RawQuery string
	Header                 http.Header
	Body                   []byte
}

// fakeService records requests and answers with a canned reply.
type fakeService struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []captured
	status int
	reply  string
}

func newFake(t *testing.T, status int, reply string) *fakeService {
	t.Helper()
	f := &fakeService{status: status, reply: reply}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, captured{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), b})
		st, rp := f.status, f.reply
		f.mu.Unlock()
		w.WriteHeader(st)
		_, _ = io.WriteString(w, rp)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeService) only(t *testing.T) captured {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(f.reqs))
	}
	return f.reqs[0]
}

func (f *fakeService) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reqs) }

var fixedNow = time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC) // unix 1700000000

func testDeliverer(f *fakeService) *Deliverer {
	d := &Deliverer{Backoff: []time.Duration{}, Now: func() time.Time { return fixedNow }, Host: "dev-box", Location: time.UTC,
		DefaultLanguage: "en"}
	if f != nil {
		d.HTTP = f.Client()
	}
	return d
}

func sampleEv(kind string) Event { return SampleEvent(kind, fixedNow) }

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, b)
	}
	return m
}

func mustContain(t *testing.T, what, s string, subs ...string) {
	t.Helper()
	for _, x := range subs {
		if !strings.Contains(s, x) {
			t.Errorf("%s lacks %q:\n%s", what, x, s)
		}
	}
}

func mustNotContain(t *testing.T, what, s string, subs ...string) {
	t.Helper()
	for _, x := range subs {
		if x != "" && strings.Contains(s, x) {
			t.Errorf("%s leaks %q:\n%s", what, x, s)
		}
	}
}

func TestSignaturesMatchKnownValues(t *testing.T) {
	// Computed independently (Python hmac) for these inputs.
	if got := FeishuSign("1700000000", "SECRET-xyz"); got != "qoPCGI0766uDZC6paoGBCruFSGImXJOylNExaLaAJG4=" {
		t.Errorf("feishu: %s", got)
	}
	if got := DingTalkSign("1700000000000", "SECRET-xyz"); got != "N0wPr2+Gw5rErDn99S9Dx3Kkb+T3fucvL99n2hNcwiQ=" {
		t.Errorf("dingtalk: %s", got)
	}
}

func TestTelegram(t *testing.T) {
	f := newFake(t, 200, `{"ok":true}`)
	tg := Target{Channel: config.Channel{ID: "tg", Type: config.ChannelTelegram, ChatID: "-100123", APIBase: f.URL},
		Secrets: map[string]string{"bot_token": "123456:SECRETTOKEN123"}}
	if err := testDeliverer(f).Deliver(context.Background(), tg, sampleEv(FailureSpike)); err != nil {
		t.Fatal(err)
	}
	r := f.only(t)
	if r.Method != "POST" || r.Path != "/bot123456:SECRETTOKEN123/sendMessage" || r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request: %+v", r)
	}
	b := decode(t, r.Body)
	if b["chat_id"] != "-100123" || b["parse_mode"] != "HTML" || b["disable_web_page_preview"] != true {
		t.Errorf("body: %v", b)
	}
	mustContain(t, "text", b["text"].(string), "<b>[Warning] High failure rate</b>", "<code>demo</code>", "dev-box")
	mustNotContain(t, "body", string(r.Body), "SECRETTOKEN123")
}

func TestTelegramRefusalIsAnErrorWithoutTheToken(t *testing.T) {
	f := newFake(t, 200, `{"ok":false,"description":"Bad Request: chat not found"}`)
	tg := Target{Channel: config.Channel{ID: "tg", Type: config.ChannelTelegram, ChatID: "1", APIBase: f.URL},
		Secrets: map[string]string{"bot_token": "123456:SECRETTOKEN123"}}
	err := testDeliverer(f).Deliver(context.Background(), tg, sampleEv(Test))
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("got %v", err)
	}
	assertNoSecret(t, err.Error(), f.URL)
}

func TestWeCom(t *testing.T) {
	f := newFake(t, 200, `{"errcode":0,"errmsg":"ok"}`)
	u := f.URL + "/cgi-bin/webhook/send?key=QUERYSECRET"
	tgt := Target{Channel: config.Channel{ID: "wc", Type: config.ChannelWeCom, Language: "zh"}, Secrets: map[string]string{"url": u}}
	if err := testDeliverer(f).Deliver(context.Background(), tgt, sampleEv(NeedsAttention)); err != nil {
		t.Fatal(err)
	}
	r := f.only(t)
	if r.Method != "POST" || r.Path != "/cgi-bin/webhook/send" || r.RawQuery != "key=QUERYSECRET" {
		t.Errorf("request: %+v", r)
	}
	b := decode(t, r.Body)
	md := b["markdown"].(map[string]any)["content"].(string)
	if b["msgtype"] != "markdown" {
		t.Errorf("body: %v", b)
	}
	mustContain(t, "markdown", md, "**[严重] 需要处理**", " · demo")
	mustNotContain(t, "body", string(r.Body), "QUERYSECRET")
}

func TestWeComErrorCodeFailsWithoutLeaking(t *testing.T) {
	f := newFake(t, 200, `{"errcode":93000,"errmsg":"invalid webhook url, hint: [x]"}`)
	u := f.URL + "/send?key=QUERYSECRET"
	tgt := Target{Channel: config.Channel{ID: "wc", Type: config.ChannelWeCom}, Secrets: map[string]string{"url": u}}
	err := testDeliverer(f).Deliver(context.Background(), tgt, sampleEv(Test))
	var de *DeliveryError
	if err == nil || !errors.As(err, &de) || de.Temporary || !strings.Contains(err.Error(), "93000") {
		t.Fatalf("got %v", err)
	}
	assertNoSecret(t, err.Error(), u)
	if f.count() != 1 {
		t.Errorf("a permanent error was retried: %d", f.count())
	}
}

func TestFeishuWithAndWithoutSignature(t *testing.T) {
	f := newFake(t, 200, `{"code":0,"msg":"success"}`)
	u := f.URL + "/open-apis/bot/v2/hook/abc"
	tgt := Target{Channel: config.Channel{ID: "fs", Type: config.ChannelFeishu, Language: "zh"}, Secrets: map[string]string{"url": u, "secret": "SECRET-xyz"}}
	if err := testDeliverer(f).Deliver(context.Background(), tgt, sampleEv(AutoPaused)); err != nil {
		t.Fatal(err)
	}
	r := f.only(t)
	if r.Method != "POST" || r.Path != "/open-apis/bot/v2/hook/abc" {
		t.Errorf("request: %+v", r)
	}
	b := decode(t, r.Body)
	if b["timestamp"] != "1700000000" || b["sign"] != "qoPCGI0766uDZC6paoGBCruFSGImXJOylNExaLaAJG4=" || b["msg_type"] != "post" {
		t.Errorf("body: %v", b)
	}
	post := b["content"].(map[string]any)["post"].(map[string]any)["zh_cn"].(map[string]any)
	if !strings.Contains(post["title"].(string), "已自动暂停") || len(post["content"].([]any)) < 3 {
		t.Errorf("post: %v", post)
	}
	mustNotContain(t, "body", string(r.Body), "SECRET-xyz")

	// Without a secret there is no timestamp or sign.
	f2 := newFake(t, 200, `{"code":0}`)
	tgt.Secrets = map[string]string{"url": f2.URL + "/hook"}
	if err := testDeliverer(f2).Deliver(context.Background(), tgt, sampleEv(Test)); err != nil {
		t.Fatal(err)
	}
	b = decode(t, f2.only(t).Body)
	if _, ok := b["sign"]; ok || b["timestamp"] != nil {
		t.Errorf("unsigned body has a signature: %v", b)
	}
}

func TestFeishuErrorCode(t *testing.T) {
	f := newFake(t, 200, `{"code":19021,"msg":"sign match fail or timestamp is not within one hour from current time"}`)
	tgt := Target{Channel: config.Channel{ID: "fs", Type: config.ChannelFeishu}, Secrets: map[string]string{"url": f.URL + "/hook", "secret": "SECRET-xyz"}}
	err := testDeliverer(f).Deliver(context.Background(), tgt, sampleEv(Test))
	if err == nil || !strings.Contains(err.Error(), "19021") || strings.Contains(err.Error(), "SECRET-xyz") {
		t.Fatalf("got %v", err)
	}
}

func TestDingTalkSignatureIsOnTheURL(t *testing.T) {
	f := newFake(t, 200, `{"errcode":0,"errmsg":"ok"}`)
	u := f.URL + "/robot/send?access_token=QUERYSECRET"
	tgt := Target{Channel: config.Channel{ID: "dt", Type: config.ChannelDingTalk, Language: "en"}, Secrets: map[string]string{"url": u, "secret": "SECRET-xyz"}}
	if err := testDeliverer(f).Deliver(context.Background(), tgt, sampleEv(Stale)); err != nil {
		t.Fatal(err)
	}
	r := f.only(t)
	q, err := url.ParseQuery(r.RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != "/robot/send" || q.Get("access_token") != "QUERYSECRET" || q.Get("timestamp") != "1700000000000" ||
		q.Get("sign") != "N0wPr2+Gw5rErDn99S9Dx3Kkb+T3fucvL99n2hNcwiQ=" {
		t.Errorf("query: %s", r.RawQuery)
	}
	if !strings.Contains(r.RawQuery, "sign=N0wPr2%2BGw5rErDn99S9Dx3Kkb%2BT3fucvL99n2hNcwiQ%3D") {
		t.Errorf("sign is not URL-encoded: %s", r.RawQuery)
	}
	b := decode(t, r.Body)
	md := b["markdown"].(map[string]any)
	if b["msgtype"] != "markdown" || !strings.Contains(md["title"].(string), "No successful call") {
		t.Errorf("body: %v", b)
	}
	mustNotContain(t, "body", string(r.Body), "SECRET-xyz", "QUERYSECRET")

	// A failure never shows the signed URL.
	f.mu.Lock()
	f.reply = `{"errcode":310000,"errmsg":"keywords not in content"}`
	f.mu.Unlock()
	err = testDeliverer(f).Deliver(context.Background(), tgt, sampleEv(Test))
	if err == nil || !strings.Contains(err.Error(), "keywords not in content") {
		t.Fatalf("got %v", err)
	}
	mustNotContain(t, "error", err.Error(), "SECRET-xyz", "QUERYSECRET", "sign=")

	// No secret: no signature parameters.
	f3 := newFake(t, 200, `{"errcode":0}`)
	tgt.Secrets = map[string]string{"url": f3.URL + "/robot/send?access_token=QUERYSECRET"}
	if err := testDeliverer(f3).Deliver(context.Background(), tgt, sampleEv(Test)); err != nil {
		t.Fatal(err)
	}
	if rq := f3.only(t).RawQuery; rq != "access_token=QUERYSECRET" {
		t.Errorf("unsigned query: %s", rq)
	}
}

func TestDiscordAndSlack(t *testing.T) {
	f := newFake(t, 204, "")
	tgt := Target{Channel: config.Channel{ID: "dc", Type: config.ChannelDiscord}, Secrets: map[string]string{"url": f.URL + "/api/webhooks/1/QUERYSECRET"}}
	if err := testDeliverer(f).Deliver(context.Background(), tgt, sampleEv(SecretExpiring)); err != nil {
		t.Fatal(err)
	}
	r := f.only(t)
	b := decode(t, r.Body)
	if r.Method != "POST" || r.Path != "/api/webhooks/1/QUERYSECRET" {
		t.Errorf("request: %+v", r)
	}
	mustContain(t, "content", b["content"].(string), "**[Warning] Client secret expiring**")
	if am := b["allowed_mentions"].(map[string]any); len(am["parse"].([]any)) != 0 {
		t.Errorf("mentions allowed: %v", am)
	}

	f2 := newFake(t, 200, "ok")
	tgt = Target{Channel: config.Channel{ID: "sl", Type: config.ChannelSlack}, Secrets: map[string]string{"url": f2.URL + "/services/T/B/QUERYSECRET"}}
	if err := testDeliverer(f2).Deliver(context.Background(), tgt, sampleEv(PeriodEnded)); err != nil {
		t.Fatal(err)
	}
	b = decode(t, f2.only(t).Body)
	mustContain(t, "text", b["text"].(string), "*[Info] Run period ended*", " · demo")
	if b["mrkdwn"] != true {
		t.Errorf("mrkdwn: %v", b)
	}
}

func TestBarkLevelsAndServer(t *testing.T) {
	f := newFake(t, 200, `{"code":200,"message":"success"}`)
	tgt := Target{Channel: config.Channel{ID: "bk", Type: config.ChannelBark, Server: f.URL, Language: "zh"}, Secrets: map[string]string{"device_key": "DEVKEY123"}}
	if err := testDeliverer(f).Deliver(context.Background(), tgt, sampleEv(NeedsAttention)); err != nil {
		t.Fatal(err)
	}
	r := f.only(t)
	b := decode(t, r.Body)
	if r.Method != "POST" || r.Path != "/push" || b["device_key"] != "DEVKEY123" || b["level"] != "critical" || !strings.Contains(b["title"].(string), "需要处理") {
		t.Errorf("request: %+v %v", r, b)
	}
	if err := testDeliverer(f).Deliver(context.Background(), tgt, sampleEv(Recovered)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	last := decode(t, f.reqs[1].Body)
	f.mu.Unlock()
	if last["level"] != "active" {
		t.Errorf("non-critical level: %v", last["level"])
	}
	// Bark reports errors in the body.
	f.mu.Lock()
	f.reply = `{"code":400,"message":"failed to get device token"}`
	f.mu.Unlock()
	if err := testDeliverer(f).Deliver(context.Background(), tgt, sampleEv(Test)); err == nil || !strings.Contains(err.Error(), "device token") {
		t.Errorf("got %v", err)
	}
}

func TestNtfyHeaders(t *testing.T) {
	f := newFake(t, 200, `{}`)
	tgt := Target{Channel: config.Channel{ID: "nt", Type: config.ChannelNtfy, Server: f.URL, Topic: "e5-alerts", Language: "zh"},
		Secrets: map[string]string{"token": "tk_SECRETTOKEN123"}}
	if err := testDeliverer(f).Deliver(context.Background(), tgt, sampleEv(AllFailing)); err != nil {
		t.Fatal(err)
	}
	r := f.only(t)
	if r.Method != "POST" || r.Path != "/e5-alerts" || r.Header.Get("Priority") != "5" || r.Header.Get("Tags") != "rotating_light" ||
		r.Header.Get("Authorization") != "Bearer tk_SECRETTOKEN123" {
		t.Errorf("request: %+v", r.Header)
	}
	title, err := new(mime.WordDecoder).DecodeHeader(r.Header.Get("Title"))
	if err != nil || !strings.Contains(title, "所有账号同时异常") {
		t.Errorf("title %q: %v", r.Header.Get("Title"), err)
	}
	mustContain(t, "body", string(r.Body), "所有账号同时异常")
	mustNotContain(t, "body", string(r.Body), "SECRETTOKEN123")

	// No token: no Authorization header; warning priority.
	f2 := newFake(t, 200, `{}`)
	tgt.Channel.Server, tgt.Secrets = f2.URL, nil
	tgt.Channel.Language = "en"
	if err := testDeliverer(f2).Deliver(context.Background(), tgt, sampleEv(Stale)); err != nil {
		t.Fatal(err)
	}
	r = f2.only(t)
	if r.Header.Get("Authorization") != "" || r.Header.Get("Priority") != "4" || mimeDecode(r.Header.Get("Title")) != "E5 Developer Extend · No successful call for a long time · demo" {
		t.Errorf("headers: %v", r.Header)
	}
}

func TestServerChan(t *testing.T) {
	f := newFake(t, 200, `{"code":0,"message":"","data":{}}`)
	d := testDeliverer(f)
	d.ServerChanBase = f.URL
	tgt := Target{Channel: config.Channel{ID: "sc", Type: config.ChannelServerChan, Language: "zh"}, Secrets: map[string]string{"sendkey": "SCTSECRETTOKEN123"}}
	if err := d.Deliver(context.Background(), tgt, sampleEv(FailureSpike)); err != nil {
		t.Fatal(err)
	}
	r := f.only(t)
	if r.Method != "POST" || r.Path != "/SCTSECRETTOKEN123.send" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		t.Errorf("request: %+v", r)
	}
	form, _ := url.ParseQuery(string(r.Body))
	mustContain(t, "title", form.Get("title"), "调用失败率过高")
	mustContain(t, "desp", form.Get("desp"), "**[警告] 调用失败率过高**")
	mustNotContain(t, "body", string(r.Body), "SCTSECRETTOKEN123")
}

func TestGenericWebhook(t *testing.T) {
	f := newFake(t, 200, "")
	u := f.URL + "/hook?token=QUERYSECRET"
	tgt := Target{Channel: config.Channel{ID: "webhook", Type: config.ChannelWebhook}, Secrets: map[string]string{"url": u}}
	if err := testDeliverer(f).Deliver(context.Background(), tgt, sampleEv(FailureSpike)); err != nil {
		t.Fatal(err)
	}
	r := f.only(t)
	b := decode(t, r.Body)
	if r.Method != "POST" || r.Path != "/hook" || r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request: %+v", r)
	}
	// The established fields are all there, and the new ones are additive.
	for _, k := range []string{"event", "account", "message", "time", "text", "content"} {
		if _, ok := b[k]; !ok {
			t.Errorf("missing %q: %v", k, b)
		}
	}
	if b["event"] != "failure_spike" || b["level"] != "warning" || b["account"] != "demo" {
		t.Errorf("body: %v", b)
	}
	mustNotContain(t, "body", string(r.Body), "QUERYSECRET")

	// format = template.
	f2 := newFake(t, 200, "")
	tgt = Target{Channel: config.Channel{ID: "hook", Type: config.ChannelWebhook, Format: config.FormatTemplate,
		Template: `{"msg":{{json .Message}},"lvl":"{{.Level}}","who":"{{.Account}}"}`}, Secrets: map[string]string{"url": f2.URL + "/h"}}
	if err := testDeliverer(f2).Deliver(context.Background(), tgt, sampleEv(NeedsAttention)); err != nil {
		t.Fatal(err)
	}
	r = f2.only(t)
	b = decode(t, r.Body)
	if r.Header.Get("Content-Type") != "application/json" || b["lvl"] != "critical" || b["who"] != "demo" || !strings.HasPrefix(b["msg"].(string), "needs attention") {
		t.Errorf("template body: %s", r.Body)
	}
}

func TestDigestThroughAChannel(t *testing.T) {
	f := newFake(t, 200, "ok")
	tgt := Target{Channel: config.Channel{ID: "sl", Type: config.ChannelSlack}, Secrets: map[string]string{"url": f.URL + "/h"}}
	if err := testDeliverer(f).Deliver(context.Background(), tgt, sampleEv(Digest)); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "digest", string(f.only(t).Body), "2 alerts", "Client secret expiring")

	f2 := newFake(t, 200, "")
	tgt = Target{Channel: config.Channel{ID: "w", Type: config.ChannelWebhook}, Secrets: map[string]string{"url": f2.URL + "/h"}}
	if err := testDeliverer(f2).Deliver(context.Background(), tgt, sampleEv(Digest)); err != nil {
		t.Fatal(err)
	}
	items := decode(t, f2.only(t).Body)["items"].([]any)
	if len(items) != 2 {
		t.Errorf("webhook digest items: %v", items)
	}
}

func TestMissingSecretAndInsecureAddress(t *testing.T) {
	d := testDeliverer(nil)
	err := d.Deliver(context.Background(), Target{Channel: config.Channel{ID: "tg", Type: config.ChannelTelegram, ChatID: "1"}}, sampleEv(Test))
	if err == nil || !strings.Contains(err.Error(), "bot_token is not set") {
		t.Errorf("missing token: %v", err)
	}
	for _, typ := range []string{config.ChannelWeCom, config.ChannelFeishu, config.ChannelDingTalk, config.ChannelDiscord, config.ChannelSlack, config.ChannelWebhook} {
		err := d.Deliver(context.Background(), Target{Channel: config.Channel{ID: "x", Type: typ}, Secrets: map[string]string{"url": "http://example.com/QUERYSECRET"}}, sampleEv(Test))
		if err == nil || !strings.Contains(err.Error(), "https") {
			t.Errorf("%s over http: %v", typ, err)
			continue
		}
		mustNotContain(t, typ, err.Error(), "QUERYSECRET", "example.com")
	}
	// Telegram's api_base must be https as well.
	err = d.Deliver(context.Background(), Target{Channel: config.Channel{ID: "tg", Type: config.ChannelTelegram, ChatID: "1", APIBase: "http://proxy.example"},
		Secrets: map[string]string{"bot_token": "SECRETTOKEN123"}}, sampleEv(Test))
	if err == nil || strings.Contains(err.Error(), "SECRETTOKEN123") {
		t.Errorf("telegram over http: %v", err)
	}
}

func TestBarkAndNtfyAllowPlainHTTPServers(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"code":200}`)
	}))
	defer srv.Close()
	d := testDeliverer(nil)
	for _, tg := range []Target{
		{Channel: config.Channel{ID: "bk", Type: config.ChannelBark, Server: srv.URL}, Secrets: map[string]string{"device_key": "k"}},
		{Channel: config.Channel{ID: "nt", Type: config.ChannelNtfy, Server: srv.URL, Topic: "t"}},
	} {
		if err := d.Deliver(context.Background(), tg, sampleEv(Test)); err != nil {
			t.Errorf("%s over http: %v", tg.Channel.Type, err)
		}
	}
	if hits.Load() != 2 {
		t.Errorf("hits: %d", hits.Load())
	}
}

func TestRetryClassesAndRedirects(t *testing.T) {
	f := newFake(t, 503, "")
	d := testDeliverer(f)
	d.Backoff = []time.Duration{time.Millisecond, time.Millisecond}
	tgt := Target{Channel: config.Channel{ID: "sl", Type: config.ChannelSlack}, Secrets: map[string]string{"url": f.URL + "/QUERYSECRET"}}
	err := d.Deliver(context.Background(), tgt, sampleEv(Test))
	var de *DeliveryError
	if err == nil || !errors.As(err, &de) || de.Status != 503 || !de.Temporary || f.count() != 3 {
		t.Fatalf("5xx: %v, %d requests", err, f.count())
	}
	assertNoSecret(t, err.Error(), tgt.Secrets["url"])

	f4 := newFake(t, 404, "")
	tgt.Secrets["url"] = f4.URL + "/x"
	err = d.Deliver(context.Background(), tgt, sampleEv(Test))
	if err == nil || f4.count() != 1 {
		t.Errorf("4xx: %v, %d requests", err, f4.count())
	}

	fr := newFake(t, 302, "")
	tgt.Secrets["url"] = fr.URL + "/x"
	err = d.Deliver(context.Background(), tgt, sampleEv(Test))
	if err == nil || !strings.Contains(err.Error(), "redirect") || fr.count() != 1 {
		t.Errorf("redirect: %v, %d requests", err, fr.count())
	}
}

func TestMessagesNeverCarryChannelSecrets(t *testing.T) {
	secrets := []string{"SECRETTOKEN123", "QUERYSECRET", "SECRET-xyz", "DEVKEY123"}
	types := map[string]map[string]string{
		config.ChannelTelegram: {"bot_token": secrets[0]}, config.ChannelWeCom: {"url": "/send?key=QUERYSECRET"},
		config.ChannelFeishu: {"url": "/h/QUERYSECRET", "secret": "SECRET-xyz"}, config.ChannelDingTalk: {"url": "/r?access_token=QUERYSECRET", "secret": "SECRET-xyz"},
		config.ChannelDiscord: {"url": "/w/QUERYSECRET"}, config.ChannelSlack: {"url": "/s/QUERYSECRET"},
		config.ChannelBark: {"device_key": "DEVKEY123"}, config.ChannelNtfy: {"token": secrets[0]},
		config.ChannelServerChan: {"sendkey": secrets[0]}, config.ChannelWebhook: {"url": "/h?t=QUERYSECRET"},
	}
	for typ, sec := range types {
		f := newFake(t, 200, `{"ok":true,"code":0,"errcode":0}`)
		d := testDeliverer(f)
		d.ServerChanBase = f.URL
		s := map[string]string{}
		for k, v := range sec {
			if k == "url" {
				v = f.URL + v
			}
			s[k] = v
		}
		ch := config.Channel{ID: "c", Type: typ, ChatID: "1", APIBase: f.URL, Server: f.URL, Topic: "t"}
		for _, kind := range SampleKinds() {
			f.mu.Lock()
			f.reqs = nil
			f.mu.Unlock()
			if err := d.Deliver(context.Background(), Target{Channel: ch, Secrets: s}, sampleEv(kind)); err != nil {
				t.Fatalf("%s/%s: %v", typ, kind, err)
			}
			r := f.only(t)
			text := string(r.Body)
			if typ == config.ChannelBark { // the protocol itself carries the device key in the request; the message text must not
				m := decode(t, r.Body)
				text = m["title"].(string) + m["body"].(string)
			}
			mustNotContain(t, typ+"/"+kind+" body", text, secrets...)
			for k, v := range r.Header {
				if k != "Authorization" {
					mustNotContain(t, typ+" header "+k, strings.Join(v, ","), secrets...)
				}
			}
		}
	}
}

func mimeDecode(s string) string {
	d, err := new(mime.WordDecoder).DecodeHeader(s)
	if err != nil {
		return s
	}
	return d
}

// trustAll returns a client that trusts the certificates of every fake server.
func trustAll(fs ...*fakeService) *http.Client {
	pool := x509.NewCertPool()
	for _, f := range fs {
		pool.AddCert(f.Certificate())
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
}

func TestClientErrorsShowTheServicesReasonWithoutSecrets(t *testing.T) {
	f := newFake(t, 400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	tg := Target{Channel: config.Channel{ID: "tg", Type: config.ChannelTelegram, ChatID: "1", APIBase: f.URL}, Secrets: map[string]string{"bot_token": "123456:SECRETTOKEN123"}}
	err := testDeliverer(f).Deliver(context.Background(), tg, sampleEv(Test))
	if err == nil || !strings.Contains(err.Error(), "HTTP 400: Bad Request: chat not found") {
		t.Fatalf("got %v", err)
	}
	assertNoSecret(t, err.Error(), f.URL)
	// A body that echoes the secret is redacted, even a very short one.
	f2 := newFake(t, 401, `{"message":"bad key zq in request"}`)
	tg2 := Target{Channel: config.Channel{ID: "sl", Type: config.ChannelSlack}, Secrets: map[string]string{"url": f2.URL + "/zq"}}
	err = testDeliverer(f2).Deliver(context.Background(), tg2, sampleEv(Test))
	if err == nil || strings.Contains(err.Error(), "zq") {
		t.Fatalf("short secret leaked: %v", err)
	}
	// An HTML error page is not echoed.
	f3 := newFake(t, 403, `<html><body>Forbidden</body></html>`)
	tg2.Secrets["url"] = f3.URL + "/x"
	if err = testDeliverer(f3).Deliver(context.Background(), tg2, sampleEv(Test)); err == nil || strings.Contains(err.Error(), "<") {
		t.Fatalf("got %v", err)
	}
}

func TestServerChanTurboAndServerChan3(t *testing.T) {
	if got := ServerChanURL("SCT123abcDEF"); got != "https://sctapi.ftqq.com/SCT123abcDEF.send" {
		t.Errorf("turbo: %s", got)
	}
	if got := ServerChanURL("sctp1234t9XyZabc"); got != "https://1234.push.ft07.com/send/sctp1234t9XyZabc.send" {
		t.Errorf("server chan3: %s", got)
	}
	for key, wantPath := range map[string]string{"SCT123abcDEF": "/SCT123abcDEF.send", "sctp1234t9XyZabc": "/send/sctp1234t9XyZabc.send"} {
		f := newFake(t, 200, `{"code":0,"message":"success"}`)
		d := testDeliverer(f)
		d.ServerChanBase = f.URL
		tgt := Target{Channel: config.Channel{ID: "sc", Type: config.ChannelServerChan}, Secrets: map[string]string{"sendkey": key}}
		if err := d.Deliver(context.Background(), tgt, sampleEv(Test)); err != nil {
			t.Fatal(err)
		}
		r := f.only(t)
		if r.Path != wantPath || r.Method != "POST" {
			t.Errorf("%s: %s %s", key, r.Method, r.Path)
		}
		form, _ := url.ParseQuery(string(r.Body))
		if form.Get("title") == "" || form.Get("desp") == "" || strings.Contains(string(r.Body), key) {
			t.Errorf("%s body: %s", key, r.Body)
		}
	}
}
