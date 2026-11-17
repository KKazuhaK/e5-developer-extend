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
	var last error
	for attempt := 0; ; attempt++ {
		var de *DeliveryError
		last = w.deliver(ctx, raw, ev)
		if last == nil {
			return nil
		}
		if !errors.As(last, &de) || !de.Temporary || attempt >= maxRetries || attempt >= len(backoff) {
			return last
		}
		if w.Log != nil {
			w.Log.Warn("webhook delivery failed; will retry", "event", ev.Event, "attempt", attempt+1, "err", last.Error())
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
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, raw, bytes.NewReader(body))
	if err != nil {
		return &DeliveryError{msg: "invalid webhook address: " + redact(err.Error(), raw)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "e5extend/"+version.Version)

	hc := http.DefaultClient
	if w.HTTP != nil {
		hc = w.HTTP
	}
	cp := *hc
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := cp.Do(req)
	if err != nil {
		return &DeliveryError{Temporary: true, msg: "send failed: " + redact(transportReason(err), raw)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	switch s := resp.StatusCode; {
	case s >= 200 && s < 300:
		return nil
	case s == http.StatusRequestTimeout, s == http.StatusTooManyRequests, s >= 500:
		return &DeliveryError{Status: s, Temporary: true, msg: fmt.Sprintf("webhook answered HTTP %d", s)}
	case s >= 300 && s < 400:
		return &DeliveryError{Status: s, msg: fmt.Sprintf("webhook answered HTTP %d (redirects are not followed; use the final address)", s)}
	default:
		return &DeliveryError{Status: s, msg: fmt.Sprintf("webhook answered HTTP %d", s)}
	}
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
	}
	for _, p := range parts {
		if len(p) >= 3 && p != "/" {
			s = strings.ReplaceAll(s, p, "<webhook>")
		}
	}
	return s
}
