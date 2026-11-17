package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const secretPath = "/services/T000/B000/SECRETTOKEN123"

type recv struct {
	mu     sync.Mutex
	bodies [][]byte
	ctype  string
	hits   atomic.Int32
	codes  []int // answers in order; the last repeats
}

func (r *recv) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	n := int(r.hits.Add(1))
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.bodies = append(r.bodies, b)
	r.ctype = req.Header.Get("Content-Type")
	code := 200
	if len(r.codes) > 0 {
		code = r.codes[min(n-1, len(r.codes)-1)]
	}
	r.mu.Unlock()
	w.WriteHeader(code)
}

func newHook(t *testing.T, codes ...int) (*Webhook, *recv, string) {
	t.Helper()
	rc := &recv{codes: codes}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)
	url := srv.URL + secretPath + "?token=QUERYSECRET"
	return &Webhook{URL: func() (string, error) { return url, nil }, Backoff: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}}, rc, url
}

var ev = Event{Event: NeedsAttention, Account: "main", Message: "login required", Time: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}

func TestPayloadShape(t *testing.T) {
	w, rc, _ := newHook(t)
	if err := w.Send(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(rc.bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"event": "needs_attention", "account": "main", "message": "login required",
		"time": "2026-11-17T10:00:00Z", "text": "[e5extend] main: login required", "content": "[e5extend] main: login required"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if rc.ctype != "application/json" {
		t.Errorf("content type %q", rc.ctype)
	}
	if (Event{Message: "hello"}).Text() != "[e5extend] hello" {
		t.Error("text without account")
	}
}

func TestRetryThenSuccess(t *testing.T) {
	w, rc, _ := newHook(t, 500, 429, 200)
	if err := w.Send(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if rc.hits.Load() != 3 {
		t.Errorf("attempts = %d", rc.hits.Load())
	}
}

func TestGivesUpAfterThreeRetries(t *testing.T) {
	w, rc, url := newHook(t, 503)
	err := w.Send(context.Background(), ev)
	if err == nil || rc.hits.Load() != 4 {
		t.Fatalf("err=%v attempts=%d", err, rc.hits.Load())
	}
	assertNoSecret(t, err.Error(), url)
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	w, rc, _ := newHook(t, 404)
	if err := w.Send(context.Background(), ev); err == nil || rc.hits.Load() != 1 {
		t.Fatalf("err=%v attempts=%d", err, rc.hits.Load())
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	w := &Webhook{URL: func() (string, error) { return srv.URL + secretPath, nil }, Backoff: []time.Duration{}}
	err := w.Send(context.Background(), ev)
	var de *DeliveryError
	if !errors.As(err, &de) || de.Status != 307 || elsewhere.Load() != 0 {
		t.Errorf("err=%v elsewhere=%d", err, elsewhere.Load())
	}
}

func TestTransportErrorHidesAddress(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + secretPath + "?token=QUERYSECRET"
	srv.Close() // nothing listens any more
	var logs bytes.Buffer
	w := &Webhook{URL: func() (string, error) { return url, nil }, Backoff: []time.Duration{time.Millisecond},
		Log: slog.New(slog.NewTextHandler(&logs, nil))}
	err := w.Send(context.Background(), ev)
	if err == nil {
		t.Fatal("expected an error")
	}
	assertNoSecret(t, err.Error(), url)
	assertNoSecret(t, logs.String(), url)
	if logs.Len() == 0 {
		t.Error("retry was not logged")
	}
}

func TestTimeoutHidesAddress(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	url := srv.URL + secretPath
	w := &Webhook{URL: func() (string, error) { return url, nil }, Timeout: 30 * time.Millisecond, Backoff: []time.Duration{}}
	err := w.Send(context.Background(), ev)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	assertNoSecret(t, err.Error(), url)
}

func TestInvalidAddressErrorHidesAddress(t *testing.T) {
	bad := "http://exa mple.com/hook/SECRETTOKEN123"
	w := &Webhook{URL: func() (string, error) { return bad, nil }}
	err := w.Send(context.Background(), ev)
	if err == nil {
		t.Fatal("expected an error")
	}
	assertNoSecret(t, err.Error(), bad)
}

func TestCancelStopsRetrying(t *testing.T) {
	w, _, _ := newHook(t, 500)
	w.Backoff = []time.Duration{time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	if err := w.Send(ctx, ev); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v after %s", err, time.Since(start))
	}
}

func TestEnabled(t *testing.T) {
	w := &Webhook{URL: func() (string, error) { return "", ErrNotConfigured }}
	if w.Enabled() {
		t.Error("enabled without address")
	}
	w.URL = func() (string, error) { return "http://x", nil }
	if !w.Enabled() {
		t.Error("disabled with address")
	}
	w.URL = func() (string, error) { return "", errors.New("keyring locked") }
	if !w.Enabled() {
		t.Error("an unreadable address must surface as errors, not silence")
	}
	if err := w.Send(context.Background(), ev); err == nil {
		t.Error("send with unreadable address succeeded")
	}
}

func assertNoSecret(t *testing.T, text, url string) {
	t.Helper()
	for _, s := range []string{"SECRETTOKEN123", "QUERYSECRET", url, "127.0.0.1"} {
		if strings.Contains(text, s) {
			t.Errorf("text leaks %q: %s", s, text)
		}
	}
}

// ---- dispatcher ----

type fakeSender struct {
	failFirst bool
	mu        sync.Mutex
	got       []Event
	err       error
	hold      chan struct{}
}

func (f *fakeSender) Enabled() bool { return true }
func (f *fakeSender) Send(ctx context.Context, e Event) error {
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, e)
	if f.failFirst && len(f.got) == 1 {
		return errors.New("down")
	}
	return f.err
}

func TestDispatcherDeliversAndFlushesOnClose(t *testing.T) {
	fs := &fakeSender{}
	d := NewDispatcher(fs, nil)
	for i := 0; i < 10; i++ {
		d.Send(ev, nil)
	}
	d.Close()
	d.Send(ev, nil) // after Close: dropped, no panic
	d.Close()
	if len(fs.got) != 10 {
		t.Errorf("delivered %d", len(fs.got))
	}
}

func TestDispatcherDoesNotBlockAndAbandonsOnClose(t *testing.T) {
	fs := &fakeSender{hold: make(chan struct{})}
	var logs bytes.Buffer
	d := NewDispatcher(fs, slog.New(slog.NewTextHandler(&logs, nil)))
	d.CloseWait = 50 * time.Millisecond
	start := time.Now()
	for i := 0; i < 500; i++ { // far more than the queue holds
		d.Send(ev, nil)
	}
	if time.Since(start) > time.Second {
		t.Fatal("Send blocked")
	}
	d.Close()
	if time.Since(start) > 5*time.Second {
		t.Fatal("Close did not give up")
	}
	if !strings.Contains(logs.String(), "queue is full") {
		t.Error("overflow not logged")
	}
}

func TestDispatcherLogsFailureWithoutAddress(t *testing.T) {
	w, _, url := newHook(t, 400)
	var logs bytes.Buffer
	d := NewDispatcher(w, slog.New(slog.NewJSONHandler(&logs, nil)))
	d.Send(ev, nil)
	d.Close()
	if !strings.Contains(logs.String(), "not delivered") {
		t.Errorf("failure not logged: %s", logs.String())
	}
	assertNoSecret(t, logs.String(), url)
}

func TestDispatcherReportsResultsToCallbacks(t *testing.T) {
	fs := &fakeSender{failFirst: true}
	d := NewDispatcher(fs, nil)
	var mu sync.Mutex
	var results []error
	d.Send(ev, func(err error) { mu.Lock(); results = append(results, err); mu.Unlock() })
	d.Send(ev, func(err error) { mu.Lock(); results = append(results, err); mu.Unlock() })
	d.Close()
	if len(results) != 2 || results[0] == nil || results[1] != nil {
		t.Errorf("results = %v", results)
	}
}

type blockingSender struct{ release chan struct{} }

func (b blockingSender) Enabled() bool                     { <-b.release; return true }
func (b blockingSender) Send(context.Context, Event) error { return nil }

// A keyring that hangs must not delay whoever asks Enabled (the engine's tick).
func TestEnabledNeverBlocks(t *testing.T) {
	b := blockingSender{release: make(chan struct{})}
	d := NewDispatcher(b, nil)
	start := time.Now()
	for i := 0; i < 1000; i++ {
		if d.Enabled() {
			t.Fatal("enabled before the sender answered")
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("Enabled blocked")
	}
	d.CloseWait = 50 * time.Millisecond
	closed := make(chan struct{})
	go func() { d.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waits for a hanging Enabled")
	}
	close(b.release)
}

func TestEnabledIsRefreshedByTTL(t *testing.T) {
	var on atomic.Bool
	s := &toggleSender{on: &on}
	d := NewDispatcherTTL(s, nil, 10*time.Millisecond)
	defer d.Close()
	on.Store(true)
	deadline := time.Now().Add(3 * time.Second)
	for !d.Enabled() {
		if time.Now().After(deadline) {
			t.Fatal("Enabled never became true")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type toggleSender struct{ on *atomic.Bool }

func (t *toggleSender) Enabled() bool                     { return t.on.Load() }
func (t *toggleSender) Send(context.Context, Event) error { return nil }
