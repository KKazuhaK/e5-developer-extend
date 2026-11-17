package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func TestAutoPauseOffResumesAutoPausedAccountOnReload(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	path := filepath.Join(f.dir, "config.toml")
	if err := f.cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	f.opts.ConfigPath = path
	f.h.setStatus(func(string) (int, map[string]string) { return 500, nil })
	f.start()
	for i := 0; i < AutoPauseRounds; i++ {
		f.round("main")
	}
	if as := f.acct("main"); as.Status != store.StatusPaused || as.NextProbe.IsZero() {
		t.Fatalf("not paused: %+v", as)
	}
	next := *f.cfg
	next.Schedule.AutoPause = false
	if err := next.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := TriggerReload(f.dir); err != nil {
		t.Fatal(err)
	}
	f.tickWait()
	as := f.acct("main")
	if as.Status != store.StatusOK || !as.NextProbe.IsZero() || as.Probes != 0 || as.NextRun.IsZero() {
		t.Fatalf("auto pause not ended: %+v", as)
	}
	f.h.setStatus(nil)
	for i := 0; i < 3; i++ {
		f.clock.Advance(time.Minute)
		f.tickWait()
	}
	if n := countEvents(f, "account_resume"); n != 1 {
		t.Errorf("account_resume fired %d times, want 1", n)
	}
}

func countEvents(f *fixture, name string) int {
	es, _ := store.New(f.dir).Read(store.Filter{})
	n := 0
	for _, e := range es {
		if e.Event == name {
			n++
		}
	}
	return n
}

func TestAutoPauseOffLeavesUserPausedAccountAlone(t *testing.T) {
	a := delegatedAcct("main")
	a.Paused = true
	f := newFixture(t, a)
	f.cfg.Schedule.AutoPause = false
	f.start()
	for i := 0; i < 3; i++ {
		f.clock.Advance(time.Hour)
		f.tickWait()
	}
	if f.h.total() != 0 || countEvents(f, "account_resume") != 0 {
		t.Errorf("user-paused account touched: calls=%d", f.h.total())
	}
}

func TestResumeIgnoresExpiredAPIPauses(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.start()
	f.round("main")
	f.e.mu.Lock()
	f.e.state.Accounts["main"].APISkipUntil = map[string]time.Time{"mail.messages": f.clock.Now().Add(-time.Hour)}
	f.e.mu.Unlock()
	if err := TriggerResume(f.dir, "main"); err != nil {
		t.Fatal(err)
	}
	f.tickWait()
	if f.hasEvent("account_resume") {
		t.Error("account_resume fired with nothing to clear")
	}
}

func TestResumeClearsAPIPausesOfARunningAccount(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.start()
	f.round("main")
	f.e.mu.Lock()
	f.e.state.Accounts["main"].APISkipUntil = map[string]time.Time{"mail.messages": f.clock.Now().Add(20 * time.Hour)}
	f.e.mu.Unlock()
	if err := TriggerResume(f.dir, "main"); err != nil {
		t.Fatal(err)
	}
	f.tickWait()
	if as := f.acct("main"); as.Status != store.StatusOK || len(as.APISkipUntil) != 0 {
		t.Fatalf("API pauses kept: %+v", as)
	}
	if !f.hasEvent("account_resume") {
		t.Error("no account_resume event")
	}
}
