package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// renameFixture is a machine with the account "old": a stored secret, a token
// cache, state with statistics and subscription keys, and a log entry.
func renameFixture(t *testing.T) *machine {
	t.Helper()
	m := newMachine(t)
	m.addSecretAccount("old", bkSecretOld)
	m.addSecretAccount("other", bkSecretMain)
	m.put("tokencache/old", "TOKENCACHE-OLD")
	st := store.NewState()
	st.Accounts["old"] = &store.AccountState{
		Status: store.StatusNeedsAttention, Reason: "x", Totals: store.Totals{Rounds: 5, OK: 4, Failed: 1},
		LastPasswordCode: "AADSTS50126",
		Notified:         map[string]store.Notice{"needs_attention": {At: time.Now(), Key: "x", Pending: true, Msg: "m"}},
	}
	st.Accounts["other"] = &store.AccountState{Status: store.StatusOK, Totals: store.Totals{Rounds: 2}}
	st.Tenants = map[string]*store.TenantState{"account:old": {Access: store.AccessOK}}
	st.TenantAliases = map[string]string{"account:old": "guid-1"}
	if err := store.New(m.dataDir).SaveState(st); err != nil {
		t.Fatal(err)
	}
	if err := store.New(m.dataDir).Append(store.Entry{TS: time.Now(), Account: "old", API: "me", Status: 200}); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *machine) rename(from, to string) ([]string, error) {
	m.t.Helper()
	m.use()
	return m.RenameAccount(context.Background(), from, to)
}

func TestRenameAccountWithoutEngine(t *testing.T) {
	m := renameFixture(t)
	m.use()
	if err := engine.TriggerRun(m.dataDir, "old"); err != nil {
		t.Fatal(err)
	}
	if err := engine.TriggerPasswordReset(m.dataDir, "old"); err != nil {
		t.Fatal(err)
	}
	if err := store.SavePendingImport(m.dataDir, &store.PendingImport{Mode: store.PendingMerge,
		Accounts: map[string]*store.AccountState{"old": {Status: store.StatusOK}}}); err != nil {
		t.Fatal(err)
	}

	warns, err := m.rename("old", "fresh")
	if err != nil || len(warns) > 0 {
		t.Fatalf("rename: %v %v", err, warns)
	}

	cfg := m.config()
	if _, ok := cfg.Find("old"); ok {
		t.Error("old is still in the config")
	}
	if a, ok := cfg.Find("fresh"); !ok || a.Auth != config.AuthClientSecret || a.ClientID != testGUID {
		t.Errorf("fresh: %+v", a)
	}
	if got := m.mustGet("secret/fresh"); got != bkSecretOld {
		t.Errorf("secret: %q", got)
	}
	if got := m.mustGet("tokencache/fresh"); got != "TOKENCACHE-OLD" {
		t.Errorf("token cache: %q", got)
	}
	for _, k := range []string{"secret/old", "tokencache/old"} {
		if _, err := m.get(k); !errors.Is(err, vault.ErrNotFound) {
			t.Errorf("%s was kept: %v", k, err)
		}
	}
	if m.mustGet("secret/other") != bkSecretMain {
		t.Error("another account's secret changed")
	}

	st := m.state()
	if st.Accounts["old"] != nil {
		t.Error("state of the old name is still there")
	}
	as := st.Accounts["fresh"]
	if as == nil || as.Totals.Rounds != 5 || as.Status != store.StatusNeedsAttention || as.LastPasswordCode != "AADSTS50126" ||
		!as.Notified["needs_attention"].Pending || as.Notified["needs_attention"].Msg != "m" {
		t.Errorf("state of fresh: %+v", as)
	}
	if st.TenantAliases["account:fresh"] != "guid-1" || st.TenantAliases["account:old"] != "" || st.Tenants["account:fresh"] == nil || st.Tenants["account:old"] != nil {
		t.Errorf("tenant keys: %v %v", st.TenantAliases, st.Tenants)
	}
	if got := st.RenamedFrom["fresh"]; len(got) != 1 || got[0] != "old" {
		t.Errorf("renamed_from: %v", st.RenamedFrom)
	}
	if st.Accounts["other"].Totals.Rounds != 2 {
		t.Error("another account's state changed")
	}
	if store.HasPendingRename(m.dataDir) {
		t.Error("the rename file was left")
	}

	// Triggers follow the account; the imported state waiting for an engine too.
	for _, f := range []string{"fresh", "fresh.pwreset"} {
		if _, err := os.Stat(filepath.Join(m.dataDir, "trigger", f)); err != nil {
			t.Errorf("trigger %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(m.dataDir, "trigger", "old")); err == nil {
		t.Error("the trigger of the old name was left")
	}
	if p, _ := store.ReadPendingImport(m.dataDir); p == nil || p.Accounts["fresh"] == nil || p.Accounts["old"] != nil {
		t.Errorf("pending import: %+v", p)
	}

	// The log keeps the old name, and the new one finds it.
	es, err := store.New(m.dataDir).Read(store.Filter{Account: "fresh"})
	if err != nil || len(es) != 1 || es[0].Account != "old" {
		t.Errorf("log of fresh: %v %v", es, err)
	}
	if es, _ := store.New(m.dataDir).Read(store.Filter{Account: "other"}); len(es) != 0 {
		t.Errorf("log of other: %v", es)
	}
}

func TestRenameAccountWithoutAnyStateOrCache(t *testing.T) {
	m := newMachine(t)
	m.addSecretAccount("a", bkSecretOld)
	if _, err := m.rename("a", "b"); err != nil {
		t.Fatal(err)
	}
	if m.mustGet("secret/b") != bkSecretOld {
		t.Error("secret not moved")
	}
	if ok, _ := m.vault(m.key).Has("tokencache/b"); ok {
		t.Error("a token cache appeared")
	}
}

func TestRenameAccountChainsAndForgetsLeftoverRenames(t *testing.T) {
	m := renameFixture(t)
	if _, err := m.rename("old", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.rename("second", "third"); err != nil {
		t.Fatal(err)
	}
	st := m.state()
	if st.Accounts["third"].Totals.Rounds != 5 || len(st.Accounts) != 2 {
		t.Errorf("accounts: %+v", st.Accounts)
	}
	if got := st.RenamedFrom["third"]; len(got) != 2 || got[0] != "old" || got[1] != "second" {
		t.Errorf("renamed_from: %v", st.RenamedFrom)
	}
	if es, _ := store.New(m.dataDir).Read(store.Filter{Account: "third"}); len(es) != 1 {
		t.Errorf("log: %v", es)
	}

	// A rename an engine never applied (it stopped before it looked) is
	// joined with the next one, and applied at once without an engine.
	m2 := renameFixture(t)
	m2.use()
	if err := store.SavePendingRenames(m2.dataDir, time.Time{}, []store.Rename{{From: "old", To: "mid"}}); err != nil {
		t.Fatal(err)
	}
	m2.edit(func(c *config.Config) { c.Accounts[0].Name = "mid" })
	m2.put("secret/mid", bkSecretOld)
	if _, err := m2.rename("mid", "last"); err != nil {
		t.Fatal(err)
	}
	st = m2.state()
	if st.Accounts["last"] == nil || st.Accounts["last"].Totals.Rounds != 5 || st.Accounts["old"] != nil || st.Accounts["mid"] != nil {
		t.Errorf("accounts: %+v", st.Accounts)
	}
}

func TestRenameAccountRejectsBadNames(t *testing.T) {
	m := renameFixture(t)
	for name, want := range map[string]string{
		"other":                 "already exists",
		"Bad Name":              "lowercase",
		"":                      "name",
		"old":                   "same",
		strings.Repeat("a", 65): "1-64",
		"con":                   "reserved",
		"has/slash":             "lowercase",
		"../escape":             "lowercase",
	} {
		before := m.snapshot()
		_, err := m.rename("old", name)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("rename to %q: %v (want %q)", name, err, want)
		}
		m.mustNotChange(before)
	}
	if _, err := m.rename("nobody", "fresh"); err == nil || !strings.Contains(err.Error(), "no account named") {
		t.Errorf("unknown account: %v", err)
	}
}

func TestRenameAccountRollsBackWhenTheConfigCannotBeSaved(t *testing.T) {
	m := renameFixture(t)
	before := m.snapshot()
	var pendingSeen bool
	m.Ops.SaveConfig = func(c *config.Config, path string) error {
		pendingSeen = store.HasPendingRename(m.dataDir)
		return errors.New("disk full")
	}
	_, err := m.rename("old", "fresh")
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("rename: %v", err)
	}
	if !pendingSeen {
		t.Error("the state was not handed over before the config was saved")
	}
	m.mustNotChange(before) // config, vault entries, state and no hand-over file
	if m.mustGet("secret/old") != bkSecretOld || m.mustGet("tokencache/old") != "TOKENCACHE-OLD" {
		t.Error("old vault entries changed")
	}
	if ok, _ := m.vault(m.key).Has("secret/fresh"); ok {
		t.Error("copy left behind")
	}
}

// A leftover entry of an account that was removed earlier must not turn up in the renamed one.
func TestRenameAccountDoesNotInheritLeftoverVaultEntries(t *testing.T) {
	m := newMachine(t)
	m.addSecretAccount("a", bkSecretOld)
	m.put("tokencache/b", "LEFTOVER")
	if _, err := m.rename("a", "b"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.vault(m.key).Has("tokencache/b"); ok {
		t.Error("leftover token cache of the new name was kept")
	}
}

func TestRenameAccountDropsAnUnreadableTokenCacheWithAWarning(t *testing.T) {
	m := renameFixture(t)
	other, err := vault.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.vault(other).Put("tokencache/old", []byte("x")); err != nil { // encrypted with another key
		t.Fatal(err)
	}
	warns, err := m.rename("old", "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "account login fresh") {
		t.Errorf("warnings: %v", warns)
	}
	if m.mustGet("secret/fresh") != bkSecretOld {
		t.Error("secret not moved")
	}
}

// A token refresh holds the lock of the token cache; the rename waits for it
// instead of copying a cache that is about to change.
func TestRenameAccountWaitsForARunningTokenRefresh(t *testing.T) {
	m := renameFixture(t)
	m.use()
	release, err := m.vault(m.key).Lock(context.Background(), "tokencache/old")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.RenameAccount(context.Background(), "old", "fresh"); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("the rename did not wait for the refresh: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, ok := m.config().Find("old"); !ok {
		t.Error("the config changed while the lock was held")
	}
	// The refresh writes its new cache, then lets go.
	m.put("tokencache/old", "TOKENCACHE-REFRESHED")
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the rename never finished")
	}
	if got := m.mustGet("tokencache/fresh"); got != "TOKENCACHE-REFRESHED" {
		t.Errorf("the renamed cache is not the refreshed one: %q", got)
	}
}

func TestRenameAccountGivesUpWhenTheLockIsNotReleased(t *testing.T) {
	m := renameFixture(t)
	m.use()
	release, err := m.vault(m.key).Lock(context.Background(), "tokencache/old")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	before := m.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := m.RenameAccount(ctx, "old", "fresh"); err == nil {
		t.Fatal("renamed while the lock was held")
	}
	m.mustNotChange(before)
}

// The engine holds the lock: the state goes to it in a file, written before
// config.toml, and state.json is not touched.
func TestRenameAccountWhileEngineHoldsTheLockHandsTheRenameOver(t *testing.T) {
	m := renameFixture(t)
	release, err := engine.AcquireLock(m.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	stateBefore := mustRead(t, filepath.Join(m.dataDir, "state.json"))
	var seen bool
	m.Ops.SaveConfig = func(c *config.Config, path string) error {
		seen = store.HasPendingRename(m.dataDir)
		return c.Save(path)
	}
	if _, err := m.rename("old", "fresh"); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Error("no hand-over file when the config was saved")
	}
	if string(mustRead(t, filepath.Join(m.dataDir, "state.json"))) != string(stateBefore) {
		t.Error("state.json was written under a running engine")
	}
	p, err := store.ReadPendingRenameFile(store.PendingRenamePath(m.dataDir))
	if err != nil || p == nil || len(p.Renames) != 1 || p.Renames[0] != (store.Rename{From: "old", To: "fresh"}) {
		t.Errorf("hand-over: %+v %v", p, err)
	}
}

// A real engine in the loop: it keeps its own copy of the state, the rename
// arrives, and what ends up in state.json is the renamed state, also after the
// engine has saved again. Nothing may alert because of the rename.
func TestRenameAccountIntoARunningEngine(t *testing.T) {
	m := renameFixture(t)
	m.use()
	cfg := m.config()
	opts := engine.DefaultOptions()
	opts.Config, opts.ConfigPath, opts.DataDir = cfg, filepath.Join(m.cfgDir, "config.toml"), m.dataDir
	opts.NewAuth = func(config.Account) (auth.Authenticator, error) { return nil, errors.New("no sign-in in this test") }
	opts.Tick, opts.ConfigCheckEvery = 20*time.Millisecond, 1
	e, err := engine.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}
	defer stop()
	waitFor(t, "the engine to start", func() bool { return engine.Running(m.dataDir) && hasEvent(m.dataDir, "start") })

	if _, err := m.RenameAccount(context.Background(), "old", "fresh"); err != nil {
		t.Fatal(err)
	}
	m.ReloadEngine()
	waitFor(t, "the engine to apply the rename", func() bool {
		st, err := store.New(m.dataDir).LoadState()
		return err == nil && !store.HasPendingRename(m.dataDir) && st.Accounts["fresh"] != nil && st.Accounts["fresh"].Totals.Rounds == 5
	})
	time.Sleep(300 * time.Millisecond) // more saves of the engine
	stop()
	st := m.state()
	if st.Accounts["old"] != nil || st.Accounts["fresh"] == nil || st.Accounts["fresh"].Totals.Rounds != 5 ||
		st.Accounts["fresh"].Status != store.StatusNeedsAttention || st.Accounts["other"].Totals.Rounds != 2 {
		t.Errorf("state.json after the engine stopped: %+v", st.Accounts)
	}
	if got := st.RenamedFrom["fresh"]; len(got) != 1 || got[0] != "old" {
		t.Errorf("renamed_from %v", st.RenamedFrom)
	}
	if !hasEvent(m.dataDir, "account_renamed") {
		t.Error("no account_renamed event")
	}
	for _, bad := range []string{"alert", "needs_attention", "recovered"} {
		if es, _ := store.New(m.dataDir).Read(store.Filter{Account: "fresh"}); containsEvent(es, bad) {
			t.Errorf("the rename raised %s", bad)
		}
	}
}

func containsEvent(es []store.Entry, name string) bool {
	for _, e := range es {
		if e.Event == name {
			return true
		}
	}
	return false
}
