package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func inRange(t *testing.T, what string, got, lo, hi time.Duration) {
	t.Helper()
	if got < lo || got > hi {
		t.Errorf("%s = %s, want within [%s, %s]", what, got, lo, hi)
	}
}

func TestStartupOverdueIsNotMadeUp(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	past := f.clock.now.Add(-5 * time.Hour)
	st := store.NewState()
	st.Accounts["main"] = &store.AccountState{Status: store.StatusOK, NextRun: past, Totals: store.Totals{Rounds: 7}}
	if err := store.New(f.dir).SaveState(st); err != nil {
		t.Fatal(err)
	}
	e := f.start()
	as := f.acct("main")
	inRange(t, "next_run delay", as.NextRun.Sub(f.clock.now), time.Minute, 5*time.Minute)
	if as.Totals.Rounds != 7 {
		t.Error("totals not restored")
	}
	f.tickWait()
	if f.h.total() != 0 {
		t.Errorf("a missed round was made up (%d calls)", f.h.total())
	}
	_ = e
}

func TestStartupKeepsFutureNextRun(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	future := f.clock.now.Add(3 * time.Hour)
	st := store.NewState()
	st.Accounts["main"] = &store.AccountState{Status: store.StatusOK, NextRun: future}
	_ = store.New(f.dir).SaveState(st)
	f.start()
	if as := f.acct("main"); !as.NextRun.Equal(future) {
		t.Errorf("next_run changed: %s", as.NextRun)
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.start()
	f.round("main")
	before := f.acct("main")
	if before.Totals.Rounds != 1 || before.LastSuccess.IsZero() || len(before.GrantedScopes) == 0 {
		t.Fatalf("unexpected state %+v", before)
	}
	f2 := *f
	f2.start()
	after := f2.acct("main")
	if after.Totals != before.Totals || !after.LastSuccess.Equal(before.LastSuccess) || !after.NextRun.Equal(before.NextRun) {
		t.Errorf("state lost:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestDamagedStateStartsFresh(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	_ = os.WriteFile(filepath.Join(f.dir, "state.json"), []byte("{not json"), 0o600)
	f.start()
	if f.acct("main").Status != store.StatusOK || !f.hasEvent("state_reset") {
		t.Error("engine did not recover from a damaged state file")
	}
}

func TestTimeJumpPostponesDueRounds(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.start()
	f.tickWait()
	// The machine sleeps for hours; the next_run slipped into the past.
	f.clock.Advance(4 * time.Hour)
	f.tickWait()
	if f.h.total() != 0 {
		t.Fatalf("round ran right after wake-up (%d calls)", f.h.total())
	}
	if !f.hasEvent("resume") {
		t.Error("no resume event")
	}
	inRange(t, "postponement", f.acct("main").NextRun.Sub(f.clock.now), time.Minute, 3*time.Minute)

	// A normal short gap is not a jump.
	f.clock.Advance(f.opts.Tick)
	f.e.updateAccount("main", func(as *store.AccountState) { as.NextRun = f.clock.now.Add(-time.Second) })
	f.tickWait()
	if f.h.total() == 0 {
		t.Error("due round did not run on a normal tick")
	}
}

func TestNetworkErrorIsNotAFailure(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.auths["main"].setErr(fmt.Errorf("dial: %w", auth.ErrTransient))
	f.start()
	for i := 0; i < 6; i++ {
		f.round("main")
	}
	as := f.acct("main")
	if as.Status != store.StatusOK || as.ConsecutiveFailedRound != 0 || as.Totals.Failed != 0 {
		t.Errorf("network errors counted: %+v", as)
	}
	if got := f.lastRoundEnd("main").Skipped; got != "network" {
		t.Errorf("skipped_reason = %q", got)
	}
	inRange(t, "retry delay", as.NextRun.Sub(f.clock.now), 5*time.Minute, 15*time.Minute)
	if f.h.total() != 0 {
		t.Error("calls made without a token")
	}
}

func TestLoginRequiredNeedsAttentionThenRecovers(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.opts.Hint = func(err error, name string) string { return "run: e5extend account login " + name }
	f.auths["main"].setErr(fmt.Errorf("refresh: %w", auth.ErrLoginRequired))
	f.start()
	f.round("main")
	as := f.acct("main")
	if as.Status != store.StatusNeedsAttention || !strings.Contains(as.Reason, "e5extend account login main") {
		t.Fatalf("got %+v", as)
	}
	if got := as.NextRun.Sub(f.clock.now); got != attentionRetry {
		t.Errorf("retry in %s, want %s", got, attentionRetry)
	}
	if !f.hasEvent("needs_attention") {
		t.Error("no needs_attention event")
	}

	// Six hours later the silent retry works, so the account recovers by itself.
	f.auths["main"].setErr(nil)
	f.jump(attentionRetry)
	f.round("main")
	as = f.acct("main")
	if as.Status != store.StatusOK || as.Reason != "" || as.LastSuccess.IsZero() {
		t.Errorf("did not recover: %+v", as)
	}
	if !f.hasEvent("recovered") {
		t.Error("no recovered event")
	}
}

func TestOtherTokenErrorKinds(t *testing.T) {
	for _, kind := range []error{auth.ErrInvalidClient, auth.ErrConsentRequired} {
		f := newFixture(t, delegatedAcct("main"))
		f.auths["main"].setErr(fmt.Errorf("x: %w", kind))
		f.start()
		f.round("main")
		if f.acct("main").Status != store.StatusNeedsAttention {
			t.Errorf("%v: status %s", kind, f.acct("main").Status)
		}
	}
}

func TestNoUsableAPIsNeedsAttention(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.auths["main"].tok.Scopes = []string{"openid", "profile"}
	f.start()
	f.round("main")
	as := f.acct("main")
	if as.Status != store.StatusNeedsAttention || !strings.Contains(as.Reason, "no usable APIs") {
		t.Errorf("got %+v", as)
	}
}

// throttledFixture serves 429 with the given Retry-After for the contacts call.
func throttledFixture(t *testing.T, n int, retry string) *fixture {
	t.Helper()
	f := newFixture(t, delegatedAcct("main"))
	f.cfg.Schedule.APIsPerRoundMin, f.cfg.Schedule.APIsPerRoundMax = 3, 3
	f.auths["main"].tok.Scopes = []string{"Contacts.Read", "Tasks.Read"} // contacts.list, todo.lists, planner.tasks
	left := n
	var mu sync.Mutex
	f.h.setStatus(func(p string) (int, map[string]string) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(p, "contacts") && left != 0 {
			left--
			return 429, map[string]string{"Retry-After": retry}
		}
		return 200, nil
	})
	f.start()
	return f
}

func callsOf(es []store.Entry) []store.Entry {
	var calls []store.Entry
	for _, e := range es {
		if e.API != "" {
			calls = append(calls, e)
		}
	}
	return calls
}

func TestThrottledCallIsRetriedAfterRetryAfter(t *testing.T) {
	f := throttledFixture(t, 2, "20") // two 429s, then it works
	f.round("main")
	if got := f.clock.Sleeps(); len(got) != 2 || got[0] != 20*time.Second || got[1] != 20*time.Second {
		t.Fatalf("slept %v, want two waits of 20s", got)
	}
	if n := f.h.count("contacts"); n != 3 {
		t.Errorf("contacts called %d times, want 3", n)
	}
	var classes []string
	for _, e := range callsOf(f.entries("main")) {
		if strings.Contains(e.API, "contacts") {
			classes = append(classes, e.Class)
		}
	}
	if strings.Join(classes, ",") != "throttled,throttled,ok" {
		t.Errorf("logged %v", classes)
	}
	as := f.acct("main")
	if as.ConsecutiveFailedRound != 0 || as.Totals.Failed != 0 || as.LastSuccess.IsZero() {
		t.Errorf("throttling counted as failure: %+v", as)
	}
}

func TestThrottledGivesUpAfterTwoRetries(t *testing.T) {
	f := throttledFixture(t, -1, "5") // always 429
	f.round("main")
	if n := f.h.count("contacts"); n != 3 {
		t.Errorf("contacts called %d times, want 1 + 2 retries", n)
	}
	as := f.acct("main")
	if as.ConsecutiveFailedRound != 0 || as.Totals.Failed != 0 || as.Status != store.StatusOK {
		t.Errorf("throttled call failed the round: %+v", as)
	}
	if end := f.lastRoundEnd("main"); end.Failed != 0 || end.OK == 0 {
		t.Errorf("round end %+v", end)
	}
}

func TestThrottledWaitHttpDateAndCap(t *testing.T) {
	// A Retry-After above the cap skips the call at once, without a wait.
	f := throttledFixture(t, -1, "3600")
	f.round("main")
	if got := f.clock.Sleeps(); len(got) != 0 {
		t.Errorf("slept %v for an over-long Retry-After", got)
	}
	if n := f.h.count("contacts"); n != 1 {
		t.Errorf("contacts called %d times, want 1", n)
	}
	if f.acct("main").Totals.Failed != 0 {
		t.Error("skipped call counted as failure")
	}
	// An HTTP date counts as the time left until then.
	f = throttledFixture(t, 1, time.Now().Add(31*time.Second).UTC().Format(http.TimeFormat))
	f.round("main")
	got := f.clock.Sleeps()
	if len(got) != 1 || got[0] < 25*time.Second || got[0] > 31*time.Second {
		t.Errorf("slept %v for an HTTP date 31s ahead", got)
	}
}

func TestThrottledDoesNotCountTowardsFailureSpike(t *testing.T) {
	f := throttledFixture(t, -1, "1")
	n := &fakeNotifier{}
	f.opts.Notifier = n
	f.start()
	for i := 0; i < 6; i++ {
		f.round("main")
	}
	for _, k := range n.kinds("main") {
		if k == notify.FailureSpike || k == notify.AutoPaused {
			t.Errorf("throttling raised %s", k)
		}
	}
	if f.acct("main").Status != store.StatusOK {
		t.Errorf("status %s", f.acct("main").Status)
	}
}

func TestUnavailableAPIIsPausedFor24Hours(t *testing.T) {
	for _, code := range []int{403, 404} {
		f := newFixture(t, delegatedAcct("main"))
		f.cfg.Schedule.APIsPerRoundMin, f.cfg.Schedule.APIsPerRoundMax = 3, 3
		f.auths["main"].tok.Scopes = []string{"Contacts.Read", "Tasks.Read"}
		f.h.setStatus(func(p string) (int, map[string]string) {
			if strings.Contains(p, "contacts") {
				return code, nil
			}
			return 200, nil
		})
		f.start()
		f.round("main")
		as := f.acct("main")
		until := as.APISkipUntil["contacts.list"]
		if want := f.clock.now.Add(unavailableFor); until.Sub(want) > time.Minute || want.Sub(until) > time.Minute {
			t.Fatalf("%d: skip until %s, want about %s", code, until, want)
		}
		// The round did not fail by itself.
		if as.Totals.Failed != 0 || as.ConsecutiveFailedRound != 0 || as.Status != store.StatusOK || as.LastSuccess.IsZero() {
			t.Errorf("%d: %+v", code, as)
		}
		for i := 0; i < 3; i++ {
			f.round("main")
		}
		if f.h.count("contacts") != 1 {
			t.Errorf("%d: contacts called while paused: %d", code, f.h.count("contacts"))
		}

		// After a day the API is tried again.
		f.jump(unavailableFor)
		f.h.setStatus(nil)
		f.round("main")
		if f.h.count("contacts") != 2 {
			t.Errorf("%d: contacts not retried after 24h: %d", code, f.h.count("contacts"))
		}
		if as := f.acct("main"); len(as.APISkipUntil) != 0 {
			t.Errorf("%d: skip map not cleaned: %+v", code, as.APISkipUntil)
		}
	}
}

func TestUnauthorizedStillFails(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.h.setStatus(func(string) (int, map[string]string) { return 401, nil })
	f.start()
	f.round("main")
	if as := f.acct("main"); as.Totals.Failed == 0 || len(as.APISkipUntil) != 0 {
		t.Fatalf("401 handled as unavailable: %+v", as)
	}
}

func TestThreeFailedRoundsPauseAndResumeTrigger(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.h.setStatus(func(string) (int, map[string]string) { return 500, nil })
	f.start()
	for i := 0; i < AutoPauseRounds; i++ {
		f.round("main")
	}
	as := f.acct("main")
	if as.Status != store.StatusPaused || !strings.Contains(as.Reason, "server_error") || !strings.Contains(as.Reason, "account resume main") {
		t.Fatalf("got %+v", as)
	}
	if !f.hasEvent("auto_pause") {
		t.Error("no auto_pause event")
	}
	calls := f.h.total()
	f.jump(probeFirst - time.Hour)
	if f.h.total() != calls {
		t.Error("paused account kept running")
	}

	f.h.setStatus(nil)
	if err := TriggerResume(f.dir, "main"); err != nil {
		t.Fatal(err)
	}
	f.tickWait()
	as = f.acct("main")
	if as.Status != store.StatusOK || as.ConsecutiveFailedRound != 0 {
		t.Fatalf("not resumed: %+v", as)
	}
	inRange(t, "resume delay", as.NextRun.Sub(f.clock.now), time.Minute, 5*time.Minute)
	f.clock.Advance(6 * time.Minute)
	f.tickWait()
	if f.acct("main").LastSuccess.IsZero() {
		t.Error("resumed account did not run")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "trigger", "main.resume")); !os.IsNotExist(err) {
		t.Error("resume trigger not consumed")
	}
}

func TestSuccessResetsFailureStreak(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.h.setStatus(func(string) (int, map[string]string) { return 500, nil })
	f.start()
	f.round("main")
	f.round("main")
	f.h.setStatus(nil)
	f.round("main")
	f.h.setStatus(func(string) (int, map[string]string) { return 500, nil })
	f.round("main")
	f.round("main")
	if as := f.acct("main"); as.Status != store.StatusOK || as.ConsecutiveFailedRound != 2 {
		t.Errorf("got %+v", as)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.auths["main"].panicNext = true
	f.start()
	f.round("main")
	as := f.acct("main")
	if as.ConsecutiveFailedRound != 1 || as.Totals.Failed != 1 || f.lastRoundEnd("main").Skipped != "panic" {
		t.Fatalf("panic round not booked as a failure: %+v", as)
	}
	f.round("main")
	as = f.acct("main")
	if as.ConsecutiveFailedRound != 0 || as.LastSuccess.IsZero() {
		t.Errorf("next round did not run normally: %+v", as)
	}
}

func TestPausedInConfigDoesNotRun(t *testing.T) {
	a := delegatedAcct("main")
	a.Paused = true
	f := newFixture(t, a)
	f.start()
	f.clock.Advance(time.Hour)
	f.tickWait()
	if f.h.total() != 0 {
		t.Error("paused account ran")
	}
	// Unpausing in the config schedules a soon-but-not-instant round.
	f.e.mu.Lock()
	f.e.cfg.Accounts[0].Paused = false
	f.e.mu.Unlock()
	f.tickWait()
	inRange(t, "delay after unpausing", f.acct("main").NextRun.Sub(f.clock.now), time.Minute, 5*time.Minute)
}

func TestRunNowTriggerFile(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"), delegatedAcct("other"))
	f.start()
	if err := TriggerRun(f.dir, "main"); err != nil {
		t.Fatal(err)
	}
	if err := TriggerRun(f.dir, "ghost"); err != nil {
		t.Fatal(err)
	}
	f.tickWait()
	if f.acct("main").Totals.Rounds != 1 || f.acct("other").Totals.Rounds != 0 {
		t.Errorf("main=%+v other=%+v", f.acct("main").Totals, f.acct("other").Totals)
	}
	left, _ := os.ReadDir(filepath.Join(f.dir, "trigger"))
	if len(left) != 0 {
		t.Errorf("trigger files left: %v", left)
	}
	if !f.hasEvent("trigger_ignored") {
		t.Error("unknown account not reported")
	}
}

func TestRunNowIgnoredWhileAutoPaused(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.start()
	f.e.updateAccount("main", func(as *store.AccountState) { as.Status = store.StatusPaused })
	_ = TriggerRun(f.dir, "main")
	f.tickWait()
	if f.h.total() != 0 {
		t.Error("paused account ran on trigger")
	}
}

func TestConfigReload(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	path := filepath.Join(f.dir, "config.toml")
	if err := f.cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	f.opts.ConfigPath = path
	f.opts.ConfigCheckEvery = 2
	f.auths["second"] = &fakeAuth{tok: f.auths["main"].tok}
	f.start()

	// A new account appears after the file's mtime changes (picked up on the 2nd tick).
	next := *f.cfg
	next.Accounts = append(append([]config.Account(nil), f.cfg.Accounts...), delegatedAcct("second"))
	if err := next.Save(path); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(path, future, future)
	f.tickWait()
	f.tickWait()
	f.e.mu.Lock()
	_, has := f.e.state.Accounts["second"]
	f.e.mu.Unlock()
	if !has || !f.hasEvent("config_reload") {
		t.Fatal("new account not loaded")
	}

	// An invalid file keeps the old configuration.
	if err := os.WriteFile(path, []byte("version = 1\n[schedule]\nwork_start = \"22:00\"\nwork_end = \"06:00\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := TriggerReload(f.dir); err != nil {
		t.Fatal(err)
	}
	f.tickWait()
	f.e.mu.Lock()
	n := len(f.e.cfg.Accounts)
	f.e.mu.Unlock()
	if n != 2 || !f.hasEvent("config_error") {
		t.Errorf("invalid config replaced the old one (accounts=%d)", n)
	}
}

func TestOldLogsArePruned(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	st := store.New(f.dir)
	for i := 0; i < 50; i++ {
		_ = st.Append(store.Entry{TS: f.clock.now.AddDate(0, 0, -i), Account: "main", Event: "x"})
	}
	f.start()
	f.tickWait()
	files, _ := filepath.Glob(filepath.Join(f.dir, "logs", "calls-*.jsonl"))
	if len(files) != 31 {
		t.Errorf("%d log files after pruning, want 31", len(files))
	}
	// Pruning happens once per day.
	_ = st.Append(store.Entry{TS: f.clock.now.AddDate(0, 0, -40), Event: "x"})
	f.tickWait()
	if files, _ = filepath.Glob(filepath.Join(f.dir, "logs", "calls-*.jsonl")); len(files) != 32 {
		t.Errorf("pruned twice on the same day: %d files", len(files))
	}
}

func TestNothingSensitiveIsStored(t *testing.T) {
	a := delegatedAcct("main")
	a.Username = "alice@example.com"
	f := newFixture(t, a, config.Account{Name: "svc", Auth: config.AuthClientSecret, Tenant: "example.org", ClientID: testGUID})
	f.auths["svc"].tok.Scopes = []string{"User.Read.All", "Mail.Read", "Calendars.Read"}
	f.start()
	f.round("main")
	f.round("svc")
	if f.h.count("uid-") == 0 && f.h.count("/users/") == 0 {
		t.Log("app account made no {user} call this time")
	}
	for _, pat := range []string{"calls-*.jsonl", "state.json", "heartbeat"} {
		files, _ := filepath.Glob(filepath.Join(f.dir, "logs", pat))
		more, _ := filepath.Glob(filepath.Join(f.dir, pat))
		for _, p := range append(files, more...) {
			b, _ := os.ReadFile(p)
			for _, bad := range []string{"secret-token", "alice", "example.com", "uid-", "Bearer", "@"} {
				if strings.Contains(string(b), bad) {
					t.Errorf("%s contains %q", filepath.Base(p), bad)
				}
			}
		}
	}
}

func TestApplicationIdentityPicksAUserOncePerDay(t *testing.T) {
	f := newFixture(t, config.Account{Name: "svc", Auth: config.AuthClientSecret, Tenant: "example.org", ClientID: testGUID})
	f.auths["svc"].tok.Scopes = []string{"Mail.Read"} // mail.* paths all use {user}
	f.start()
	f.round("svc")
	f.round("svc")
	f.round("svc")
	if n := f.h.count("/users"); n < 3 {
		t.Fatalf("only %d /users requests", n)
	}
	lookups := 0
	f.h.mu.Lock()
	for _, p := range f.h.hits {
		if p == "/users" {
			lookups++
		}
	}
	f.h.mu.Unlock()
	if lookups != 1 {
		t.Errorf("user list fetched %d times in one day, want 1", lookups)
	}
	f.jump(24 * time.Hour)
	f.round("svc")
	f.h.mu.Lock()
	lookups = 0
	for _, p := range f.h.hits {
		if p == "/users" {
			lookups++
		}
	}
	f.h.mu.Unlock()
	if lookups != 2 {
		t.Errorf("no new lookup on the next day (%d)", lookups)
	}
}

func TestSingleInstance(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.opts.Clock = SystemClock{}
	f.opts.Tick = 10 * time.Millisecond
	f.auths["main"].setErr(auth.ErrTransient)

	e1, err := New(f.opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e1.Run(ctx) }()

	// The heartbeat appears only after the lock is held and the engine has started.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if hb, err := ReadHeartbeat(f.dir); err == nil && !hb.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("engine never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !Running(f.dir) {
		t.Fatal("engine does not hold the lock")
	}
	e2, _ := New(f.opts)
	if err := e2.Run(context.Background()); !errors.Is(err, ErrRunning) {
		t.Errorf("second engine: %v", err)
	}
	if err := RunOnce(context.Background(), f.opts, nil); !errors.Is(err, ErrRunning) {
		t.Errorf("RunOnce while running: %v", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if Running(f.dir) {
		t.Error("lock still held after shutdown")
	}
	st, err := store.New(f.dir).LoadState()
	if err != nil || st.Accounts["main"] == nil {
		t.Errorf("state not saved on shutdown: %v %+v", err, st)
	}
	if !f.hasEvent("stop") {
		t.Error("no stop event")
	}
}

func TestShutdownAbortsRunningRound(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.start()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.e.updateAccount("main", func(as *store.AccountState) { as.NextRun = f.clock.now.Add(-time.Minute) })
	f.e.tick(ctx)
	f.e.wait()
	if f.acct("main").Totals.Rounds != 0 {
		t.Error("an aborted round was booked")
	}
}

func TestRunOnce(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"), delegatedAcct("other"))
	var seen []store.Entry
	f.opts.OnEntry = func(e store.Entry) { seen = append(seen, e) }
	if err := RunOnce(context.Background(), f.opts, []string{"main"}); err != nil {
		t.Fatal(err)
	}
	st, _ := store.New(f.dir).LoadState()
	if st.Accounts["main"].Totals.Rounds != 1 || st.Accounts["other"].Totals.Rounds != 0 {
		t.Errorf("state: %+v", st.Accounts)
	}
	calls := 0
	for _, e := range seen {
		if e.API != "" {
			calls++
		}
	}
	if calls == 0 {
		t.Error("OnEntry saw no calls")
	}
	if Running(f.dir) {
		t.Error("RunOnce left the lock held")
	}
}
