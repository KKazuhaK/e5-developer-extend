package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

// Target is a channel ready to send: its settings and its secrets. Secrets
// are keyed by field name (bot_token, url, secret, ...). Nothing here is ever
// logged or put into an error.
type Target struct {
	Channel config.Channel
	Secrets map[string]string
	// AllowHTTP lets the address be http: set for a webhook that is still
	// stored the way older versions did, which accepted http.
	AllowHTTP bool
	// Err, when set, is why the secrets could not be read; Deliver reports it.
	Err error
}

// Default servers of the services that have one.
const (
	DefaultTelegramBase = "https://api.telegram.org"
	DefaultBarkServer   = "https://api.day.app"
	DefaultNtfyServer   = "https://ntfy.sh"
	serverChanBase      = "https://sctapi.ftqq.com"
)

// Deliverer renders an event for a channel and sends it.
type Deliverer struct {
	HTTP    *http.Client
	Timeout time.Duration   // per attempt; DefaultTimeout when zero
	Backoff []time.Duration // waits before each retry; DefaultBackoff when nil
	Log     *slog.Logger
	// Now supplies the clock for signatures; time.Now when nil.
	Now func() time.Time
	// Host is the machine name shown in messages; os.Hostname when empty.
	Host string
	// Location is the time zone of the message time; local when nil.
	Location *time.Location
	// DefaultLanguage is used by channels without a language of their own.
	DefaultLanguage string
	// ServerChanBase replaces the Server Chan address; for tests.
	ServerChanBase string
	// TLSConfig is the base TLS configuration of email connections (for
	// example to trust a private CA); nil uses the system's.
	TLSConfig *tls.Config
}

func (d *Deliverer) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Deliverer) host() string {
	if d.Host != "" {
		return d.Host
	}
	h, _ := os.Hostname()
	return h
}

// sizeLimit is how much body a service accepts; bytes says what max counts.
type sizeLimit struct {
	max   int
	bytes bool
}

// limits are kept a little under what each service documents.
var limits = map[string]sizeLimit{
	config.ChannelTelegram:   {4096, false},
	config.ChannelWeCom:      {4096, true},
	config.ChannelDingTalk:   {20000, true},
	config.ChannelDiscord:    {2000, false},
	config.ChannelSlack:      {35000, false},
	config.ChannelBark:       {4000, false},
	config.ChannelServerChan: {30000, true},
	config.ChannelNtfy:       {4000, true},
	config.ChannelFeishu:     {20000, true},
}

func (l sizeLimit) fits(body string) bool {
	if l.max == 0 {
		return true
	}
	if l.bytes {
		return len(body) <= l.max
	}
	return utf8.RuneCountInString(body) <= l.max
}

// Deliver sends ev through the channel, retrying temporary failures with
// backoff. It does not apply the channel's level and event filters. A message
// that is too long for the service is split (a digest at item boundaries) or
// shortened before it is rendered, so what is sent is always well-formed.
func (d *Deliverer) Deliver(ctx context.Context, t Target, ev Event) error {
	_, err := d.DeliverParts(ctx, t, ev)
	return err
}

// leaves are the single alerts an event stands for: the items of a digest, or the event itself.
func leaves(ev Event) []Event {
	if ev.Event == Digest {
		return ev.Items
	}
	return []Event{ev}
}

// DeliverParts is Deliver that also reports which alerts reached the
// channel: when a split digest fails midway, the parts sent before the
// failure are not lost.
func (d *Deliverer) DeliverParts(ctx context.Context, t Target, ev Event) (sent []Event, err error) {
	c := t.Channel
	if t.Err != nil {
		return nil, fmt.Errorf("channel %s: %w", c.ID, t.Err)
	}
	lang := Language(c.Language, d.DefaultLanguage)
	render := func(e Event) (Rendered, error) {
		if c.Type == config.ChannelWebhook && c.Format != config.FormatTemplate {
			return Rendered{}, nil
		}
		r, err := Render(c.Type, lang, c.Template, NewData(e, lang, d.host(), d.Location))
		if err != nil {
			return r, fmt.Errorf("render for channel %s: %w", c.ID, err)
		}
		return r, nil
	}
	parts, err := fitEvent(ev, limits[c.Type], render)
	if err != nil {
		return nil, err
	}
	backoff := d.Backoff
	if backoff == nil {
		backoff = DefaultBackoff
	}
	label := c.Type + " channel " + c.ID
	for _, p := range parts {
		rendered, err := render(p)
		if err != nil {
			return sent, err
		}
		err = withRetry(ctx, backoff, d.Log, label, p.Event, func() error {
			return d.send(ctx, t, p, lang, rendered)
		})
		if err != nil {
			return sent, err
		}
		sent = append(sent, leaves(p)...)
	}
	return sent, nil
}

// fitEvent returns the events to send so that each renders within limit. A
// digest is split at item boundaries; an event whose own text is too long gets
// its message cut (the raw text, before any escaping, so no tag or entity is
// ever cut in half).
func fitEvent(ev Event, limit sizeLimit, render func(Event) (Rendered, error)) ([]Event, error) {
	fits := func(e Event) (bool, error) {
		r, err := render(e)
		return limit.fits(r.Body), err
	}
	if ok, err := fits(ev); err != nil || ok {
		return []Event{ev}, err
	}
	shrink := func(e Event) (Event, error) {
		m := []rune(e.Message)
		for len(m) > 16 {
			m = m[:len(m)*3/4]
			e.Message = string(m) + "…"
			if ok, err := fits(e); err != nil || ok {
				return e, err
			}
		}
		return e, nil
	}
	if ev.Event != Digest {
		e, err := shrink(ev)
		return []Event{e}, err
	}
	chunk := func(items []Event) Event {
		if len(items) == 1 {
			return items[0]
		}
		c := ev
		c.Items = items
		c.Message = fmt.Sprintf("%d alerts were held during the quiet hours", len(items))
		return c
	}
	var out []Event
	var cur []Event
	flush := func() error {
		if len(cur) == 0 {
			return nil
		}
		e := chunk(cur)
		if len(cur) == 1 {
			var err error
			if e, err = shrink(e); err != nil {
				return err
			}
		}
		out = append(out, e)
		cur = nil
		return nil
	}
	for _, it := range ev.Items {
		ok, err := fits(chunk(append(slices.Clone(cur), it)))
		if err != nil {
			return nil, err
		}
		if !ok && len(cur) > 0 {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		cur = append(cur, it)
	}
	return out, flush()
}

// send makes one attempt.
func (d *Deliverer) send(ctx context.Context, t Target, ev Event, lang string, r Rendered) error {
	c := t.Channel
	need := func(field string) (string, error) {
		v := strings.TrimSpace(t.Secrets[field])
		if v == "" {
			return "", &DeliveryError{msg: fmt.Sprintf("channel %s: %s is not set", c.ID, field)}
		}
		return v, nil
	}
	if c.Type == config.ChannelEmail {
		return d.sendEmail(ctx, t, r)
	}
	req, err := d.build(t, ev, lang, r, need)
	if err != nil {
		return err
	}
	req.label = c.Type
	return doRequest(ctx, d.HTTP, d.Timeout, req)
}

func jsonBody(v any) []byte { b, _ := json.Marshal(v); return b }

var jsonHeader = map[string]string{"Content-Type": "application/json"}

// build makes the HTTP request of a channel type.
func (d *Deliverer) build(t Target, ev Event, lang string, r Rendered, need func(string) (string, error)) (request, error) {
	c := t.Channel
	switch c.Type {
	case config.ChannelTelegram:
		token, err := need("bot_token")
		if err != nil {
			return request{}, err
		}
		base := strings.TrimRight(orDefault(c.APIBase, DefaultTelegramBase), "/")
		if err := requireScheme(base, t.AllowHTTP); err != nil {
			return request{}, err
		}
		return request{method: http.MethodPost, url: base + "/bot" + token + "/sendMessage", header: jsonHeader,
			body: jsonBody(map[string]any{"chat_id": c.ChatID, "text": r.Body,
				"parse_mode": "HTML", "disable_web_page_preview": true}),
			secret: []string{token, base + "/bot" + token}, check: checkTelegram}, nil

	case config.ChannelWeCom:
		u, err := need("url")
		if err != nil {
			return request{}, err
		}
		if err := requireScheme(u, t.AllowHTTP); err != nil {
			return request{}, err
		}
		return request{method: http.MethodPost, url: u, header: jsonHeader,
			body:   jsonBody(map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": r.Body}}),
			secret: []string{u}, check: checkErrCode("errcode", "errmsg", 45009)}, nil

	case config.ChannelFeishu:
		u, err := need("url")
		if err != nil {
			return request{}, err
		}
		if err := requireScheme(u, t.AllowHTTP); err != nil {
			return request{}, err
		}
		var lines [][]map[string]string
		for _, line := range strings.Split(r.Body, "\n") {
			lines = append(lines, []map[string]string{{"tag": "text", "text": line}})
		}
		key := "zh_cn"
		if lang == "en" {
			key = "en_us"
		}
		body := map[string]any{"msg_type": "post", "content": map[string]any{"post": map[string]any{
			key: map[string]any{"title": r.Title, "content": lines}}}}
		secret := strings.TrimSpace(t.Secrets["secret"])
		secrets := []string{u}
		if secret != "" {
			ts := strconv.FormatInt(d.now().Unix(), 10)
			body["timestamp"], body["sign"] = ts, FeishuSign(ts, secret)
			secrets = append(secrets, secret)
		}
		return request{method: http.MethodPost, url: u, header: jsonHeader, body: jsonBody(body),
			secret: secrets, check: checkErrCode("code", "msg", 11232)}, nil

	case config.ChannelDingTalk:
		u, err := need("url")
		if err != nil {
			return request{}, err
		}
		if err := requireScheme(u, t.AllowHTTP); err != nil {
			return request{}, err
		}
		secrets := []string{u}
		if secret := strings.TrimSpace(t.Secrets["secret"]); secret != "" {
			pu, err := url.Parse(u)
			if err != nil {
				return request{}, &DeliveryError{msg: fmt.Sprintf("channel %s: the address is not a valid URL", c.ID)}
			}
			ms := strconv.FormatInt(d.now().UnixMilli(), 10)
			q := pu.Query()
			q.Set("timestamp", ms)
			q.Set("sign", DingTalkSign(ms, secret))
			pu.RawQuery = q.Encode()
			secrets = append(secrets, u, pu.String(), secret)
			u = pu.String()
		}
		return request{method: http.MethodPost, url: u, header: jsonHeader,
			body: jsonBody(map[string]any{"msgtype": "markdown",
				"markdown": map[string]string{"title": r.Title, "text": r.Body}}),
			secret: secrets, check: checkErrCode("errcode", "errmsg", 130101, 660026)}, nil

	case config.ChannelDiscord:
		u, err := need("url")
		if err != nil {
			return request{}, err
		}
		if err := requireScheme(u, t.AllowHTTP); err != nil {
			return request{}, err
		}
		// allowed_mentions: an alert text must never ping @everyone.
		return request{method: http.MethodPost, url: u, header: jsonHeader,
			body: jsonBody(map[string]any{"content": r.Body,
				"allowed_mentions": map[string]any{"parse": []string{}}}), secret: []string{u}}, nil

	case config.ChannelSlack:
		u, err := need("url")
		if err != nil {
			return request{}, err
		}
		if err := requireScheme(u, t.AllowHTTP); err != nil {
			return request{}, err
		}
		return request{method: http.MethodPost, url: u, header: jsonHeader,
			body: jsonBody(map[string]any{"text": r.Body, "mrkdwn": true}), secret: []string{u}}, nil

	case config.ChannelBark:
		key, err := need("device_key")
		if err != nil {
			return request{}, err
		}
		server := strings.TrimRight(orDefault(c.Server, DefaultBarkServer), "/")
		if err := requireScheme(server, true); err != nil {
			return request{}, err
		}
		level := "active"
		if ev.Level() == config.LevelCritical {
			level = "critical"
		}
		return request{method: http.MethodPost, url: server + "/push", header: jsonHeader,
			body: jsonBody(map[string]string{"device_key": key, "title": r.Title,
				"body": r.Body, "level": level, "group": "e5extend"}),
			secret: []string{key}, check: checkErrCode("code", "message", -1)}, nil

	case config.ChannelNtfy:
		server := strings.TrimRight(orDefault(c.Server, DefaultNtfyServer), "/")
		if err := requireScheme(server, true); err != nil {
			return request{}, err
		}
		h := map[string]string{"Content-Type": "text/plain; charset=utf-8", "Title": headerText(r.Title)}
		switch ev.Level() {
		case config.LevelCritical:
			h["Priority"], h["Tags"] = "5", "rotating_light"
		case config.LevelWarning:
			h["Priority"], h["Tags"] = "4", "warning"
		default:
			h["Priority"], h["Tags"] = "3", "information_source"
		}
		if ev.Event == Recovered {
			h["Tags"] = "white_check_mark"
		}
		// The topic is a kind of password on a public server, so it is kept out of errors too.
		secrets := []string{server + "/" + url.PathEscape(c.Topic)}
		if tok := strings.TrimSpace(t.Secrets["token"]); tok != "" {
			h["Authorization"] = "Bearer " + tok
			secrets = append(secrets, tok)
		}
		return request{method: http.MethodPost, url: server + "/" + url.PathEscape(c.Topic), header: h,
			body: []byte(r.Body), secret: secrets}, nil

	case config.ChannelServerChan:
		key, err := need("sendkey")
		if err != nil {
			return request{}, err
		}
		form := url.Values{"title": {r.Title}, "desp": {r.Body}}
		target := ServerChanURL(key)
		if d.ServerChanBase != "" { // tests
			target = strings.TrimRight(d.ServerChanBase, "/") + serverChanPath(key)
		}
		return request{method: http.MethodPost, url: target,
			header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, body: []byte(form.Encode()),
			secret: []string{key, target}, check: checkErrCode("code", "message", -1)}, nil

	case config.ChannelWebhook:
		u, err := need("url")
		if err != nil {
			return request{}, err
		}
		if err := requireScheme(u, t.AllowHTTP); err != nil {
			return request{}, err
		}
		body := []byte(nil)
		if c.Format == config.FormatTemplate {
			body = []byte(r.Body)
		} else {
			body = jsonBody(ev.payload())
		}
		return request{method: http.MethodPost, url: u, header: jsonHeader, body: body, secret: []string{u}}, nil
	}
	return request{}, &DeliveryError{msg: "unknown channel type " + c.Type}
}

var serverChan3Re = regexp.MustCompile(`^sctp(\d+)t`)

// serverChanPath is the path part of the send address of a SendKey: Server Chan
// Turbo keys (SCT...) use /<key>.send, Server Chan3 keys (sctp<uid>t...) /send/<key>.send.
func serverChanPath(key string) string {
	if serverChan3Re.MatchString(key) {
		return "/send/" + url.PathEscape(key) + ".send"
	}
	return "/" + url.PathEscape(key) + ".send"
}

// ServerChanURL is the address a SendKey posts to: Turbo keys go to
// sctapi.ftqq.com, Server Chan3 keys to the host of their own uid,
// https://<uid>.push.ft07.com.
func ServerChanURL(key string) string {
	if m := serverChan3Re.FindStringSubmatch(key); m != nil {
		return "https://" + m[1] + ".push.ft07.com" + serverChanPath(key)
	}
	return serverChanBase + serverChanPath(key)
}

// FeishuSign is the signature of a Feishu custom bot: HMAC-SHA256 keyed with
// "timestamp\nsecret" over an empty message, base64 encoded. timestamp is in seconds.
func FeishuSign(timestamp, secret string) string {
	h := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// DingTalkSign is the signature of a DingTalk custom bot: HMAC-SHA256 keyed
// with the secret over "timestamp\nsecret", base64 encoded. timestamp is in
// milliseconds. The caller URL-encodes the result when it goes into a query.
func DingTalkSign(timestamp, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(timestamp + "\n" + secret))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// requireScheme accepts https, and http too when allowHTTP is set. The value
// is not echoed: it may carry a token.
func requireScheme(raw string, allowHTTP bool) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "":
		return &DeliveryError{msg: "the address is not a valid URL"}
	case u.Scheme == "https", allowHTTP && u.Scheme == "http", u.Scheme == "http" && IsLoopback(u.Hostname()):
		return nil
	case allowHTTP:
		return &DeliveryError{msg: "the address must be an http(s) URL"}
	}
	return &DeliveryError{msg: "the address must be an https URL"}
}

// IsLoopback reports whether host is this machine: plain http to it never
// crosses a network, so it is accepted (a local relay, a test receiver).
func IsLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IsPlainHTTP reports whether raw is an http (not https) URL.
func IsPlainHTTP(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http"
}

func checkTelegram(body []byte) error {
	var a struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if json.Unmarshal(body, &a) != nil || a.OK {
		return nil
	}
	return &DeliveryError{msg: "telegram refused the message: " + shorten(a.Description)}
}

// checkErrCode builds a check for services that answer {"<codeKey>":0,...}
// on success. Codes in temporary are worth retrying (rate limits).
func checkErrCode(codeKey, msgKey string, temporary ...int) func([]byte) error {
	return func(body []byte) error {
		var m map[string]json.RawMessage
		if json.Unmarshal(body, &m) != nil {
			return nil // an answer that is not JSON: the status said success
		}
		raw, ok := m[codeKey]
		if !ok {
			return nil
		}
		var code int
		if json.Unmarshal(raw, &code) != nil {
			return nil
		}
		// Bark answers 200 on success; the others answer 0.
		if code == 0 || (codeKey == "code" && code == 200) {
			return nil
		}
		var msg string
		_ = json.Unmarshal(m[msgKey], &msg)
		de := &DeliveryError{msg: fmt.Sprintf("the service refused the message (code %d): %s", code, shorten(msg))}
		for _, c := range temporary {
			de.Temporary = de.Temporary || c == code
		}
		return de
	}
}

func shorten(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return truncRunes(s, 120)
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// headerText makes s fit an HTTP header value: no line breaks, and non-ASCII
// text as an RFC 2047 encoded word (ntfy decodes it).
func headerText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return mime.BEncoding.Encode("UTF-8", s)
}
