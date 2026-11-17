package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func TestRenameTriggersFollowTheAccount(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []func(string, string) error{TriggerRun, TriggerResume, TriggerSubscription, TriggerPasswordReset} {
		if err := f(dir, "old"); err != nil {
			t.Fatal(err)
		}
	}
	if err := TriggerResume(dir, "new"); err != nil { // a request under the new name wins
		t.Fatal(err)
	}
	RenameTriggers(dir, "old", "new")
	for _, n := range []string{"new", "new.resume", "new.subscription", "new.pwreset"} {
		if !fileExists(filepath.Join(triggerDir(dir), n)) {
			t.Errorf("%s is missing", n)
		}
	}
	for _, n := range []string{"old", "old.resume", "old.subscription", "old.pwreset"} {
		if fileExists(filepath.Join(triggerDir(dir), n)) {
			t.Errorf("%s was left", n)
		}
	}
}

func (f *fixture) renameConfig(path string, to ...string) {
	f.t.Helper()
	next := *f.cfg
	next.Accounts = nil
	for _, n := range to {
		next.Accounts = append(next.Accounts, delegatedAcct(n))
		if f.auths[n] == nil {
			f.auths[n] = f.auths["main"]
		}
	}
	if err := next.Save(path); err != nil {
		f.t.Fatal(err)
	}
}

// The state of a renamed account is moved when the engine loads the config that
// has the new name; no alert is raised for the rename.
func TestReloadAppliesRenameAndKeepsTheState(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	path := filepath.Join(f.dir, "config.toml")
	_ = f.cfg.Save(path)
	f.opts.ConfigPath = path
	f.start()
	f.round("main")
	if f.acct("main").Totals.Rounds != 1 {
		t.Fatal("setup: no round ran")
	}

	// The config is saved first, then the hand-over is written.
	f.renameConfig(path, "renamed")
	if err := store.SavePendingRenames(f.dir, time.Time{}, []store.Rename{{From: "main", To: "renamed"}}); err != nil {
		t.Fatal(err)
	}
	_ = TriggerReload(f.dir)
	f.tickWait()

	if store.HasPendingRename(f.dir) {
		t.Error("the rename file was not removed")
	}
	got := f.acct("renamed")
	if got.Totals.Rounds != 1 || got.Status != store.StatusOK {
		t.Errorf("renamed: %+v", got)
	}
	saved, err := store.New(f.dir).LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Accounts["main"] != nil || saved.Accounts["renamed"] == nil || saved.RenamedFrom["renamed"][0] != "main" {
		t.Errorf("state.json: %+v %v", saved.Accounts, saved.RenamedFrom)
	}
	if !f.hasEvent("account_renamed") {
		t.Error("no account_renamed event")
	}
	// The old name's calls still count for the new one.
	if es, _ := store.New(f.dir).Read(store.Filter{Account: "renamed"}); len(es) == 0 {
		t.Error("the log of the old name is not found under the new one")
	}
}

// The engine loads the config with the new name a moment before the hand-over
// arrives: the state it had to set aside is found again when the rename comes.
func TestRenameArrivingAfterTheConfigStillFindsTheState(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	path := filepath.Join(f.dir, "config.toml")
	_ = f.cfg.Save(path)
	f.opts.ConfigPath = path
	f.start()
	f.round("main")

	f.renameConfig(path, "renamed")
	_ = TriggerReload(f.dir)
	f.tickWait()
	if got := f.acct("renamed"); got.Totals.Rounds != 0 {
		t.Fatalf("setup: the new name should start empty: %+v", got)
	}
	if err := store.SavePendingRenames(f.dir, time.Time{}, []store.Rename{{From: "main", To: "renamed"}}); err != nil {
		t.Fatal(err)
	}
	_ = TriggerReload(f.dir)
	f.tickWait()
	if got := f.acct("renamed"); got.Totals.Rounds != 1 {
		t.Errorf("the state was lost: %+v", got)
	}
}

// The other order: the rename is claimed while the loaded config still says
// "main". The renamed state is kept until a config has the new name.
func TestRenameClaimedBeforeTheConfigHasTheNewName(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	path := filepath.Join(f.dir, "config.toml")
	_ = f.cfg.Save(path)
	f.opts.ConfigPath = path
	f.start()
	f.round("main")

	if err := store.SavePendingRenames(f.dir, time.Time{}, []store.Rename{{From: "main", To: "renamed"}}); err != nil {
		t.Fatal(err)
	}
	_ = TriggerReload(f.dir) // the config still says "main"
	f.tickWait()
	f.renameConfig(path, "renamed")
	_ = TriggerReload(f.dir)
	f.tickWait()
	if got := f.acct("renamed"); got.Totals.Rounds != 1 {
		t.Errorf("the state was lost: %+v", got)
	}
}

// A round in flight when the rename is applied stays marked as running, so no
// second round starts, and books its result under the new name.
func TestRenameKeepsARoundInFlight(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	path := filepath.Join(f.dir, "config.toml")
	_ = f.cfg.Save(path)
	f.opts.ConfigPath = path
	f.start()
	f.round("main")
	hits := f.h.count("")

	gate := make(chan struct{})
	f.h.setStatus(func(string) (int, map[string]string) {
		<-gate
		return 200, nil
	})
	f.clock.Advance(time.Minute)
	f.e.updateAccount(context.Background(), "main", func(as *store.AccountState) { as.NextRun = f.clock.Now().Add(-time.Second) })
	f.e.tick(context.Background()) // the round starts and waits at the first call
	waitUntil(t, func() bool { return f.h.count("") > hits })

	f.renameConfig(path, "renamed")
	if err := store.SavePendingRenames(f.dir, time.Time{}, []store.Rename{{From: "main", To: "renamed"}}); err != nil {
		t.Fatal(err)
	}
	_ = TriggerReload(f.dir)
	f.e.tick(context.Background()) // applies the rename; must not start another round
	f.e.mu.Lock()
	rt := f.e.rt["renamed"]
	running := rt != nil && rt.running
	f.e.mu.Unlock()
	if !running {
		t.Error("the round in flight is not marked as running under the new name")
	}
	f.clock.Advance(time.Minute)
	f.e.updateAccount(context.Background(), "renamed", func(as *store.AccountState) { as.NextRun = f.clock.Now().Add(-time.Second) })
	f.e.tick(context.Background())
	close(gate)
	f.e.wait()
	got := f.acct("renamed")
	if got.Totals.Rounds != 2 {
		t.Errorf("rounds = %d: the in-flight result was lost or a second round ran", got.Totals.Rounds)
	}
	f.e.mu.Lock()
	left := f.e.state.Accounts["main"]
	f.e.mu.Unlock()
	if left != nil {
		t.Errorf("state left under the old name: %+v", left)
	}
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// A rename made while the engine was stopped is applied at the next start.
func TestStartAppliesRename(t *testing.T) {
	f := newFixture(t, delegatedAcct("renamed"))
	old := store.NewState()
	old.Accounts["main"] = importedState(8, f.clock.Now().Add(time.Hour))
	_ = store.New(f.dir).SaveState(old)
	if err := store.SavePendingRenames(f.dir, time.Time{}, []store.Rename{{From: "main", To: "renamed"}}); err != nil {
		t.Fatal(err)
	}
	f.start()
	if got := f.acct("renamed").Totals.Rounds; got != 8 {
		t.Errorf("rounds = %d", got)
	}
	if store.HasPendingRename(f.dir) {
		t.Error("the rename file was not removed")
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// A name that was renamed to and later removed starts empty when it is added
// again: nothing of the removed account, its password block least of all, comes back.
func TestReAddedNameDoesNotInheritARemovedRenamedAccount(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"), delegatedAcct("other"))
	path := filepath.Join(f.dir, "config.toml")
	_ = f.cfg.Save(path)
	f.opts.ConfigPath = path
	f.start()
	f.round("main")
	f.e.updateAccount(context.Background(), "main", func(as *store.AccountState) {
		as.PasswordBlocked = &store.PasswordBlock{Code: "AADSTS50126", At: f.clock.Now()}
	})

	f.renameConfig(path, "renamed", "other")
	if err := store.SavePendingRenames(f.dir, time.Time{}, []store.Rename{{From: "main", To: "renamed"}}); err != nil {
		t.Fatal(err)
	}
	_ = TriggerReload(f.dir)
	f.tickWait()
	if f.acct("renamed").PasswordBlocked == nil {
		t.Fatal("setup: the block did not follow the account")
	}

	f.renameConfig(path, "other") // removed
	_ = TriggerReload(f.dir)
	f.tickWait()
	f.renameConfig(path, "renamed", "other") // added again
	_ = TriggerReload(f.dir)
	f.tickWait()
	if got := f.acct("renamed"); got.PasswordBlocked != nil || got.Totals.Rounds != 0 {
		t.Errorf("the new account inherited the removed one: %+v", got)
	}
}

// A rename written before its config was saved (and never committed) is not
// applied while the config still has the old name.
func TestUncommittedRenameIsIgnoredUntilTheConfigHasIt(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	path := filepath.Join(f.dir, "config.toml")
	_ = f.cfg.Save(path)
	f.opts.ConfigPath = path
	f.start()
	f.round("main")
	if err := store.SavePendingRenames(f.dir, time.Time{}, []store.Rename{{From: "main", To: "renamed", Uncommitted: true}}); err != nil {
		t.Fatal(err)
	}
	_ = TriggerReload(f.dir)
	f.tickWait()
	if got := f.acct("main"); got.Totals.Rounds != 1 {
		t.Errorf("a rename the config never got to was applied: %+v", got)
	}
	if store.HasPendingRename(f.dir) {
		t.Error("the file was left")
	}
}
