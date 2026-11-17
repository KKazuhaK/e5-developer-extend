package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-renew/internal/auth"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/engine"
	"github.com/KKazuhaK/e5-renew/internal/store"
)

// While an engine holds the lock, an import must not write state.json (the
// engine would save its own copy over it); it hands the state over in a file.
func TestImportWhileEngineHoldsTheLockHandsTheStateOver(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.addSecretAccount("old", bkSecretOld)
	b.saveState(map[string]int{"old": 11})
	stateBefore := mustRead(t, filepath.Join(b.dataDir, "state.json"))

	release, err := engine.AcquireLock(b.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if _, err := b.importFrom(file, ImportReplace); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, filepath.Join(b.dataDir, "state.json")); string(got) != string(stateBefore) {
		t.Error("state.json was written under a running engine")
	}
	p, err := store.ReadPendingImport(b.dataDir)
	if err != nil || p == nil {
		t.Fatalf("no hand-over file: %v", err)
	}
	if p.Mode != store.PendingReplace || p.Accounts["main"].Totals.Rounds != 7 || p.Accounts["dev"].Totals.Rounds != 3 || len(p.Accounts) != 2 {
		t.Errorf("hand-over: %+v", p)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(store.PendingImportPath(b.dataDir)); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", fi.Mode().Perm())
		}
	}
	// The other files are still written directly.
	if got := string(mustRead(t, filepath.Join(b.cfgDir, "gui.json"))); got == "" {
		t.Error("gui.json missing")
	}
}

func TestMergeWhileEngineHoldsTheLockHandsOverOnlyNewAccounts(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.addSecretAccount("mine", bkSecretOld)
	b.saveState(map[string]int{"mine": 11})
	release, err := engine.AcquireLock(b.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := b.importFrom(file, ImportMerge); err != nil {
		t.Fatal(err)
	}
	p, _ := store.ReadPendingImport(b.dataDir)
	if p == nil || p.Mode != store.PendingMerge || len(p.Accounts) != 2 || p.Accounts["main"] == nil {
		t.Fatalf("hand-over: %+v", p)
	}
	if b.state().Accounts["main"] != nil {
		t.Error("state.json was written under a running engine")
	}
}

// Without an engine the state is written directly, and a stale hand-over file is dropped.
func TestImportWithoutEngineWritesStateDirectly(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.use()
	if err := store.SavePendingImport(b.dataDir, &store.PendingImport{Mode: store.PendingReplace,
		Accounts: map[string]*store.AccountState{"ghost": {Status: store.StatusOK}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.importFrom(file, ImportDefault); err != nil {
		t.Fatal(err)
	}
	if b.state().Accounts["main"].Totals.Rounds != 7 {
		t.Error("state.json not written")
	}
	if store.HasPendingImport(b.dataDir) {
		t.Error("a stale hand-over file was left")
	}
}

// A real engine in the loop: it runs with its own copy of the state, the
// import arrives, and what ends up in state.json is the imported state, also
// after the engine has saved again.
func TestImportIntoARunningEngineSurvivesItsSaves(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.addSecretAccount("old", bkSecretOld)
	b.saveState(map[string]int{"old": 11})
	b.use()

	cfg := b.config()
	opts := engine.DefaultOptions()
	opts.Config, opts.ConfigPath, opts.DataDir = cfg, filepath.Join(b.cfgDir, "config.toml"), b.dataDir
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
	waitFor(t, "the engine to start", func() bool { return engine.Running(b.dataDir) && hasEvent(b.dataDir, "start") })

	if _, err := b.importFrom(file, ImportReplace); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the engine to adopt the imported state", func() bool {
		st, err := store.New(b.dataDir).LoadState()
		return err == nil && !store.HasPendingImport(b.dataDir) && st.Accounts["main"] != nil && st.Accounts["main"].Totals.Rounds == 7
	})
	// Let the engine save a few more times, then stop it (which saves once more).
	time.Sleep(300 * time.Millisecond)
	stop()
	st := b.state()
	if st.Accounts["old"] != nil || st.Accounts["main"] == nil || st.Accounts["main"].Totals.Rounds != 7 || st.Accounts["dev"].Totals.Rounds != 3 {
		t.Errorf("state.json after the engine stopped: %+v", st.Accounts)
	}
}

func hasEvent(dir, name string) bool {
	es, _ := store.New(dir).Read(store.Filter{})
	for _, e := range es {
		if e.Event == name {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// With an engine running the handover exists before config.toml is saved, and a
// failed save takes it back.
func TestHandoverIsWrittenBeforeTheConfigAndRemovedWhenSavingFails(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.addSecretAccount("mine", bkSecretOld)
	release, err := engine.AcquireLock(b.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	old := engineProbeWait
	engineProbeWait = 100 * time.Millisecond
	defer func() { engineProbeWait = old }()

	var seen bool
	b.Ops.SaveConfig = func(c *config.Config, path string) error {
		seen = store.HasPendingImport(b.dataDir)
		return errors.New("disk full")
	}
	if _, err := b.importFrom(file, ImportMerge); err == nil {
		t.Fatal("succeeded")
	}
	if !seen {
		t.Error("the handover did not exist when the config was saved")
	}
	if store.HasPendingImport(b.dataDir) {
		t.Error("the handover was left after the import failed")
	}
	b.Ops.SaveConfig = func(c *config.Config, path string) error {
		seen = store.HasPendingImport(b.dataDir)
		return c.Save(path)
	}
	seen = false
	if _, err := b.importFrom(file, ImportMerge); err != nil {
		t.Fatal(err)
	}
	if !seen || !store.HasPendingImport(b.dataDir) {
		t.Errorf("before save: %v, after: %v", seen, store.HasPendingImport(b.dataDir))
	}
}

// A probe that holds the engine lock for a moment is not mistaken for an engine.
func TestBriefLockHolderIsNotTakenForAnEngine(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.use()
	release, err := engine.AcquireLock(b.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	old := engineProbeWait
	engineProbeWait = 3 * time.Second
	defer func() { engineProbeWait = old }()
	go func() {
		time.Sleep(200 * time.Millisecond)
		release()
	}()
	if _, err := b.importFrom(file, ImportDefault); err != nil {
		t.Fatal(err)
	}
	if store.HasPendingImport(b.dataDir) {
		t.Error("handed the state to an engine that was only a probe")
	}
	if b.state().Accounts["main"] == nil {
		t.Error("state.json was not written")
	}
}

func TestLongLockHolderIsAnEngine(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.use()
	release, err := engine.AcquireLock(b.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	old := engineProbeWait
	engineProbeWait = 150 * time.Millisecond
	defer func() { engineProbeWait = old }()
	start := time.Now()
	if _, err := b.importFrom(file, ImportDefault); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 150*time.Millisecond || !store.HasPendingImport(b.dataDir) {
		t.Error("an engine that kept the lock was not recognised after waiting")
	}
}

// The lock of a token cache is held from the snapshot until the import is done,
// so an engine's refresh cannot slip in between.
func TestTokenCacheLocksAreHeldThroughTheImport(t *testing.T) {
	file := sourceMachine(t).export(false) // has tokencache/main and tokencache/dev
	b := newMachine(t)
	b.addSecretAccount("old", bkSecretOld)
	b.put("tokencache/old", "OLD-CACHE")
	b.use()
	v := b.vault(b.key)
	var blocked []string
	b.Ops.SaveConfig = func(c *config.Config, path string) error {
		for _, k := range []string{"tokencache/main", "tokencache/dev", "tokencache/old"} { // two new, one stale
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			unlock, err := v.Lock(ctx, k)
			cancel()
			if err == nil {
				unlock()
			} else {
				blocked = append(blocked, k)
			}
		}
		return c.Save(path)
	}
	if _, err := b.importFrom(file, ImportReplace); err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 3 {
		t.Errorf("locks held for %v only", blocked)
	}
	// And they are free again afterwards (the engine reload comes after).
	for _, k := range []string{"tokencache/main", "tokencache/dev"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		unlock, err := v.Lock(ctx, k)
		cancel()
		if err != nil {
			t.Fatalf("%s still locked: %v", k, err)
		}
		unlock()
	}
}

// An engine refreshing a token cache holds its lock: the import waits for it.
func TestImportWaitsForATokenRefresh(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.use()
	v := b.vault(b.key)
	unlock, err := v.Lock(context.Background(), "tokencache/main")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := b.ImportBackup(context.Background(), file, bkPass, ImportDefault); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("the import did not wait for the lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
