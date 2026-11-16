package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-renew/internal/auth"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/notify"
)

type fakeNotifier struct {
	mu       sync.Mutex
	disabled bool
	failing  bool // deliveries fail
	events   []notify.Event
	closed   int
}

func (n *fakeNotifier) Enabled() bool { return !n.disabled }
func (n *fakeNotifier) Send(e notify.Event, done func(error)) {
	n.mu.Lock()
	n.events = append(n.events, e)
	fail := n.failing
	n.mu.Unlock()
	if done != nil {
		if fail {
			done(fmt.Errorf("webhook down"))
		} else {
			done(nil)
		}
	}
}
func (n *fakeNotifier) Close() { n.mu.Lock(); n.closed++; n.mu.Unlock() }
func (n *fakeNotifier) kinds(account string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var k []string
	for _, e := range n.events {
		if account == "" || e.Account == account {
			k = append(k, e.Event)
		}
	}
	return k
}

func TestAlertsFollowStateChanges(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	n := &fakeNotifier{}
	f.opts.Notifier = n
	f.start()

	f.round("main")
	if k := n.kinds(""); len(k) != 0 {
		t.Fatalf("healthy round alerted: %v", k)
	}
	f.auths["main"].setErr(fmt.Errorf("x: %w", auth.ErrLoginRequired))
	f.round("main")
	f.round("main") // same problem again
	if k := n.kinds("main"); len(k) != 1 || k[0] != "needs_attention" {
		t.Fatalf("alerts = %v", k)
	}
	if _, ok := f.acct("main").Notified["needs_attention"]; !ok {
		t.Error("announcement not recorded in the state")
	}
	f.auths["main"].setErr(nil)
	f.round("main")
	if k := n.kinds("main"); len(k) != 2 || k[1] != "recovered" {
		t.Fatalf("alerts = %v", k)
	}
	if !f.hasEvent("alert") {
		t.Error("alerts are not in the call log")
	}
}

func TestAlertStateSurvivesRestart(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	n := &fakeNotifier{}
	f.opts.Notifier = n
	f.start()
	f.auths["main"].setErr(fmt.Errorf("x: %w", auth.ErrLoginRequired))
	f.round("main")
	f.start() // a new engine reads the same state
	f.round("main")
	if k := n.kinds("main"); len(k) != 1 {
		t.Errorf("restart repeated the alert: %v", k)
	}
}

func TestNoDestinationMeansNothingRecorded(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	n := &fakeNotifier{disabled: true}
	f.opts.Notifier = n
	f.start()
	f.auths["main"].setErr(fmt.Errorf("x: %w", auth.ErrLoginRequired))
	f.round("main")
	if len(n.kinds("")) != 0 || len(f.acct("main").Notified) != 0 {
		t.Fatalf("alerts=%v notified=%v", n.kinds(""), f.acct("main").Notified)
	}
	// A destination added later still hears about the standing problem.
	n.disabled = false
	f.round("main")
	if k := n.kinds("main"); len(k) != 1 {
		t.Errorf("alerts = %v", k)
	}
}

func TestAlertEventFilterFromConfig(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.cfg.Notify.Events = []string{"recovered"}
	n := &fakeNotifier{}
	f.opts.Notifier = n
	f.start()
	f.auths["main"].setErr(fmt.Errorf("x: %w", auth.ErrLoginRequired))
	f.round("main")
	f.auths["main"].setErr(nil)
	f.round("main")
	if k := n.kinds("main"); len(k) != 1 || k[0] != "recovered" {
		t.Errorf("alerts = %v", k)
	}
}

func TestAutoPauseAlert(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	n := &fakeNotifier{}
	f.opts.Notifier = n
	f.h.setStatus(func(string) (int, map[string]string) { return 500, nil })
	f.start()
	for i := 0; i < AutoPauseRounds; i++ {
		f.round("main")
	}
	if k := n.kinds("main"); len(k) != 1 || k[0] != "auto_paused" {
		t.Errorf("alerts = %v", k)
	}
}

func TestHooksAndNotifierClosedOnRun(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	n := &fakeNotifier{}
	f.opts.Notifier = n
	var mu sync.Mutex
	var order []string
	ctx, cancel := context.WithCancel(context.Background())
	ticks := 0
	f.opts.Hooks = Hooks{
		Ready: func() { mu.Lock(); order = append(order, "ready"); mu.Unlock() },
		Tick: func() {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, "tick")
			if ticks++; ticks == 3 {
				cancel()
			}
		},
		Stopping: func() { mu.Lock(); order = append(order, "stopping"); mu.Unlock() },
	}
	e, err := New(f.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	// The fake clock fires at once, so a few more ticks may run before the
	// loop sees the cancelled context.
	if len(order) < 5 || order[0] != "ready" || order[len(order)-1] != "stopping" {
		t.Fatalf("hook order = %v", order)
	}
	for _, h := range order[1 : len(order)-1] {
		if h != "tick" {
			t.Errorf("hook order = %v", order)
		}
	}
	if n.closed != 1 {
		t.Errorf("notifier closed %d times", n.closed)
	}
}

func TestPanickingHookDoesNotStopEngine(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.opts.Hooks.Tick = func() { panic("hook") }
	f.start()
	f.round("main")
	if f.acct("main").Totals.Rounds == 0 {
		t.Error("round did not run")
	}
}

// TestYearWithAlerts runs the year simulation with alerts on: a ten day
// sign-in outage for one account and a client secret that expires. The number
// of alerts must be small and none may repeat needlessly.
func TestYearWithAlerts(t *testing.T) {
	ny := nyLoc(t)
	n := &fakeNotifier{}
	s := newSim(t, time.Date(2026, 1, 1, 0, 0, 0, 0, ny), func(c *config.Config) {
		c.Accounts[1].SecretExpires = "2026-09-15"
	}, func(o *Options) { o.Notifier = n })
	f := s.f
	start := f.clock.Now()
	s.runUntil(start.AddDate(0, 0, 365), func(day int) {
		switch day {
		case 100:
			f.auths["alpha"].setErr(fmt.Errorf("x: %w", auth.ErrLoginRequired))
		case 110:
			f.auths["alpha"].setErr(nil)
		}
	})

	count := func(acct, kind string) int {
		c := 0
		for _, e := range n.events {
			if e.Account == acct && e.Event == kind {
				c++
			}
		}
		return c
	}
	if got := count("alpha", "needs_attention"); got != 1 {
		t.Errorf("needs_attention = %d", got)
	}
	if got := count("alpha", "recovered"); got != 1 {
		t.Errorf("recovered = %d", got)
	}
	if got := count("alpha", "stale"); got < 1 || got > 10 {
		t.Errorf("stale = %d", got)
	}
	if got := count("beta", "secret_expiring"); got != 4 {
		t.Errorf("secret_expiring = %d", got)
	}
	if len(n.events) > 20 {
		t.Errorf("%d alerts in a year", len(n.events))
	}
	var lastStale time.Time
	for _, e := range n.events {
		if e.Event == "stale" {
			if !lastStale.IsZero() && e.Time.Sub(lastStale) < 24*time.Hour {
				t.Errorf("stale reminders %s apart", e.Time.Sub(lastStale))
			}
			lastStale = e.Time
		}
	}
	for _, name := range []string{"alpha", "beta"} {
		if as := f.acct(name); len(as.Notified) > 6 {
			t.Errorf("%s: notified map has %d entries", name, len(as.Notified))
		}
	}
}

func TestAlertsAreRetriedUntilDelivered(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	n := &fakeNotifier{failing: true}
	f.opts.Notifier = n
	f.start()
	f.auths["main"].setErr(fmt.Errorf("x: %w", auth.ErrLoginRequired))
	f.round("main")
	if got := f.acct("main").Notified["needs_attention"]; !got.Pending {
		t.Fatalf("failed delivery not kept pending: %+v", got)
	}
	// Ticks within the hour do not resend; later ones do, once per hour.
	for i := 0; i < 5; i++ {
		f.clock.Advance(10 * time.Minute)
		f.tickWait()
	}
	if k := n.kinds("main"); len(k) != 1 {
		t.Fatalf("resent too early: %v", k)
	}
	f.clock.Advance(2 * time.Hour)
	f.tickWait()
	if k := n.kinds("main"); len(k) != 2 {
		t.Fatalf("not retried: %v", k)
	}
	// The webhook is back.
	n.mu.Lock()
	n.failing = false
	n.mu.Unlock()
	f.clock.Advance(2 * time.Hour)
	f.tickWait()
	if got := f.acct("main").Notified["needs_attention"]; got.Pending {
		t.Fatalf("delivery not recorded: %+v", got)
	}
	before := len(n.kinds("main"))
	for i := 0; i < 20; i++ {
		f.clock.Advance(30 * time.Minute)
		f.tickWait()
	}
	if len(n.kinds("main")) != before {
		t.Errorf("alerts repeated after delivery: %v", n.kinds("main"))
	}
}
