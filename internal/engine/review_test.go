package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func (f *fixture) eventMsgs(name string) []string {
	es, _ := store.New(f.dir).Read(store.Filter{})
	var out []string
	for _, e := range es {
		if e.Event == name {
			out = append(out, e.Message)
		}
	}
	return out
}

func TestFarFutureNextRunIsRescheduledAtStartup(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	st := store.NewState()
	// Saved while the clock was a month fast.
	st.Accounts["main"] = &store.AccountState{Status: store.StatusOK, NextRun: f.clock.now.Add(30 * 24 * time.Hour)}
	_ = store.New(f.dir).SaveState(st)
	f.start()
	if d := f.acct("main").NextRun.Sub(f.clock.now); d > 5*24*time.Hour {
		t.Errorf("next_run still %s away", d)
	}
	if !f.hasEvent("clock_fix") {
		t.Error("no clock_fix event")
	}
}

func TestFarFutureNextRunIsRescheduledOnTick(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.start()
	f.tickWait()
	// The clock ran a month ahead, then NTP set it back.
	f.e.updateAccount(context.Background(), "main", func(as *store.AccountState) { as.NextRun = f.clock.now.Add(29 * 24 * time.Hour) })
	f.clock.Advance(-time.Hour)
	f.tickWait()
	if !f.hasEvent("clock_change") {
		t.Error("backward jump not reported")
	}
	if d := f.acct("main").NextRun.Sub(f.clock.now); d > 5*24*time.Hour || d < 0 {
		t.Errorf("next_run %s away after correction", d)
	}
}

func TestDistantPeriodStartIsNotRescheduledEveryTick(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	ps := toml.LocalDate{Year: 2027, Month: 2, Day: 1}
	f.cfg.Schedule.PeriodStart = &ps
	f.start()
	first := f.acct("main").NextRun
	for i := 0; i < 4; i++ {
		f.clock.Advance(time.Minute)
		f.tickWait()
	}
	if !f.acct("main").NextRun.Equal(first) || f.hasEvent("clock_fix") {
		t.Errorf("legitimate far next_run was touched: %s -> %s", first, f.acct("main").NextRun)
	}
}

func TestLastDayOfPeriodIsNotBursty(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	pe := toml.LocalDate{Year: 2026, Month: 11, Day: 17} // today
	f.cfg.Schedule.PeriodEnd = &pe
	f.start()
	end := time.Date(2026, 11, 18, 3, 0, 0, 0, f.clock.now.Location())
	var callsAtMidnight int
	for f.clock.now.Before(end) {
		f.clock.Advance(5 * time.Minute)
		f.tickWait()
		if f.clock.now.Hour() == 23 && f.clock.now.Minute() == 55 {
			callsAtMidnight = f.h.total()
		}
	}
	as := f.acct("main")
	// 12 hours left in the window at 40 minutes minimum spacing.
	if as.Totals.Rounds < 1 || as.Totals.Rounds > 18 {
		t.Errorf("%d rounds on the last day, want 1..18", as.Totals.Rounds)
	}
	if as.Status != store.StatusPeriodEnded {
		t.Errorf("status %s after the period", as.Status)
	}
	if callsAtMidnight == 0 || f.h.total() != callsAtMidnight {
		t.Errorf("calls after the end: %d -> %d", callsAtMidnight, f.h.total())
	}

	// Extending the period brings the account back.
	pe2 := toml.LocalDate{Year: 2026, Month: 12, Day: 31}
	f.e.mu.Lock()
	f.e.cfg.Schedule.PeriodEnd = &pe2
	f.e.sched, _ = newSchedule(f.e.cfg.Schedule)
	f.e.mu.Unlock()
	f.tickWait()
	if f.acct("main").Status != store.StatusOK || f.acct("main").NextRun.IsZero() {
		t.Errorf("not reactivated: %+v", f.acct("main"))
	}
}

func appAcct() config.Account {
	return config.Account{Name: "svc", Auth: config.AuthClientSecret, Tenant: "example.org", ClientID: testGUID}
}

func TestFailedUserLookupIsCachedAndNeedsAttention(t *testing.T) {
	f := newFixture(t, appAcct())
	f.auths["svc"].tok.Scopes = []string{"Mail.Read"} // three APIs, all with {user}
	f.cfg.Schedule.APIsPerRoundMin, f.cfg.Schedule.APIsPerRoundMax = 3, 3
	f.h.usersCode = 403
	f.start()
	f.round("svc")
	as := f.acct("svc")
	if as.Status != store.StatusNeedsAttention || !strings.Contains(as.Reason, "set `username`") || !strings.Contains(as.Reason, "User.Read.All") {
		t.Fatalf("got %+v", as)
	}
	if n := f.h.count("/users"); n != 1 {
		t.Errorf("%d lookups in one round, want 1", n)
	}
	f.round("svc")
	if n := f.h.count("/users"); n != 1 {
		t.Errorf("failure not cached for the day: %d lookups", n)
	}
	// An explicit run retries, and succeeds once the permission is there.
	f.h.usersCode = 0
	_ = TriggerRun(f.dir, "svc")
	f.tickWait()
	if as := f.acct("svc"); as.Status != store.StatusOK || as.LastSuccess.IsZero() {
		t.Errorf("did not recover: %+v", as)
	}
}

func TestUnknownTokenErrorsStoreNoProviderText(t *testing.T) {
	f := newFixture(t, delegatedAcct("odd"), delegatedAcct("coded"))
	f.auths["odd"].setErr(errors.New("request to https://login.contoso-tenant.example/oauth2/token failed, trace 9f8e7d6c-aaaa-bbbb-cccc-111122223333"))
	f.auths["coded"].setErr(errors.New("AADSTS7000999: tenant contoso-tenant.example is odd. Correlation ID: 9f8e7d6c-aaaa-bbbb-cccc-111122223333"))
	f.start()
	f.round("odd")
	f.round("coded")
	if r := f.acct("odd").Reason; r != "sign-in failed (unknown error)" {
		t.Errorf("odd: %q", r)
	}
	if r := f.acct("coded").Reason; r != "sign-in failed (AADSTS7000999)" {
		t.Errorf("coded: %q", r)
	}
	for _, pat := range []string{filepath.Join("logs", "calls-*.jsonl"), "state.json"} {
		files, _ := filepath.Glob(filepath.Join(f.dir, pat))
		for _, p := range files {
			b, _ := os.ReadFile(p)
			for _, bad := range []string{"contoso-tenant", "9f8e7d6c", "oauth2"} {
				if strings.Contains(string(b), bad) {
					t.Errorf("%s contains %q", filepath.Base(p), bad)
				}
			}
		}
	}
}

func TestLockWaitCoversBriefProbes(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.opts.LockWait = 5 * time.Second
	e, _ := New(f.opts)
	release, err := AcquireLock(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(200 * time.Millisecond); release() }()
	rel2, err := e.acquire(context.Background())
	if err != nil {
		t.Fatalf("did not wait for a brief holder: %v", err)
	}
	rel2()

	f.opts.LockWait = 100 * time.Millisecond
	e, _ = New(f.opts)
	hold, _ := AcquireLock(f.dir)
	defer hold()
	if _, err := e.acquire(context.Background()); !errors.Is(err, ErrRunning) {
		t.Errorf("got %v", err)
	}
}

func TestFreshTokenAfterGraph401(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.h.setStatus(func(string) (int, map[string]string) { return 401, nil })
	f.start()
	f.round("main")
	f.h.setStatus(nil)
	f.round("main")
	f.round("main")
	got := f.auths["main"].forced
	if len(got) != 3 || got[0] || !got[1] || got[2] {
		t.Errorf("forced refresh sequence = %v, want [false true false]", got)
	}
}

func TestForcedRefreshFailureFallsBackToSilent(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.auths["main"].forcedErr = fmt.Errorf("claims rejected: %w", auth.ErrLoginRequired)
	f.h.setStatus(func(string) (int, map[string]string) { return 401, nil })
	f.start()
	f.round("main")
	f.h.setStatus(nil)
	f.round("main")
	got := f.auths["main"].forced
	if len(got) != 3 || got[0] || !got[1] || got[2] {
		t.Fatalf("token calls = %v, want [false true false] (forced attempt, then silent fallback)", got)
	}
	if st := f.acct("main"); st.Status != store.StatusOK {
		t.Errorf("status = %q, want ok after the silent fallback succeeded", st.Status)
	}
}

func TestRunOnceSkipsPausedAndEndedAccounts(t *testing.T) {
	f := newFixture(t, delegatedAcct("auto"), delegatedAcct("ended"), delegatedAcct("fine"))
	st := store.NewState()
	st.Accounts["auto"] = &store.AccountState{Status: store.StatusPaused}
	st.Accounts["ended"] = &store.AccountState{Status: store.StatusPeriodEnded}
	_ = store.New(f.dir).SaveState(st)
	var msgs []string
	f.opts.OnEntry = func(e store.Entry) {
		if e.Event == "trigger_ignored" {
			msgs = append(msgs, e.Account+": "+e.Message)
		}
	}
	if err := RunOnce(context.Background(), f.opts, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := store.New(f.dir).LoadState()
	if after.Accounts["auto"].Totals.Rounds != 0 || after.Accounts["ended"].Totals.Rounds != 0 || after.Accounts["fine"].Totals.Rounds != 1 {
		t.Errorf("rounds: %+v", after.Accounts)
	}
	if len(msgs) != 2 || !strings.Contains(strings.Join(msgs, "|"), "account resume auto") || !strings.Contains(strings.Join(msgs, "|"), "period has ended") {
		t.Errorf("messages: %v", msgs)
	}
}

func TestReloadFlagsNetworkAndVaultChanges(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	path := filepath.Join(f.dir, "config.toml")
	_ = f.cfg.Save(path)
	f.opts.ConfigPath = path
	f.start()

	next := *f.cfg
	next.Network.Proxy = "http://127.0.0.1:9"
	if err := next.Save(path); err != nil {
		t.Fatal(err)
	}
	_ = TriggerReload(f.dir)
	f.tickWait()
	msgs := f.eventMsgs("config_reload")
	if len(msgs) != 1 || !strings.Contains(msgs[0], "restart required for network/vault changes") {
		t.Errorf("proxy change: %v", msgs)
	}
	// A reload that changes neither says nothing special.
	next.Schedule.APIsPerRoundMax = 4
	_ = next.Save(path)
	_ = TriggerReload(f.dir)
	f.tickWait()
	if msgs = f.eventMsgs("config_reload"); len(msgs) != 2 || msgs[1] != "" {
		t.Errorf("plain change: %v", msgs)
	}
}

func TestPanicWhileBookingDoesNotKillTheProcess(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	armed := true
	f.opts.OnEntry = func(e store.Entry) {
		if armed && e.Event == "round_end" {
			armed = false
			panic("hook failure")
		}
	}
	f.start()
	f.round("main")
	if !f.hasEvent("panic") {
		t.Error("panic not reported")
	}
	// The lock is free and later rounds work.
	f.round("main")
	if f.acct("main").Totals.Rounds < 2 {
		t.Errorf("engine stuck: %+v", f.acct("main").Totals)
	}

	ran := false
	f.e.guard("test", func() { ran = true; panic("x") })
	if !ran {
		t.Error("guard did not run the step")
	}
}

func TestStaleTempFilesAreRemovedAtStart(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	cfgDir := t.TempDir()
	path := filepath.Join(cfgDir, "config.toml")
	_ = f.cfg.Save(path)
	f.opts.ConfigPath = path
	old := time.Now().Add(-time.Hour)
	var stale []string
	for _, p := range []string{
		filepath.Join(f.dir, "state.json.tmp-1"), filepath.Join(cfgDir, "config.toml.tmp-2"),
	} {
		_ = os.WriteFile(p, []byte("x"), 0o600)
		_ = os.Chtimes(p, old, old)
		stale = append(stale, p)
	}
	f.start()
	for _, p := range stale {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s not removed", p)
		}
	}
}
