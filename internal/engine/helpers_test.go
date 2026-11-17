package engine

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

const testGUID = "11111111-2222-3333-4444-555555555555"

func nyLoc(t testing.TB) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// fakeClock never blocks: After records the wait and fires at once.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d > 0 {
		c.sleeps = append(c.sleeps, d)
	}
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

func (c *fakeClock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

type fakeAuth struct {
	mu        sync.Mutex
	tok       auth.Token
	err       error
	panicNext bool
	calls     int
	forced    []bool // per Token call: was a fresh token demanded
	forcedErr error  // when set, returned for forced calls only
}

func (a *fakeAuth) Token(ctx context.Context) (auth.Token, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.forced = append(a.forced, auth.Forced(ctx))
	if a.panicNext {
		a.panicNext = false
		panic("boom")
	}
	if a.forcedErr != nil && auth.Forced(ctx) {
		return auth.Token{}, a.forcedErr
	}
	return a.tok, a.err
}

func (a *fakeAuth) setErr(err error) {
	a.mu.Lock()
	a.err = err
	a.mu.Unlock()
}

func (a *fakeAuth) Login(context.Context, auth.LoginUI) (auth.Token, error) { return a.tok, nil }
func (a *fakeAuth) Logout(context.Context) error                            { return nil }

// graphHandler is the fake Graph: status picks the answer for a request path.
type graphHandler struct {
	mu     sync.Mutex
	status func(path string) (int, map[string]string)
	hits   []string
	// usersCode, when set, is the answer to the {user} lookup.
	usersCode int
}

func (h *graphHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.hits = append(h.hits, r.URL.Path)
	st, uc := h.status, h.usersCode
	h.mu.Unlock()
	if r.URL.Path == "/users" && r.URL.Query().Get("$select") == "id" {
		if uc != 0 {
			w.WriteHeader(uc)
			return
		}
		_, _ = w.Write([]byte(`{"value":[{"id":"uid-1"},{"id":"uid-2"}]}`))
		return
	}
	code, hdr := 200, map[string]string(nil)
	if st != nil {
		code, hdr = st(r.URL.Path)
	}
	for k, v := range hdr {
		w.Header().Set(k, v)
	}
	w.WriteHeader(code)
	if code == 200 {
		_, _ = w.Write([]byte(`{"value":[]}`))
	}
}

func (h *graphHandler) setStatus(f func(string) (int, map[string]string)) {
	h.mu.Lock()
	h.status = f
	h.mu.Unlock()
}

func (h *graphHandler) count(sub string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, p := range h.hits {
		if strings.Contains(p, sub) {
			n++
		}
	}
	return n
}

func (h *graphHandler) total() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.hits)
}

type handlerRT struct{ h http.Handler }

func (rt handlerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rt.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

type fixture struct {
	t     *testing.T
	clock *fakeClock
	dir   string
	h     *graphHandler
	srv   *httptest.Server
	auths map[string]*fakeAuth
	cfg   *config.Config
	opts  Options
	e     *Engine
}

var delegatedScopes = []string{"User.Read", "Mail.Read", "Calendars.Read", "Contacts.Read", "Tasks.Read"}

func delegatedAcct(name string) config.Account {
	return config.Account{Name: name, Auth: config.AuthDeviceCode, Tenant: "organizations", ClientID: testGUID}
}

// newFixture builds an engine for the given accounts at Tue 2026-11-17 10:00 New York.
func newFixture(t *testing.T, accts ...config.Account) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir(), h: &graphHandler{}, auths: map[string]*fakeAuth{}}
	f.clock = &fakeClock{now: time.Date(2026, 11, 17, 10, 0, 0, 0, nyLoc(t))}
	f.srv = httptest.NewServer(f.h)
	t.Cleanup(f.srv.Close)

	f.cfg = config.Default()
	f.cfg.Accounts = accts
	for _, a := range accts {
		f.auths[a.Name] = &fakeAuth{tok: auth.Token{
			AccessToken: "secret-token-123", Account: "alice@example.com",
			Scopes: delegatedScopes, ExpiresOn: f.clock.now.Add(time.Hour),
		}}
	}
	f.opts = DefaultOptions()
	f.opts.Config = f.cfg
	f.opts.DataDir = f.dir
	f.opts.Clock = f.clock
	f.opts.CallGapMin, f.opts.CallGapMax = 0, 0
	f.opts.Seed = 42
	f.opts.Tick = 10 * time.Minute // jump threshold 30 minutes
	f.opts.LockWait = 50 * time.Millisecond
	f.opts.Graph = &graph.Client{HTTP: f.srv.Client(), Base: f.srv.URL}
	f.opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	f.opts.NewAuth = func(a config.Account) (auth.Authenticator, error) { return f.auths[a.Name], nil }
	return f
}

// start creates the engine (after any option tweaks) and runs init.
func (f *fixture) start() *Engine {
	f.t.Helper()
	e, err := New(f.opts)
	if err != nil {
		f.t.Fatal(err)
	}
	e.init()
	f.e = e
	return e
}

// tickWait runs one tick and waits for the rounds it started.
func (f *fixture) tickWait() {
	f.e.tick(context.Background())
	f.e.wait()
}

// round forces the account's next round to be due, then runs it.
func (f *fixture) round(name string) {
	f.t.Helper()
	f.clock.Advance(time.Minute)
	f.e.updateAccount(name, func(as *store.AccountState) {
		if as.Status == store.StatusOK || as.Status == store.StatusNeedsAttention {
			as.NextRun = f.clock.Now().Add(-time.Second)
		}
	})
	f.tickWait()
}

func (f *fixture) acct(name string) store.AccountState {
	f.e.mu.Lock()
	defer f.e.mu.Unlock()
	return *f.e.state.Accounts[name]
}

func (f *fixture) entries(account string) []store.Entry {
	f.t.Helper()
	es, err := store.New(f.dir).Read(store.Filter{Account: account})
	if err != nil {
		f.t.Fatal(err)
	}
	return es
}

func (f *fixture) lastRoundEnd(account string) store.Entry {
	f.t.Helper()
	es := f.entries(account)
	for i := len(es) - 1; i >= 0; i-- {
		if es[i].Event == "round_end" {
			return es[i]
		}
	}
	f.t.Fatal("no round_end entry")
	return store.Entry{}
}

func (f *fixture) hasEvent(name string) bool {
	es, _ := store.New(f.dir).Read(store.Filter{})
	for _, e := range es {
		if e.Event == name {
			return true
		}
	}
	return false
}

// jump moves the clock by a long span and lets the engine see the jump.
func (f *fixture) jump(d time.Duration) {
	f.t.Helper()
	f.clock.Advance(d)
	f.tickWait()
}
