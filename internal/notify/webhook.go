package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/version"
)

// ErrNotConfigured is returned by a URL source when no webhook is set.
var ErrNotConfigured = errors.New("no webhook configured")

// Defaults for Webhook.
const (
	DefaultTimeout = 10 * time.Second
	maxRetries     = 3
)

// DefaultBackoff are the waits before the three retries.
var DefaultBackoff = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

// Webhook posts events as JSON to an address that is looked up on every send,
// so a change made with `notify set-webhook` reaches a running engine.
type Webhook struct {
	// URL returns the current address, or ErrNotConfigured.
	URL func() (string, error)
	// HTTP is the client to use; nil means http.DefaultClient. Redirects are
	// never followed: they could send the alert somewhere else.
	HTTP *http.Client
	// Configured, when set, answers Enabled cheaply (no decryption); without
	// it Enabled resolves the address itself.
	Configured func() bool
	Timeout    time.Duration   // per attempt; DefaultTimeout when zero
	Backoff    []time.Duration // waits before each retry; DefaultBackoff when nil
	Log        *slog.Logger
}

// Enabled reports whether a webhook is set (or could not be read, which
// Send reports).
func (w *Webhook) Enabled() bool {
	if w.Configured != nil {
		return w.Configured()
	}
	_, err := w.URL()
	return !errors.Is(err, ErrNotConfigured)
}

// Send delivers ev, retrying temporary failures with exponential backoff.
func (w *Webhook) Send(ctx context.Context, ev Event) error {
	raw, err := w.URL()
	if err != nil {
		return fmt.Errorf("webhook address unavailable: %w", err)
	}
	backoff := w.Backoff
	if backoff == nil {
		backoff = DefaultBackoff
	}
	return withRetry(ctx, backoff, w.Log, "webhook", ev.Event, func() error { return w.deliver(ctx, raw, ev) })
}

// withRetry runs try until it succeeds, fails for good, or the retries are
// used up; temporary failures wait for the next backoff step first.
func withRetry(ctx context.Context, backoff []time.Duration, log *slog.Logger, label, event string, try func() error) error {
	var last error
	for attempt := 0; ; attempt++ {
		var de *DeliveryError
		last = try()
		if last == nil {
			return nil
		}
		if !errors.As(last, &de) || !de.Temporary || attempt >= maxRetries || attempt >= len(backoff) {
			return last
		}
		if log != nil {
			log.Warn(label+" delivery failed; will retry", "event", event, "attempt", attempt+1, "err", last.Error())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff[attempt]):
		}
	}
}

// DeliveryError describes a failed attempt. Its text never contains the address.
type DeliveryError struct {
	Status    int // HTTP status; 0 for transport errors
	Temporary bool
	msg       string
}

func (e *DeliveryError) Error() string { return e.msg }

func (w *Webhook) deliver(ctx context.Context, raw string, ev Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return &DeliveryError{msg: "encode event: " + err.Error()}
	}
	return doRequest(ctx, w.HTTP, w.Timeout, request{
		method: http.MethodPost, url: raw, body: body, label: "webhook",
		header: map[string]string{"Content-Type": "application/json"},
		secret: []string{raw},
	})
}

// request is one HTTP call to a destination.
type request struct {
	method, url string
	header      map[string]string
	body        []byte
	label       string   // names the destination in errors: "webhook", "telegram", ...
	secret      []string // values that must never appear in an error: the address, tokens
	// check inspects the body of a 2xx answer; some services report failure
	// there with status 200. It is optional.
	check func(body []byte) error
}

// doRequest sends r once. It never follows redirects, and no error text
// contains the address or any value listed in r.secret.
func doRequest(ctx context.Context, hc *http.Client, timeout time.Duration, r request) error {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	clean := func(s string) string {
		for _, raw := range r.secret {
			s = redact(s, raw)
		}
		return s
	}
	req, err := http.NewRequestWithContext(ctx, r.method, r.url, bytes.NewReader(r.body))
	if err != nil {
		return &DeliveryError{msg: "invalid " + r.label + " address: " + clean(err.Error())}
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", "e5extend/"+version.Version)

	if hc == nil {
		hc = http.DefaultClient
	}
	cp := *hc
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := cp.Do(req)
	if err != nil {
		return &DeliveryError{Temporary: true, msg: "send failed: " + clean(transportReason(err))}
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	switch s := resp.StatusCode; {
	case s >= 200 && s < 300:
		if r.check != nil {
			if err := r.check(answer); err != nil {
				var de *DeliveryError
				if errors.As(err, &de) {
					de.msg = clean(de.msg)
					de.Status = s
				}
				return err
			}
		}
		return nil
	case s == http.StatusRequestTimeout, s == http.StatusTooManyRequests, s >= 500:
		return &DeliveryError{Status: s, Temporary: true, msg: fmt.Sprintf("%s answered HTTP %d", r.label, s)}
	case s >= 300 && s < 400:
		return &DeliveryError{Status: s, msg: fmt.Sprintf("%s answered HTTP %d (redirects are not followed; use the final address)", r.label, s)}
	default:
		// The reason the service gives (Telegram's "description", ...) is what
		// tells a wrong chat id from a wrong token. It is redacted like any error.
		msg := fmt.Sprintf("%s answered HTTP %d", r.label, s)
		if snip := snippet(answer); snip != "" {
			msg += ": " + clean(snip)
		}
		return &DeliveryError{Status: s, msg: msg}
	}
}

// snippet is a short one-line excerpt of an error answer: the text of a JSON
// description/message/error field when there is one, else the start of the body.
func snippet(body []byte) string {
	var m map[string]any
	text := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &m) == nil {
		for _, k := range []string{"description", "message", "errmsg", "error", "msg"} {
			if v, ok := m[k].(string); ok && v != "" {
				text = v
				break
			}
		}
	}
	if strings.HasPrefix(text, "<") { // an HTML error page says nothing useful
		return ""
	}
	return shorten(text)
}

// transportReason strips the request URL that net/http puts into its errors.
func transportReason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if errors.Is(ue.Err, context.DeadlineExceeded) {
			return "timed out"
		}
		return ue.Err.Error()
	}
	return err.Error()
}

// redact removes every part of the address from s: the whole address, its
// host and its path and query, any of which may identify or authorize it.
func redact(s, raw string) string {
	parts := []string{raw}
	if u, err := url.Parse(raw); err == nil {
		parts = append(parts, u.Host, u.Hostname(), u.RawQuery, u.RawPath, u.Path, u.User.String())
		// Tokens sit in single path segments and query values; they may be echoed alone.
		parts = append(parts, strings.Split(u.Path, "/")...)
		for _, vs := range u.Query() {
			parts = append(parts, vs...)
		}
		if pw, ok := u.User.Password(); ok {
			parts = append(parts, pw)
		}
	}
	for _, p := range parts {
		if p != "" && p != "/" {
			s = strings.ReplaceAll(s, p, "<webhook>")
		}
	}
	return s
}
