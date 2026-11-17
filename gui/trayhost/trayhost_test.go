package trayhost

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
)

func TestShouldStartEngine(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		own, other, quiet, w bool
	}{
		{"nothing runs", false, false, false, true},
		{"we run already", true, false, false, false},
		{"the service runs", false, true, false, false},
		{"stepping aside", false, false, true, false},
	} {
		if got := ShouldStartEngine(tt.own, tt.other, tt.quiet, false); got != tt.w {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.w)
		}
	}
}

// fakeHost stands in for the engine lock and the snapshot.
type fakeHost struct {
	mu       sync.Mutex
	other    bool // another process holds the lock
	starts   int
	runErr   error
	snap     ops.Snapshot
	handover bool
	running  chan struct{} // closed while our engine runs
	engineUp bool
}

func (f *fakeHost) deps() Deps {
	return Deps{
		Snapshot: func() (ops.Snapshot, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			s := f.snap
			s.Engine.Running = f.other || f.engineUp
			return s, nil
		},
		EngineRunning: func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.other || f.engineUp },
		RunEngine: func(ctx context.Context) error {
			f.mu.Lock()
			f.starts++
			if f.runErr != nil {
				err := f.runErr
				f.mu.Unlock()
				return err
			}
			f.engineUp = true
			f.mu.Unlock()
			<-ctx.Done()
			f.mu.Lock()
			f.engineUp = false
			f.mu.Unlock()
			return nil
		},
		HandoverRequested: func() bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			h := f.handover
			f.handover = false
			return h
		},
	}
}

func (f *fakeHost) startCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.starts }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTrayRunsTheEngineWhenNobodyElseDoes(t *testing.T) {
	f := &fakeHost{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(ctx, f.deps())

	st := c.Refresh()
	waitFor(t, "engine start", func() bool { return f.startCount() == 1 && c.Hosting() })
	if st = c.Refresh(); st.Mode != ModeHosted {
		t.Fatalf("mode %v, want hosted", st.Mode)
	}
	c.Refresh()
	if f.startCount() != 1 {
		t.Fatalf("the engine was started %d times", f.startCount())
	}
	c.Stop()
	if c.Hosting() {
		t.Fatal("still hosting after Stop")
	}
}

func TestTrayOnlyMonitorsWhenTheServiceRuns(t *testing.T) {
	f := &fakeHost{other: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(ctx, f.deps())

	for i := 0; i < 3; i++ {
		if st := c.Refresh(); st.Mode != ModeMonitor || st.Icon == IconIdle {
			t.Fatalf("refresh %d: %+v", i, st)
		}
	}
	if f.startCount() != 0 || c.Hosting() {
		t.Fatalf("the engine started although a service runs it (%d)", f.startCount())
	}
	// When the service goes away the tray takes over.
	f.mu.Lock()
	f.other = false
	f.mu.Unlock()
	c.Refresh()
	waitFor(t, "takeover", func() bool { return f.startCount() == 1 && c.Hosting() })
	c.Stop()
}

func TestTrayLosingTheLockRaceFallsBackToMonitoring(t *testing.T) {
	f := &fakeHost{runErr: engine.ErrRunning}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(ctx, f.deps())
	c.Refresh()
	waitFor(t, "failed start", func() bool { return f.startCount() == 1 && !c.Hosting() })
	f.mu.Lock()
	f.other = true
	f.mu.Unlock()
	if st := c.Refresh(); st.Mode != ModeMonitor {
		t.Fatalf("mode %v", st.Mode)
	}
}

func TestTrayRetriesAfterAFailedStart(t *testing.T) {
	f := &fakeHost{runErr: errors.New("config file not found")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(ctx, f.deps())
	c.Refresh()
	waitFor(t, "first attempt", func() bool { return f.startCount() == 1 && !c.Hosting() })
	c.Refresh()
	waitFor(t, "second attempt", func() bool { return f.startCount() == 2 })
	waitFor(t, "second attempt failed", func() bool { return !c.Hosting() })
	f.mu.Lock()
	f.runErr = nil
	f.mu.Unlock()
	c.Refresh()
	waitFor(t, "success", func() bool { return c.Hosting() })
	c.Stop()
}

func TestHandoverReleasesTheEngineAndStaysQuiet(t *testing.T) {
	f := &fakeHost{}
	now := time.Now()
	d := f.deps()
	d.Now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(ctx, d)
	c.Refresh()
	waitFor(t, "engine start", c.Hosting)

	f.mu.Lock()
	f.handover = true
	f.mu.Unlock()
	if !c.CheckHandover() || c.Hosting() {
		t.Fatal("the engine was not released")
	}
	// The lock is free, but the tray must not grab it back at once.
	if c.Refresh(); f.startCount() != 1 {
		t.Fatalf("restarted during the quiet time (%d)", f.startCount())
	}
	now = now.Add(3 * time.Minute)
	c.Refresh()
	waitFor(t, "restart after the quiet time", func() bool { return f.startCount() == 2 })
	c.Stop()
}

func TestStateIconAndTexts(t *testing.T) {
	next := time.Now().Add(time.Hour).Format(time.RFC3339)
	mk := func(running bool, statuses ...string) *fakeHost {
		f := &fakeHost{other: running}
		for _, s := range statuses {
			f.snap.Accounts = append(f.snap.Accounts, ops.AccountSummary{Name: s, Status: s, NextRun: &next})
		}
		return f
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := func(f *fakeHost) State { return New(ctx, f.deps()).state() }

	if st := state(mk(true, "ok", "ok")); st.Icon != IconOK || st.Healthy != 2 || st.Problems != 0 {
		t.Errorf("healthy: %+v", st)
	}
	if st := state(mk(true, "ok", "needs_attention")); st.Icon != IconWarn || st.Problems != 1 {
		t.Errorf("attention: %+v", st)
	}
	if st := state(mk(true, "paused", "paused")); st.Icon != IconWarn || !st.AllPaused {
		t.Errorf("paused: %+v", st)
	}
	if st := state(mk(false, "ok")); st.Icon != IconIdle || st.Mode != ModeStopped {
		t.Errorf("stopped: %+v", st)
	}
	if st := state(mk(true)); st.Icon != IconOK {
		t.Errorf("no accounts: %+v", st)
	}

	st := state(mk(true, "ok", "ok"))
	en, zh := StatusLine(LangEN, st), StatusLine(LangZH, st)
	if !strings.Contains(en, "2 account(s) OK, next run") || !strings.Contains(zh, "2 个账号正常，下次运行") {
		t.Errorf("status lines: %q / %q", en, zh)
	}
	if got := HostLine(LangZH, ModeMonitor); got != "由后台服务运行" {
		t.Errorf("host line: %q", got)
	}
	if HostLine(LangEN, ModeStopped) != "" {
		t.Error("a stopped engine has no host line")
	}
	if MenuTexts(LangZH).OpenWindow != "打开主窗口" || MenuTexts(LangEN).Quit != "Quit" {
		t.Error("menu texts")
	}
}

func TestInstalledServiceKeepsTheTrayFromTheLock(t *testing.T) {
	f := &fakeHost{}
	installed := true
	d := f.deps()
	d.ServiceState = func() (bool, error) { return installed, nil }
	d.ServiceCheckEvery = -1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(ctx, d)

	// The lock is free (the service is not up yet), but the tray must not take it.
	for i := 0; i < 3; i++ {
		c.Refresh()
	}
	time.Sleep(20 * time.Millisecond)
	if f.startCount() != 0 || c.Hosting() {
		t.Fatalf("the tray took the lock although the service is installed (%d)", f.startCount())
	}
	// Uninstalled again: the tray runs the engine.
	installed = false
	c.Refresh()
	waitFor(t, "start", c.Hosting)
	// Installed from outside while the tray hosts: it lets go.
	installed = true
	c.Refresh()
	if c.Hosting() {
		t.Fatal("the tray kept the engine from the installed service")
	}
}

func TestResumeEndsTheQuietTimeAfterAFailedInstall(t *testing.T) {
	f := &fakeHost{}
	now := time.Now()
	d := f.deps()
	d.Now = func() time.Time { return now }
	resume := false
	d.ResumeRequested = func() bool { r := resume; resume = false; return r }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(ctx, d)
	c.Refresh()
	waitFor(t, "start", c.Hosting)
	f.mu.Lock()
	f.handover = true
	f.mu.Unlock()
	c.CheckHandover()
	c.Refresh()
	if f.startCount() != 1 {
		t.Fatal("restarted during the quiet time")
	}
	resume = true
	c.Refresh()
	waitFor(t, "restart", func() bool { return f.startCount() == 2 })
	c.Stop()
}

func TestLocalRoundCountsAsHostedByTheTray(t *testing.T) {
	f := &fakeHost{other: true} // the round holds the lock
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(ctx, f.deps())
	end := c.LocalRound()
	if st := c.Refresh(); st.Mode != ModeHosted {
		t.Fatalf("mode %v during a local round", st.Mode)
	}
	end()
	if st := c.Refresh(); st.Mode != ModeMonitor {
		t.Fatalf("mode %v after it", st.Mode)
	}
}

func TestStartFailureIsLoggedOncePerKind(t *testing.T) {
	f := &fakeHost{runErr: errors.New("config file not found")}
	var buf strings.Builder
	var mu sync.Mutex
	d := f.deps()
	d.Log = slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(ctx, d)
	for i := 1; i <= 3; i++ {
		c.Refresh()
		n := i
		waitFor(t, "attempt", func() bool { return f.startCount() == n && !c.Hosting() })
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if n := strings.Count(buf.String(), "engine did not start"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, buf.String())
	}
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

// svcRig is a fake service state with a movable clock.
type svcRig struct {
	f         *fakeHost
	now       time.Time
	installed bool
	errs      []error // answers to give first; nil entries mean success
	asked     int
	hb        time.Time
	c         *Controller
}

func newSvcRig(t *testing.T) *svcRig {
	t.Helper()
	r := &svcRig{f: &fakeHost{}, now: time.Now(), installed: true}
	d := r.f.deps()
	d.Now = func() time.Time { return r.now }
	d.ServiceState = func() (bool, error) {
		r.asked++
		if len(r.errs) > 0 {
			e := r.errs[0]
			r.errs = r.errs[1:]
			if e != nil {
				return false, e
			}
		}
		return r.installed, nil
	}
	snap := d.Snapshot
	d.Snapshot = func() (ops.Snapshot, error) {
		s, err := snap()
		s.Heartbeat = r.hb
		return s, err
	}
	d.ServiceCheckEvery = 5 * time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.c = New(ctx, d)
	return r
}

func TestInstalledServiceWithRunningEngineIsMonitored(t *testing.T) {
	r := newSvcRig(t)
	r.f.other = true
	r.hb = r.now
	for i := 0; i < 4; i++ {
		r.now = r.now.Add(10 * time.Minute)
		r.hb = r.now
		if st := r.c.Refresh(); st.Mode != ModeMonitor || st.ServiceDown || st.Icon == IconWarn {
			t.Fatalf("refresh %d: %+v", i, st)
		}
	}
	if r.f.startCount() != 0 {
		t.Fatal("the tray took the lock from a running service")
	}
}

func TestInstalledServiceWithoutEngineWarnsButNeverHosts(t *testing.T) {
	r := newSvcRig(t)
	for i := 0; i < 6; i++ {
		st := r.c.Refresh()
		if i == 0 && st.ServiceDown {
			t.Fatal("warned at the first look; the engine may just be starting")
		}
		r.now = r.now.Add(time.Minute)
	}
	st := r.c.Refresh() // lock free for about 6 minutes, no heartbeat
	if !st.ServiceDown || st.Icon != IconWarn || st.Mode == ModeHosted {
		t.Fatalf("state: %+v", st)
	}
	if got := StatusLine(LangEN, st); got != "Background service installed but not running" {
		t.Fatalf("status line: %q", got)
	}
	if got := StatusLine(LangZH, st); got != "后台服务已安装但没有运行" {
		t.Fatalf("status line: %q", got)
	}
	time.Sleep(20 * time.Millisecond)
	if r.f.startCount() != 0 || r.c.Hosting() {
		t.Fatal("the tray hosted the engine for an installed service")
	}
	// A fresh heartbeat means the engine is alive after all (a restart in progress).
	r.hb = r.now
	if st := r.c.Refresh(); st.ServiceDown {
		t.Fatalf("a fresh heartbeat must clear the warning: %+v", st)
	}
	// The engine shows up: monitoring, no warning.
	r.f.mu.Lock()
	r.f.other = true
	r.f.mu.Unlock()
	if st := r.c.Refresh(); st.ServiceDown || st.Mode != ModeMonitor {
		t.Fatalf("state: %+v", st)
	}
}

func TestUninstalledServiceLetsTheTrayHost(t *testing.T) {
	r := newSvcRig(t)
	r.installed = false
	r.c.Refresh()
	waitFor(t, "hosting", func() bool { return r.f.startCount() == 1 && r.c.Hosting() })
	if st := r.c.Refresh(); st.Mode != ModeHosted || st.ServiceDown {
		t.Fatalf("state: %+v", st)
	}
	// Installed later (outside): the tray lets go after the cache expires.
	r.installed = true
	r.now = r.now.Add(6 * time.Minute)
	r.c.Refresh()
	if r.c.Hosting() {
		t.Fatal("the tray kept the lock from the installed service")
	}
}

func TestStatusErrorsAreNeverCached(t *testing.T) {
	boom := errors.New("systemctl failed")

	// An error keeps the previous answer and is retried at the next refresh.
	r := newSvcRig(t)
	r.f.other = true
	r.c.Refresh() // answer: installed
	r.now = r.now.Add(6 * time.Minute)
	r.errs = []error{boom}
	r.c.Refresh()
	asked := r.asked
	r.c.Refresh()
	if r.asked != asked+1 {
		t.Fatalf("the failed query was cached (asked %d -> %d)", asked, r.asked)
	}
	if r.f.startCount() != 0 {
		t.Fatal("hosted after a status error although the service was known to be installed")
	}

	// No answer at all: monitor and retry; after three failures count it as not installed.
	r = newSvcRig(t)
	r.installed = false
	r.errs = []error{boom, boom, boom}
	for i := 1; i <= 2; i++ {
		r.c.Refresh()
		time.Sleep(10 * time.Millisecond)
		if r.f.startCount() != 0 || r.c.Hosting() {
			t.Fatalf("hosted after %d unanswered queries", i)
		}
	}
	r.c.Refresh() // third failure
	waitFor(t, "hosting after three failures", func() bool { return r.f.startCount() == 1 })
	if r.asked != 3 {
		t.Fatalf("asked %d times", r.asked)
	}
}

func TestSuccessfulAnswersAreCachedUntilHandoverOrResume(t *testing.T) {
	r := newSvcRig(t)
	r.f.other = true
	r.c.Refresh()
	r.now = r.now.Add(time.Minute)
	r.c.Refresh()
	r.now = r.now.Add(time.Minute)
	r.c.Refresh()
	if r.asked != 1 {
		t.Fatalf("asked the platform %d times within 5 minutes", r.asked)
	}
	resume := true
	r.c.d.ResumeRequested = func() bool { v := resume; resume = false; return v }
	r.c.Refresh()
	if r.asked != 2 {
		t.Fatalf("a resume must drop the cache (asked %d)", r.asked)
	}
	r.now = r.now.Add(6 * time.Minute)
	r.c.Refresh()
	if r.asked != 3 {
		t.Fatalf("the cache must expire after 5 minutes (asked %d)", r.asked)
	}
}
