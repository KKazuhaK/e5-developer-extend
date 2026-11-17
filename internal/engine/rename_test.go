package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
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

	// The hand-over comes first: a reload before the config is saved keeps it.
	if err := store.SavePendingRenames(f.dir, time.Time{}, []store.Rename{{From: "main", To: "renamed"}}); err != nil {
		t.Fatal(err)
	}
	_ = TriggerReload(f.dir)
	f.tickWait()
	if !store.HasPendingRename(f.dir) {
		t.Error("the rename was dropped before the config had the new name")
	}
	if f.acct("main").Totals.Rounds != 1 {
		t.Error("the state moved before the config had the new name")
	}

	next := *f.cfg
	next.Accounts = []config.Account{delegatedAcct("renamed")}
	f.auths["renamed"] = f.auths["main"]
	if err := next.Save(path); err != nil {
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
