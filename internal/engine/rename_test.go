package engine

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// idAcct is a delegated account with an ID, as the add commands create them.
func idAcct(name, id string) config.Account {
	a := delegatedAcct(name)
	a.ID = id
	return a
}

// renameFixture starts an engine whose configuration lives in a file, so that a
// rename (a new config.toml) reaches it through a reload.
func renameFixture(t *testing.T, accts ...config.Account) *fixture {
	t.Helper()
	f := newFixture(t, accts...)
	path := filepath.Join(f.dir, "config.toml")
	if err := f.cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	f.opts.ConfigPath = path
	f.auths = map[string]*fakeAuth{}
	for _, a := range accts {
		f.auths[a.Key()] = &fakeAuth{tok: auth.Token{AccessToken: "t", Account: "alice@example.com",
			Scopes: delegatedScopes, ExpiresOn: f.clock.now.Add(time.Hour)}}
	}
	f.opts.NewAuth = func(a config.Account) (auth.Authenticator, error) { return f.auths[a.Key()], nil }
	return f
}

// renameTo rewrites the config file as ops.RenameAccount does and reloads the engine.
func (f *fixture) renameTo(from, to string) {
	f.t.Helper()
	cfg, err := config.Load(f.opts.ConfigPath)
	if err != nil {
		f.t.Fatal(err)
	}
	a, ok := cfg.Find(from)
	if !ok {
		f.t.Fatalf("no account %q", from)
	}
	if a.ID == "" {
		a.ID = a.Name
	}
	a.Name = to
	if err := cfg.Validate(); err != nil {
		f.t.Fatal(err)
	}
	if err := cfg.Save(f.opts.ConfigPath); err != nil {
		f.t.Fatal(err)
	}
	f.e.reloadConfig()
}

func (f *fixture) stateKeys() []string {
	f.e.mu.Lock()
	defer f.e.mu.Unlock()
	var ks []string
	for k := range f.e.state.Accounts {
		ks = append(ks, k)
	}
	return ks
}

func TestRenameSwapKeepsStateAndLogWithTheAccount(t *testing.T) {
	f := renameFixture(t, idAcct("a", "ida"), idAcct("b", "idb"))
	f.start()
	f.round("ida")
	f.round("ida")
	f.round("idb")
	if f.acct("ida").Totals.Rounds != 2 || f.acct("idb").Totals.Rounds != 1 {
		t.Fatalf("setup: %d %d", f.acct("ida").Totals.Rounds, f.acct("idb").Totals.Rounds)
	}

	f.renameTo("a", "t")
	f.renameTo("b", "a")
	f.renameTo("t", "b")

	if got := f.stateKeys(); len(got) != 2 {
		t.Errorf("state keys after the swap: %v", got)
	}
	if f.acct("ida").Totals.Rounds != 2 || f.acct("idb").Totals.Rounds != 1 {
		t.Error("state did not stay with its account")
	}
	// The next round of each books to its own state and carries the new name.
	f.round("ida")
	if f.acct("ida").Totals.Rounds != 3 || f.acct("idb").Totals.Rounds != 1 {
		t.Errorf("rounds after the swap: %d %d", f.acct("ida").Totals.Rounds, f.acct("idb").Totals.Rounds)
	}
	last := f.lastRoundEnd("ida")
	if last.Account != "b" || last.AccountID != "ida" {
		t.Errorf("round_end line: %+v", last)
	}
	// The log of the account, whatever it was called: three rounds.
	n := 0
	for _, en := range f.entries("ida") {
		if en.Event == "round_end" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("round_end lines of ida: %d", n)
	}
	if n := countRoundEnds(f.entries("idb")); n != 1 {
		t.Errorf("round_end lines of idb: %d", n)
	}
}

func countRoundEnds(es []store.Entry) int {
	n := 0
	for _, en := range es {
		if en.Event == "round_end" {
			n++
		}
	}
	return n
}

func TestRenameOfALegacyAccountKeepsStateAndOldLogLines(t *testing.T) {
	f := renameFixture(t, delegatedAcct("e5"))
	f.start()
	f.round("e5")
	if f.acct("e5").Totals.Rounds != 1 {
		t.Fatal("setup")
	}
	f.renameTo("e5", "work")
	if keys := f.stateKeys(); len(keys) != 1 || keys[0] != "e5" {
		t.Fatalf("state keys: %v", keys)
	}
	f.round("e5")
	if f.acct("e5").Totals.Rounds != 2 {
		t.Errorf("rounds: %d", f.acct("e5").Totals.Rounds)
	}
	es := f.entries("e5") // by key: the old lines (no ID, old name) and the new ones
	var names []string
	for _, en := range es {
		if en.Event == "round_end" {
			names = append(names, en.Account)
		}
	}
	if strings.Join(names, ",") != "e5,work" {
		t.Errorf("names on the round_end lines: %v", names)
	}
}

func TestRenameWhileARoundIsInFlightBooksToTheSameAccount(t *testing.T) {
	f := renameFixture(t, idAcct("a", "ida"))
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.h.setStatus(func(string) (int, map[string]string) {
		once.Do(func() { close(started) })
		<-release
		return 200, nil
	})
	f.start()
	f.clock.Advance(time.Minute)
	f.e.updateAccount("ida", func(as *store.AccountState) { as.NextRun = f.clock.Now().Add(-time.Second) })
	f.e.tick(t.Context())
	<-started

	f.renameTo("a", "z")
	close(release)
	f.e.wait()

	if keys := f.stateKeys(); len(keys) != 1 || keys[0] != "ida" {
		t.Fatalf("state keys: %v", keys)
	}
	if f.acct("ida").Totals.Rounds != 1 {
		t.Errorf("the round was booked elsewhere: %+v", f.acct("ida").Totals)
	}
	if got := f.lastRoundEnd("ida"); got.AccountID != "ida" || got.Account != "z" {
		t.Errorf("round_end: %+v", got)
	}
}

func TestPendingImportIsAdoptedAfterTheRename(t *testing.T) {
	f := renameFixture(t, idAcct("a", "ida"))
	f.start()
	// An import handed over state under the key before the rename reached the engine.
	imported := &store.AccountState{Status: store.StatusOK, Totals: store.Totals{Rounds: 40}, LastSuccess: f.clock.Now()}
	err := store.SavePendingImport(f.dir, &store.PendingImport{Mode: store.PendingReplace, Written: f.clock.Now(),
		Accounts: map[string]*store.AccountState{"ida": imported}})
	if err != nil {
		t.Fatal(err)
	}
	f.renameTo("a", "z")
	f.e.adoptPending()
	if got := f.acct("ida").Totals.Rounds; got != 40 {
		t.Errorf("rounds %d, want 40", got)
	}
	if store.HasPendingImport(f.dir) {
		t.Error("the import is still waiting")
	}
}

func TestRenamedAccountReusingAnOldNameStartsEmpty(t *testing.T) {
	f := renameFixture(t, idAcct("b", "idb1"))
	f.start()
	f.round("idb1")
	// b is removed and added again (new ID), then renamed before its first round.
	cfg := config.Default()
	cfg.Schedule = f.cfg.Schedule
	cfg.Accounts = []config.Account{idAcct("b", "idb2")}
	if err := cfg.Save(f.opts.ConfigPath); err != nil {
		t.Fatal(err)
	}
	f.e.reloadConfig()
	f.renameTo("b", "c")
	keys := f.stateKeys()
	if len(keys) != 1 || keys[0] != "idb2" {
		t.Fatalf("state keys: %v", keys)
	}
	if got := f.acct("idb2").Totals.Rounds; got != 0 {
		t.Errorf("the new account has %d rounds", got)
	}
	for _, en := range f.entries("idb2") {
		if en.Event == "round_end" {
			t.Errorf("an old line shows for the new account: %+v", en)
		}
	}
}
