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

func TestAccountRefusedEverywhereEndsUpPausedWithoutRecovered(t *testing.T) {
	n := &fakeNotifier{}
	f := newFixture(t, delegatedAcct("main"))
	f.opts.Notifier = n
	f.h.setStatus(func(string) (int, map[string]string) { return 403, nil })
	f.start()
	// The refused APIs are used up over a few rounds; then each round counts.
	for i := 0; i < 12 && f.acct("main").Status == store.StatusOK; i++ {
		f.round("main")
		if k := n.kinds("main"); f.acct("main").Status == store.StatusOK && len(k) != 0 {
			t.Fatalf("round %d alerted: %v", i+1, k)
		}
	}
	as := f.acct("main")
	if as.Status != store.StatusPaused || !strings.Contains(as.Reason, "403/404") || strings.Contains(as.Reason, "permission") {
		t.Fatalf("got %+v", as)
	}
	if k := n.kinds("main"); len(k) != 1 || k[0] != notify.AutoPaused {
		t.Errorf("alerts %v, want just auto_paused", k)
	}
	// The probe is refused too: still paused, nothing recovered, across the 24 h skip expiry.
	f.clock.Advance(probeFirst + 24*time.Hour)
	f.tickWait()
	if as := f.acct("main"); as.Status != store.StatusPaused || as.Probes != 1 {
		t.Errorf("after the probe: %+v", as)
	}
	for _, k := range n.kinds("main") {
		if k == notify.Recovered {
			t.Error("false recovered")
		}
	}
}

func TestAllAPIsSkippedCountsAsAFailedRound(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.auths["main"].tok.Scopes = []string{"User.Read"} // few APIs
	f.h.setStatus(func(string) (int, map[string]string) { return 404, nil })
	f.start()
	for i := 0; i < 6 && f.acct("main").Status == store.StatusOK; i++ {
		f.round("main")
	}
	as := f.acct("main")
	if as.Status != store.StatusPaused || !strings.Contains(as.Reason, "license or mailbox removed") {
		t.Fatalf("got %+v", as)
	}
}

func TestRefusedRoundDoesNotEndAttention(t *testing.T) {
	n := &fakeNotifier{}
	f := newFixture(t, delegatedAcct("main"))
	f.opts.Notifier = n
	f.auths["main"].setErr(fmt.Errorf("x: %w", auth.ErrLoginRequired))
	f.start()
	f.round("main")
	f.auths["main"].setErr(nil)
	f.h.setStatus(func(string) (int, map[string]string) { return 403, nil })
	f.jump(attentionRetry)
	f.round("main")
	if as := f.acct("main"); as.Status != store.StatusNeedsAttention {
		t.Fatalf("cleared by a round without a success: %+v", as)
	}
	for _, k := range n.kinds("main") {
		if k == notify.Recovered {
			t.Error("recovered without a success")
		}
	}
	f.h.setStatus(nil)
	f.jump(attentionRetry)
	f.round("main")
	if f.acct("main").Status != store.StatusOK || !f.hasEvent("recovered") {
		t.Error("a successful round must recover")
	}
}

func TestProbeWaitsForTheWorkWindow(t *testing.T) {
	f, pausedAt := pausedFixture(t, nil) // 10:00 + 3 rounds, Tue
	base := f.h.total()
	f.h.setStatus(nil)
	f.e.withLock(func() { f.e.sched.allDay, f.e.sched.startH, f.e.sched.endH = false, 8, 22 })
	f.clock.Set(time.Date(2026, 11, 17, 23, 30, 0, 0, pausedAt.Location()))
	f.tickWait()
	if f.h.total() != base {
		t.Fatal("probed outside the work window")
	}
	f.clock.Set(time.Date(2026, 11, 18, 8, 30, 0, 0, pausedAt.Location()))
	f.tickWait()
	if f.acct("main").Status != store.StatusOK {
		t.Errorf("not probed in the window: %+v", f.acct("main"))
	}
}

func TestProbeStopsAfterTheRunPeriod(t *testing.T) {
	f, pausedAt := pausedFixture(t, nil)
	base := f.h.total()
	f.e.withLock(func() {
		y := pausedAt.AddDate(0, 0, -1)
		f.e.sched.periodTo = &civilDate{y.Year(), y.Month(), y.Day()}
	})
	f.clock.Advance(7 * time.Hour)
	f.tickWait()
	if f.h.total() != base {
		t.Error("probed after the run period ended")
	}
}

func TestProbeSignInFailureLogsNeedsAttention(t *testing.T) {
	f, pausedAt := pausedFixture(t, nil)
	f.auths["main"].setErr(fmt.Errorf("x: %w", auth.ErrLoginRequired))
	f.clock.Set(pausedAt.Add(6 * time.Hour))
	f.tickWait()
	if !f.hasEvent("needs_attention") {
		t.Error("no needs_attention event")
	}
}

func TestProbeRejectedTokenForcesRefreshNextTime(t *testing.T) {
	f, pausedAt := pausedFixture(t, nil)
	f.h.setStatus(func(string) (int, map[string]string) { return 401, nil })
	f.clock.Set(pausedAt.Add(6 * time.Hour))
	f.tickWait()
	f.e.mu.Lock()
	forced := f.e.rt["main"].forceTok
	f.e.mu.Unlock()
	if !forced {
		t.Fatal("forceTok not set after a 401 probe")
	}
	f.clock.Advance(12 * time.Hour)
	f.tickWait()
	last := f.auths["main"].forced
	if !last[len(last)-1] {
		t.Error("the next probe did not ask for a fresh token")
	}
}

// A resume must not leave the 24 h API pauses behind: the account would find
// nothing to call, count a refused round and pause itself again.
func TestManualResumeClearsAPIPauses(t *testing.T) {
	n := &fakeNotifier{}
	f := newFixture(t, delegatedAcct("main"))
	f.opts.Notifier = n
	f.h.setStatus(func(string) (int, map[string]string) { return 403, nil })
	f.start()
	for i := 0; i < 12 && f.acct("main").Status == store.StatusOK; i++ {
		f.round("main")
	}
	if as := f.acct("main"); as.Status != store.StatusPaused || len(as.APISkipUntil) == 0 {
		t.Fatalf("setup: %+v", as)
	}
	f.h.setStatus(nil)
	if err := TriggerResume(f.dir, "main"); err != nil {
		t.Fatal(err)
	}
	f.tickWait()
	if as := f.acct("main"); as.Status != store.StatusOK || len(as.APISkipUntil) != 0 {
		t.Fatalf("resume left pauses: %+v", as)
	}
	base := f.h.total()
	for i := 0; i < AutoPauseRounds+1; i++ {
		f.jump(10 * time.Minute)
		f.round("main")
	}
	as := f.acct("main")
	if as.Status != store.StatusOK || f.h.total() == base || as.LastSuccess.IsZero() {
		t.Fatalf("paused again or made no call: %+v", as)
	}
	paused := 0
	for _, k := range n.kinds("main") {
		if k == notify.AutoPaused {
			paused++
		}
	}
	if paused != 1 {
		t.Errorf("%d auto_paused alerts, want 1", paused)
	}
}

func TestProbeResumeClearsAPIPauses(t *testing.T) {
	f, pausedAt := pausedFixture(t, nil)
	f.h.setStatus(nil)
	f.e.updateAccount("main", func(as *store.AccountState) {
		as.APISkipUntil = map[string]time.Time{}
		for _, a := range f.e.apis {
			if a.ID != "user.me" {
				as.APISkipUntil[a.ID] = pausedAt.Add(30 * time.Hour)
			}
		}
	})
	f.clock.Set(pausedAt.Add(6 * time.Hour))
	f.tickWait()
	if as := f.acct("main"); as.Status != store.StatusOK || len(as.APISkipUntil) != 0 {
		t.Fatalf("probe resume left pauses: %+v", as)
	}
}

func TestAppAccountLosingEveryAPIPausesWhenUserListIsRefused(t *testing.T) {
	f := newFixture(t, appAcct())
	f.auths["svc"].tok.Scopes = []string{"Mail.Read", "Organization.Read.All"}
	f.cfg.Schedule.APIsPerRoundMin, f.cfg.Schedule.APIsPerRoundMax = 30, 30
	f.h.usersCode = 403
	f.h.setStatus(func(string) (int, map[string]string) { return 404, nil })
	f.start()
	for i := 0; i < 8 && f.acct("svc").Status == store.StatusOK; i++ {
		f.round("svc")
	}
	as := f.acct("svc")
	if as.Status != store.StatusPaused {
		t.Fatalf("not auto-paused: %+v", as)
	}
}
