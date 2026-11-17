package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeUpdater answers the engine's update check without a network.
type fakeUpdater struct {
	mu     sync.Mutex
	status UpdateStatus
	err    error
	calls  int
	chans  []string
}

func (u *fakeUpdater) Check(_ context.Context, channel string) (UpdateStatus, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	u.chans = append(u.chans, channel)
	return u.status, u.err
}

func (u *fakeUpdater) set(v string, next time.Time, err error) {
	u.mu.Lock()
	u.status, u.err = UpdateStatus{Version: v, URL: "https://example.test/releases/v" + v, Next: next}, err
	u.mu.Unlock()
}

func (u *fakeUpdater) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func updateKinds(n *fakeNotifier) int {
	c := 0
	for _, k := range n.kinds("") {
		if k == "update_available" {
			c++
		}
	}
	return c
}

// updateFixture is an engine with an updater and a notifier, one minute past
// the start grace period.
func updateFixture(t *testing.T) (*fixture, *fakeUpdater, *fakeNotifier) {
	t.Helper()
	f := newFixture(t, delegatedAcct("main"))
	u := &fakeUpdater{}
	n := &fakeNotifier{}
	f.opts.Updater, f.opts.Notifier, f.opts.Version = u, n, "0.2.0"
	f.start()
	f.clock.Advance(2 * updateGrace)
	return f, u, n
}

func TestUpdateAnnouncedOncePerVersionAndForgottenAfterUpgrade(t *testing.T) {
	f, u, n := updateFixture(t)
	u.set("0.3.0", f.clock.Now().Add(24*time.Hour), nil)

	f.tickWait() // the check runs on its own goroutine
	f.tickWait() // and its answer is evaluated on the next tick
	if got := updateKinds(n); got != 1 {
		t.Fatalf("alerts after the first check: %d (%v)", got, n.kinds(""))
	}
	n.mu.Lock()
	ev := n.events[len(n.events)-1]
	n.mu.Unlock()
	if ev.Level() != "info" || ev.Account != "" || ev.Message == "" {
		t.Fatalf("event: %+v", ev)
	}
	for i := 0; i < 4; i++ {
		f.clock.Advance(30 * time.Minute)
		f.tickWait()
	}
	if updateKinds(n) != 1 || u.count() != 1 {
		t.Fatalf("alerts=%d checks=%d: once per version, once per day", updateKinds(n), u.count())
	}
	if _, ok := f.e.state.Notified["update_available"]; !ok {
		t.Fatal("the announcement is not recorded in state.json")
	}

	// A day later the checker is asked again; the user has upgraded meanwhile.
	f.clock.Advance(25 * time.Hour)
	u.set("", f.clock.Now().Add(24*time.Hour), nil)
	f.tickWait()
	f.tickWait()
	if u.count() != 2 {
		t.Fatalf("checks = %d", u.count())
	}
	if _, ok := f.e.state.Notified["update_available"]; ok {
		t.Fatal("the announcement was not forgotten once nothing newer exists")
	}

	// The next release is news again.
	f.clock.Advance(25 * time.Hour)
	u.set("0.4.0", f.clock.Now().Add(24*time.Hour), nil)
	f.tickWait()
	f.tickWait()
	if got := updateKinds(n); got != 2 {
		t.Fatalf("alerts = %d, want one per version", got)
	}
}

func TestUpdateStateSurvivesRestart(t *testing.T) {
	f, u, n := updateFixture(t)
	u.set("0.3.0", f.clock.Now().Add(24*time.Hour), nil)
	f.tickWait()
	f.tickWait()
	f.start() // a new engine process reads the same state
	f.clock.Advance(2 * updateGrace)
	f.tickWait()
	f.tickWait()
	if got := updateKinds(n); got != 1 {
		t.Fatalf("a restart announced the same version again: %d", got)
	}
}

func TestUpdateCheckSwitchedOffByConfig(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	u, n := &fakeUpdater{}, &fakeNotifier{}
	u.set("0.3.0", time.Time{}, nil)
	f.cfg.Update.Check = false
	f.opts.Updater, f.opts.Notifier = u, n
	f.start()
	f.clock.Advance(2 * updateGrace)
	f.tickWait()
	f.tickWait()
	if u.count() != 0 || updateKinds(n) != 0 {
		t.Fatalf("checks=%d alerts=%d with check = false", u.count(), updateKinds(n))
	}
}

func TestUpdateCheckWaitsForTheStartGrace(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	u := &fakeUpdater{}
	f.opts.Updater = u
	f.start()
	f.tickWait()
	if u.count() != 0 {
		t.Fatal("checked right at the start, before the network is likely to be up")
	}
}

func TestUpdateCheckOfflineIsHarmlessAndRetried(t *testing.T) {
	f, u, n := updateFixture(t)
	u.set("", time.Time{}, errors.New("no network"))
	f.tickWait()
	f.tickWait()
	if u.count() != 1 || updateKinds(n) != 0 {
		t.Fatalf("checks=%d alerts=%d", u.count(), updateKinds(n))
	}
	f.clock.Advance(10 * time.Minute)
	f.tickWait()
	if u.count() != 1 {
		t.Fatalf("retried after only 10 minutes: %d checks", u.count())
	}
	f.clock.Advance(time.Hour)
	u.set("0.3.0", f.clock.Now().Add(24*time.Hour), nil)
	f.tickWait()
	f.tickWait()
	if u.count() != 2 || updateKinds(n) != 1 {
		t.Fatalf("checks=%d alerts=%d after the network came back", u.count(), updateKinds(n))
	}
	if f.hasEvent("panic") {
		t.Fatal("the engine panicked")
	}
}

func TestUpdateChannelFromConfig(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	u := &fakeUpdater{}
	f.cfg.Update.Channel = "prerelease"
	f.opts.Updater = u
	f.start()
	f.clock.Advance(2 * updateGrace)
	f.tickWait()
	if len(u.chans) != 1 || u.chans[0] != "prerelease" {
		t.Fatalf("channels asked: %v", u.chans)
	}
}

func TestUpdateAlertFilteredOutIsStillRememberedOnce(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	u, n := &fakeUpdater{}, &fakeNotifier{}
	u.set("0.3.0", time.Now().Add(24*time.Hour), nil)
	f.cfg.Notify.Events = []string{"recovered"} // update_available is not wanted
	f.opts.Updater, f.opts.Notifier = u, n
	f.start()
	f.clock.Advance(2 * updateGrace)
	u.set("0.3.0", f.clock.Now().Add(24*time.Hour), nil)
	f.tickWait()
	f.tickWait()
	if updateKinds(n) != 0 {
		t.Fatalf("a filtered event was sent: %v", n.kinds(""))
	}
}
