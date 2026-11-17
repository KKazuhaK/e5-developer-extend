package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func importedState(rounds int, nextRun time.Time) *store.AccountState {
	return &store.AccountState{Status: store.StatusOK, NextRun: nextRun, Totals: store.Totals{Rounds: rounds}}
}

func pendingExists(dir string) bool { return store.HasPendingImport(dir) }

// A running engine adopts the state an import handed over on its next reload,
// and keeps it: its own save afterwards must not bring the old values back.
func TestReloadAdoptsImportedStateAndKeepsIt(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	path := filepath.Join(f.dir, "config.toml")
	if err := f.cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	f.opts.ConfigPath = path
	f.start()
	f.round("main")
	if f.acct("main").Totals.Rounds != 1 {
		t.Fatal("setup: no round ran")
	}

	// The import replaces the config (two accounts now, one of them overdue in the backup) and hands over the state.
	next := *f.cfg
	next.Accounts = []config.Account{delegatedAcct("main"), delegatedAcct("second")}
	f.auths["second"] = f.auths["main"]
	if err := next.Save(path); err != nil {
		t.Fatal(err)
	}
	past := f.clock.Now().Add(-6 * time.Hour)
	err := store.SavePendingImport(f.dir, &store.PendingImport{Mode: store.PendingReplace, Accounts: map[string]*store.AccountState{
		"main": importedState(7, past), "second": importedState(3, f.clock.Now().Add(2*time.Hour)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := TriggerReload(f.dir); err != nil {
		t.Fatal(err)
	}
	f.tickWait()

	if pendingExists(f.dir) {
		t.Error("the hand-over file was not removed")
	}
	if got := f.acct("main"); got.Totals.Rounds != 7 {
		t.Errorf("main: %+v", got)
	} else if d := got.NextRun.Sub(f.clock.Now()); d < time.Minute || d > 5*time.Minute+time.Hour*24 {
		t.Errorf("an overdue imported next run was not pushed back: %s", d)
	}
	if got := f.acct("second"); got.Totals.Rounds != 3 {
		t.Errorf("second: %+v", got)
	}
	// What the engine saved is what was imported, and stays so over later saves.
	f.tickWait()
	saved, err := store.New(f.dir).LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Accounts["main"].Totals.Rounds != 7 || saved.Accounts["second"].Totals.Rounds != 3 {
		t.Errorf("state.json after the reload: main=%+v second=%+v", saved.Accounts["main"], saved.Accounts["second"])
	}
	if !f.hasEvent("state_import") {
		t.Error("no state_import event")
	}
}

func TestMergeImportOnlyAddsAccountsTheEngineLacks(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	path := filepath.Join(f.dir, "config.toml")
	_ = f.cfg.Save(path)
	f.opts.ConfigPath = path
	f.start()
	f.round("main")

	next := *f.cfg
	next.Accounts = []config.Account{delegatedAcct("main"), delegatedAcct("extra")}
	f.auths["extra"] = f.auths["main"]
	_ = next.Save(path)
	err := store.SavePendingImport(f.dir, &store.PendingImport{Mode: store.PendingMerge, Accounts: map[string]*store.AccountState{
		"main": importedState(99, f.clock.Now().Add(time.Hour)), "extra": importedState(5, f.clock.Now().Add(time.Hour)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	_ = TriggerReload(f.dir)
	f.tickWait()

	if got := f.acct("main").Totals.Rounds; got != 1 {
		t.Errorf("a merge replaced the entry of an account the engine had: rounds = %d", got)
	}
	if got := f.acct("extra").Totals.Rounds; got != 5 {
		t.Errorf("extra rounds = %d", got)
	}
	if pendingExists(f.dir) {
		t.Error("the hand-over file was not removed")
	}
}

// An import made while the engine was stopping is adopted at the next start.
func TestStartAdoptsImportedState(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	old := store.NewState()
	old.Accounts["main"] = importedState(1, f.clock.Now().Add(time.Hour))
	_ = store.New(f.dir).SaveState(old)
	err := store.SavePendingImport(f.dir, &store.PendingImport{Mode: store.PendingReplace, Accounts: map[string]*store.AccountState{
		"main": importedState(42, f.clock.Now().Add(2*time.Hour)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.start()
	if got := f.acct("main").Totals.Rounds; got != 42 {
		t.Errorf("rounds = %d", got)
	}
	if pendingExists(f.dir) {
		t.Error("the hand-over file was not removed")
	}
	saved, _ := store.New(f.dir).LoadState()
	if saved.Accounts["main"].Totals.Rounds != 42 {
		t.Error("state.json does not hold the imported state")
	}
}

func TestDamagedHandOverIsDroppedWithoutHarm(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	st := store.NewState()
	st.Accounts["main"] = importedState(4, f.clock.Now().Add(time.Hour))
	_ = store.New(f.dir).SaveState(st)
	if err := writeFile(store.PendingImportPath(f.dir), "{not json"); err != nil {
		t.Fatal(err)
	}
	f.start()
	if got := f.acct("main").Totals.Rounds; got != 4 {
		t.Errorf("rounds = %d", got)
	}
	if pendingExists(f.dir) {
		t.Error("the damaged file was left in place")
	}
}

func writeFile(path, text string) error { return os.WriteFile(path, []byte(text), 0o600) }

func (f *fixture) countEvents(name string) int {
	es, _ := store.New(f.dir).Read(store.Filter{})
	n := 0
	for _, e := range es {
		if e.Event == name {
			n++
		}
	}
	return n
}

func asideFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "state.import.adopting-*"))
	return m
}

// Merge gap: the handover is written before the config, so the engine may see
// it first. The entries wait for their accounts, and are applied over the empty
// state the reload then creates for them.
func TestHandoverWrittenBeforeTheConfigIsAdoptedWhenTheAccountAppears(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	path := filepath.Join(f.dir, "config.toml")
	_ = f.cfg.Save(path)
	f.opts.ConfigPath = path
	f.start()
	f.round("main")

	if err := store.SavePendingImport(f.dir, &store.PendingImport{Mode: store.PendingMerge, Accounts: map[string]*store.AccountState{
		"extra": importedState(5, f.clock.Now().Add(time.Hour)),
	}}); err != nil {
		t.Fatal(err)
	}
	f.tickWait() // the account is not in the config yet
	if !store.HasPendingImport(f.dir) {
		t.Fatal("the entry was dropped before its account appeared")
	}
	if f.countEvents("state_import") != 0 {
		t.Fatal("adoption reported although nothing was adopted")
	}

	next := *f.cfg
	next.Accounts = []config.Account{delegatedAcct("main"), delegatedAcct("extra")}
	f.auths["extra"] = f.auths["main"]
	_ = next.Save(path)
	_ = TriggerReload(f.dir)
	f.tickWait()
	if got := f.acct("extra").Totals.Rounds; got != 5 {
		t.Errorf("extra rounds = %d (the empty state the reload created was kept)", got)
	}
	if store.HasPendingImport(f.dir) || len(asideFiles(t, f.dir)) != 0 {
		t.Error("files left over")
	}
	if got := f.countEvents("state_import"); got != 1 {
		t.Errorf("state_import events = %d", got)
	}
	f.tickWait()
	f.tickWait()
	if got := f.countEvents("state_import"); got != 1 {
		t.Errorf("state_import repeated: %d", got)
	}
}

// Entries for accounts that never appear are dropped after 24 hours, with one event.
func TestEntriesOfUnknownAccountsExpire(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.start()
	old := time.Now().Add(-25 * time.Hour)
	_ = store.SavePendingImport(f.dir, &store.PendingImport{Mode: store.PendingReplace, Written: old, Accounts: map[string]*store.AccountState{"ghost": importedState(1, time.Time{})}})
	f.tickWait()
	f.tickWait()
	if store.HasPendingImport(f.dir) {
		t.Error("an expired entry is still waiting")
	}
	if got := f.countEvents("state_import_expired"); got != 1 {
		t.Errorf("state_import_expired events = %d", got)
	}
	// A recent one is kept without any event.
	_ = store.SavePendingImport(f.dir, &store.PendingImport{Mode: store.PendingReplace, Accounts: map[string]*store.AccountState{"ghost": importedState(1, time.Time{})}})
	f.tickWait()
	f.tickWait()
	if !store.HasPendingImport(f.dir) || f.countEvents("state_import_expired") != 1 {
		t.Error("a recent entry was dropped or reported")
	}
}

func TestHandoverOlderThanAWeekIsIgnored(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.start()
	_ = store.SavePendingImport(f.dir, &store.PendingImport{Mode: store.PendingReplace, Written: time.Now().Add(-8 * 24 * time.Hour),
		Accounts: map[string]*store.AccountState{"main": importedState(77, f.clock.Now().Add(time.Hour))}})
	f.tickWait()
	f.tickWait()
	if f.acct("main").Totals.Rounds == 77 {
		t.Error("a week-old handover was applied")
	}
	if store.HasPendingImport(f.dir) || len(asideFiles(t, f.dir)) != 0 {
		t.Error("the old file was not deleted")
	}
	if got := f.countEvents("state_import_expired"); got != 1 {
		t.Errorf("events = %d", got)
	}
}

// Adoption is one-shot: when the save after it fails, the file is gone anyway
// and the state is saved on a later tick, not adopted again in a loop.
func TestAdoptionIsOneShotWhenTheSaveFails(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.start()
	state := filepath.Join(f.dir, "state.json")
	_ = os.Remove(state)
	if err := os.MkdirAll(filepath.Join(state, "x"), 0o700); err != nil { // saving state.json now fails
		t.Fatal(err)
	}
	_ = store.SavePendingImport(f.dir, &store.PendingImport{Mode: store.PendingReplace,
		Accounts: map[string]*store.AccountState{"main": importedState(9, f.clock.Now().Add(time.Hour))}})
	f.tickWait()
	if got := f.acct("main").Totals.Rounds; got != 9 {
		t.Fatalf("not adopted: %d", got)
	}
	if store.HasPendingImport(f.dir) || len(asideFiles(t, f.dir)) != 0 {
		t.Fatal("the handover file was kept after a failed save")
	}
	f.tickWait()
	f.tickWait()
	if got := f.countEvents("state_import"); got != 1 {
		t.Errorf("state_import events = %d", got)
	}
	// Once the disk is fine the next tick saves what is in memory.
	if err := os.RemoveAll(state); err != nil {
		t.Fatal(err)
	}
	f.tickWait()
	saved, err := store.New(f.dir).LoadState()
	if err != nil || saved.Accounts["main"] == nil || saved.Accounts["main"].Totals.Rounds != 9 {
		t.Errorf("the adopted state was never saved: %+v %v", saved, err)
	}
}

// A round in flight keeps its account out of adoption; other accounts do not wait.
func TestAdoptionWaitsForARoundInFlight(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"), delegatedAcct("other"))
	f.start()
	f.e.withLock(func() { f.e.rt["main"].running = true })
	_ = store.SavePendingImport(f.dir, &store.PendingImport{Mode: store.PendingReplace, Accounts: map[string]*store.AccountState{
		"main": importedState(7, f.clock.Now().Add(time.Hour)), "other": importedState(8, f.clock.Now().Add(time.Hour)),
	}})
	f.e.adoptPending()
	if got := f.acct("other").Totals.Rounds; got != 8 {
		t.Errorf("an account with no round in flight waited: %d", got)
	}
	if got := f.acct("main").Totals.Rounds; got == 7 {
		t.Error("adopted while its round was running")
	}
	p, _ := store.ReadPendingImport(f.dir)
	if p == nil || p.Accounts["main"] == nil || len(p.Accounts) != 1 {
		t.Fatalf("the deferred entry was not kept: %+v", p)
	}
	f.e.withLock(func() { f.e.rt["main"].running = false })
	f.e.adoptPending()
	if got := f.acct("main").Totals.Rounds; got != 7 {
		t.Errorf("not adopted after the round: %d", got)
	}
	if store.HasPendingImport(f.dir) {
		t.Error("file left")
	}
}

// A merge keeps what an account has recorded; the empty state of a new one is replaced.
func TestMergeRuleUsesActivityNotPresence(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"), delegatedAcct("fresh"))
	f.start()
	f.round("main") // main has a round, fresh has none
	_ = store.SavePendingImport(f.dir, &store.PendingImport{Mode: store.PendingMerge, Accounts: map[string]*store.AccountState{
		"main": importedState(50, f.clock.Now().Add(time.Hour)), "fresh": importedState(60, f.clock.Now().Add(time.Hour)),
	}})
	f.e.adoptPending()
	if f.acct("main").Totals.Rounds != 1 || f.acct("fresh").Totals.Rounds != 60 {
		t.Errorf("main=%d fresh=%d", f.acct("main").Totals.Rounds, f.acct("fresh").Totals.Rounds)
	}
}
