package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// pausedFixture runs an account into the automatic pause and returns the time of the pause.
func pausedFixture(t *testing.T, n *fakeNotifier) (*fixture, time.Time) {
	t.Helper()
	f := newFixture(t, delegatedAcct("main"))
	if n != nil {
		f.opts.Notifier = n
	}
	f.cfg.Schedule.WorkStart, f.cfg.Schedule.WorkEnd = "00:00", "00:00" // all day; the window has its own test
	f.h.setStatus(func(string) (int, map[string]string) { return 500, nil })
	f.start()
	for i := 0; i < AutoPauseRounds; i++ {
		f.round("main")
	}
	if as := f.acct("main"); as.Status != store.StatusPaused {
		t.Fatalf("not paused: %+v", as)
	}
	return f, f.clock.Now()
}

func TestAutoResumeProbeSchedule(t *testing.T) {
	f, pausedAt := pausedFixture(t, nil)
	if got := f.acct("main").NextProbe; !got.Equal(pausedAt.Add(6 * time.Hour)) {
		t.Fatalf("first probe at %s, want pause + 6h (%s)", got, pausedAt.Add(6*time.Hour))
	}
	base := f.h.total()
	f.clock.Set(pausedAt.Add(6*time.Hour - time.Minute))
	f.tickWait()
	if f.h.total() != base {
		t.Fatal("probed before 6h")
	}

	// Each failed probe is exactly one call and moves the next one out: 12h, then 24h, then 24h.
	want := []time.Duration{12 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	at := pausedAt.Add(6 * time.Hour)
	for i, gap := range want {
		f.clock.Set(at)
		f.tickWait()
		if got := f.h.total(); got != base+i+1 {
			t.Fatalf("probe %d made %d calls", i+1, got-base-i)
		}
		as := f.acct("main")
		if as.Status != store.StatusPaused || !as.NextProbe.Equal(at.Add(gap)) || as.Probes != i+1 {
			t.Fatalf("after probe %d: %+v, want next at %s", i+1, as, at.Add(gap))
		}
		at = at.Add(gap)
	}
	// Between probes nothing runs.
	f.clock.Set(at.Add(-time.Hour))
	f.tickWait()
	if got := f.h.total(); got != base+3 {
		t.Errorf("extra calls between probes: %d", got-base)
	}
}

func TestAutoResumeOnSuccessfulProbeSendsRecovered(t *testing.T) {
	n := &fakeNotifier{}
	f, pausedAt := pausedFixture(t, n)
	if k := n.kinds("main"); len(k) != 1 || k[0] != notify.AutoPaused {
		t.Fatalf("alerts after the pause: %v", k)
	}
	f.h.setStatus(nil)
	f.clock.Set(pausedAt.Add(6 * time.Hour))
	f.tickWait()
	as := f.acct("main")
	if as.Status != store.StatusOK || as.Reason != "" || as.ConsecutiveFailedRound != 0 || !as.NextProbe.IsZero() || as.Probes != 0 {
		t.Fatalf("not resumed: %+v", as)
	}
	inRange(t, "next round", as.NextRun.Sub(f.clock.now), time.Minute, 5*time.Minute)
	if k := n.kinds("main"); len(k) != 2 || k[1] != notify.Recovered {
		t.Errorf("alerts: %v, want auto_paused then recovered", k)
	}
	if !f.hasEvent("auto_resume") {
		t.Error("no auto_resume event")
	}
	// The account runs rounds again.
	f.clock.Advance(6 * time.Minute)
	f.tickWait()
	if f.acct("main").Totals.Rounds != AutoPauseRounds+1 {
		t.Error("resumed account did not run a round")
	}
}

func TestAutoResumeNeverForManualPause(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.cfg.Accounts[0].Paused = true
	f.start()
	f.clock.Advance(48 * time.Hour)
	f.tickWait()
	if f.h.total() != 0 || f.auths["main"].calls != 0 {
		t.Errorf("a manually paused account was probed (%d calls, %d tokens)", f.h.total(), f.auths["main"].calls)
	}
}

func TestProbeSignInFailureNeedsAttention(t *testing.T) {
	n := &fakeNotifier{}
	f, pausedAt := pausedFixture(t, n)
	f.auths["main"].setErr(fmt.Errorf("x: %w", auth.ErrLoginRequired))
	f.clock.Set(pausedAt.Add(6 * time.Hour))
	f.tickWait()
	as := f.acct("main")
	if as.Status != store.StatusNeedsAttention || !strings.Contains(as.Reason, "login required") || !as.NextProbe.IsZero() {
		t.Fatalf("got %+v", as)
	}
	calls := f.h.total()
	// No more probing: only the attention retry, which does a normal round.
	f.clock.Advance(2 * time.Hour)
	f.tickWait()
	if f.h.total() != calls {
		t.Error("probed an account that needs attention")
	}
}

func TestProbeNetworkErrorDoesNotCount(t *testing.T) {
	f, pausedAt := pausedFixture(t, nil)
	f.auths["main"].setErr(fmt.Errorf("x: %w", auth.ErrTransient))
	f.clock.Set(pausedAt.Add(6 * time.Hour))
	f.tickWait()
	as := f.acct("main")
	if as.Status != store.StatusPaused || as.Probes != 0 {
		t.Fatalf("got %+v", as)
	}
	inRange(t, "retry", as.NextProbe.Sub(f.clock.now), probeRetryMin, probeRetryMax)
}

func TestManualResumeClearsProbe(t *testing.T) {
	f, _ := pausedFixture(t, nil)
	f.h.setStatus(nil)
	if err := TriggerResume(f.dir, "main"); err != nil {
		t.Fatal(err)
	}
	f.tickWait()
	if as := f.acct("main"); as.Status != store.StatusOK || !as.NextProbe.IsZero() {
		t.Fatalf("got %+v", as)
	}
}

func TestPausedStateWithoutProbeTimeGetsOne(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	st := store.NewState()
	st.Accounts["main"] = &store.AccountState{Status: store.StatusPaused, Reason: "old"}
	_ = store.New(f.dir).SaveState(st)
	f.start()
	f.tickWait()
	if got := f.acct("main").NextProbe; !got.Equal(f.clock.now.Add(probeFirst)) {
		t.Errorf("probe time %s", got)
	}
}

func TestLongOverdueRunsOneRoundSoon(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"), delegatedAcct("other"))
	st := store.NewState()
	for _, name := range []string{"main", "other"} {
		st.Accounts[name] = &store.AccountState{Status: store.StatusOK, NextRun: f.clock.now.Add(-72 * time.Hour), Totals: store.Totals{Rounds: 9}}
	}
	_ = store.New(f.dir).SaveState(st)
	f.start()
	if !f.hasEvent("catch_up") {
		t.Error("no catch_up event")
	}
	for _, name := range []string{"main", "other"} {
		inRange(t, name+" next round", f.acct(name).NextRun.Sub(f.clock.now), time.Minute, 5*time.Minute)
	}
	// Different accounts get different (jittered) times, and exactly one round each follows.
	f.tickWait()
	if f.h.total() != 0 {
		t.Fatal("round ran before its time")
	}
	for i := 0; i < 8; i++ {
		f.clock.Advance(time.Minute)
		f.tickWait()
	}
	for _, name := range []string{"main", "other"} {
		if got := f.acct(name).Totals.Rounds; got != 10 {
			t.Errorf("%s: %d rounds, want exactly one catch-up round", name, got-9)
		}
	}
}

func TestAutoPauseOffKeepsRunning(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.cfg.Schedule.AutoPause = false
	n := &fakeNotifier{}
	f.opts.Notifier = n
	f.h.setStatus(func(string) (int, map[string]string) { return 500, nil })
	f.start()
	for i := 0; i < AutoPauseRounds+2; i++ {
		f.round("main")
	}
	as := f.acct("main")
	if as.Status == store.StatusPaused || as.NextRun.IsZero() || !as.NextProbe.IsZero() {
		t.Fatalf("paused although auto_pause is off: %+v", as)
	}
	if as.ConsecutiveFailedRound != AutoPauseRounds+2 {
		t.Errorf("failed rounds = %d", as.ConsecutiveFailedRound)
	}
	if f.hasEvent("auto_pause") {
		t.Error("auto_pause event")
	}
	for _, k := range n.kinds("main") {
		if k == "auto_paused" {
			t.Errorf("auto_paused alert: %v", n.kinds("main"))
		}
	}
}
