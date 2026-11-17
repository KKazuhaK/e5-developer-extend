package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// RenameAccount gives an account a new name. The name is the key of the
// account everywhere, so this moves everything that hangs on it: the entry in
// config.toml, the stored secret and token cache in the vault, and the state
// (statistics, alert notices, password-gate state, subscription aliases).
//
// Order, so that a failure leaves the old name fully working: the vault
// entries are copied to the new names, the trigger files and a waiting import
// are renamed, config.toml is saved, and only then is the state handed over
// and are the old vault entries deleted. If anything before the config is
// saved fails, all of it is put back and nothing has changed.
//
// The state belongs to the engine, which keeps it in memory. With an engine
// running the rename goes to it in a file (store.PendingRenameFile), written
// after config.toml so that every rename in the file is one the config has;
// the engine applies the renames in order when it reloads (and if its config
// was loaded a moment too early it sets the state aside until the rename
// arrives). Without an engine the state is rewritten here, with the engine
// lock held so none starts halfway. Call ReloadEngine afterwards.
//
// The call log keeps the old name in the entries already written. The state
// remembers the old name (store.State.RenamedFrom), so reading the log of the
// new name also returns those entries.
//
// Warnings are problems after the rename succeeded (an old vault entry that
// could not be deleted, state that could not be carried over).
func (o *Ops) RenameAccount(ctx context.Context, from, to string) (warnings []string, err error) {
	if from == to {
		return nil, errors.New("the new name is the same as the old one")
	}
	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	cfg, cfgPath, err := o.LoadConfig()
	if err != nil {
		return nil, err
	}
	cur, err := o.FindAccount(cfg, from)
	if err != nil {
		return nil, err
	}
	if _, exists := cfg.Find(to); exists {
		return nil, fmt.Errorf("account %q already exists", to)
	}
	renamed := *cur
	renamed.Name = to
	if err := ValidateAccount(renamed); err != nil {
		return nil, err
	}
	*cur = renamed
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid account:\n%w", err)
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return nil, err
	}

	v, err := o.OpenVault(cfg)
	if err != nil {
		return nil, err
	}
	// A stored secret under the new name belongs to something else: a setup that
	// is creating that account right now (it stores the secret first), or the
	// leftover of an account whose removal could not delete it. Neither may be
	// overwritten or inherited.
	if ok, err := v.Has(vault.SecretKey(to)); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("a stored secret already exists under the name %q (an app setup for it may be in progress);\n"+
			"finish or cancel that, or choose another name", to)
	}
	// The pairs to move, and every key that is touched.
	pairs := [][2]string{
		{vault.SecretKey(from), vault.SecretKey(to)},
		{vault.TokenCacheKey(from), vault.TokenCacheKey(to)},
	}
	var keys []string
	for _, p := range pairs {
		keys = append(keys, p[0], p[1])
	}
	// The engine takes this lock while it refreshes a token; holding it for both
	// names keeps a refresh from writing the cache between the copy and the delete.
	unlockKeys, err := lockVaultKeys(ctx, v, keys)
	if err != nil {
		return nil, err
	}
	defer unlockKeys()
	undoVault, err := snapshotVault(v, keys)
	if err != nil {
		return nil, fmt.Errorf("read the vault entries: %w", err)
	}
	var undos []func() // everything done so far that an abort takes back
	abort := func(cause error) error {
		for i := len(undos) - 1; i >= 0; i-- {
			undos[i]()
		}
		if failed := undoVault.restore(); len(failed) > 0 {
			return fmt.Errorf("%w\nthe vault entries %v could not be put back; run `e5extend doctor`", cause, failed)
		}
		return fmt.Errorf("%w\nnothing was changed", cause)
	}

	// A token cache left under the new name by an earlier account would otherwise
	// be taken for the renamed account's.
	if undoVault.had[vault.TokenCacheKey(to)] {
		if err := v.Delete(vault.TokenCacheKey(to)); err != nil && !errors.Is(err, vault.ErrNotFound) {
			return nil, abort(err)
		}
	}
	var dropped bool // the old token cache could not be read, so it was not copied
	for _, p := range pairs {
		src, dst := p[0], p[1]
		if !undoVault.had[src] {
			continue
		}
		// The entry is encrypted together with its name, so it is read and written again.
		val, err := v.Get(src)
		switch {
		case err == nil:
		case src == vault.TokenCacheKey(from) && (errors.Is(err, vault.ErrWrongKey) || errors.Is(err, vault.ErrCorrupt)):
			// An unreadable sign-in cannot be moved; it needs a new login either way.
			dropped = true
			continue
		default:
			return nil, abort(WithHint(fmt.Errorf("read vault entry %s: %w", src, err), from))
		}
		if err := v.Put(dst, val); err != nil {
			return nil, abort(WithHint(fmt.Errorf("write vault entry %s: %w", dst, err), to))
		}
	}

	// Trigger files and a waiting import follow the account before the config
	// does, so an engine that reloads sees them under the new name.
	undos = append(undos, engine.RenameTriggers(dataDir, from, to))
	if prev := store.PendingImportBytes(dataDir); prev != nil {
		if err := store.RenamePendingImport(dataDir, from, to); err != nil {
			return nil, abort(fmt.Errorf("rename the imported state waiting for the engine: %w", err))
		}
		undos = append(undos, func() { _ = paths.WriteFileAtomic(store.PendingImportPath(dataDir), prev) })
	}

	// With no engine the engine lock is held from here until the state is written.
	// The rename is written before the save, marked as uncommitted, so a crash
	// after the save still finds it; it counts only if the config got that far.
	h, err := prepareRename(dataDir, store.Rename{From: from, To: to, ID: newRenameID()})
	if err != nil {
		return nil, abort(err)
	}
	defer h.close()

	save := o.SaveConfig
	if save == nil {
		save = func(c *config.Config, path string) error { return c.Save(path) }
	}
	if err := save(cfg, cfgPath); err != nil {
		h.discard()
		return nil, abort(fmt.Errorf("save config.toml: %w", err))
	}
	// From here on the rename has happened: what fails below is a warning.
	// The state goes first, before the old vault entries are deleted.
	if err := h.apply(cfg); err != nil {
		warnings = append(warnings, fmt.Sprintf("the statistics and alert state of %q were not carried over: %v", from, err))
	}
	if dropped {
		warnings = append(warnings, fmt.Sprintf("the saved sign-in could not be read and was not moved; run `e5extend account login %s`", to))
	}
	for _, p := range pairs {
		if !undoVault.had[p[0]] {
			continue
		}
		if err := v.Delete(p[0]); err != nil && !errors.Is(err, vault.ErrNotFound) {
			warnings = append(warnings, err.Error())
		}
	}
	return warnings, nil
}

// renameHandover gives the renamed account's state to whoever owns the state
// file: the running engine (a pending file), or this process (with the engine
// lock held, it rewrites state.json).
type renameHandover struct {
	dataDir string
	r       store.Rename
	handed  bool   // an engine is running and gets the rename in a file
	release func() // the engine lock, when held
	file    string // the uncommitted rename file written before the save, without an engine
	intent  string // the note an engine reads to tell a rename from a removal, with an engine
}

func newRenameID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// prepareRename takes the engine lock if no engine runs, and then writes the
// rename, uncommitted, before the config is saved.
func prepareRename(dataDir string, r store.Rename) (*renameHandover, error) {
	h := &renameHandover{dataDir: dataDir, r: r}
	release, err := acquireEngineLock(dataDir)
	switch {
	case errors.Is(err, engine.ErrRunning):
		h.handed = true
		// Before the save: an engine that loads the new config while the rename is
		// still on its way must know the old name is being renamed, not removed.
		if h.intent, err = store.SaveRenameIntent(dataDir, h.r); err != nil {
			return nil, fmt.Errorf("write the rename: %w", err)
		}
		return h, nil
	case err != nil:
		return nil, err
	}
	h.release = release
	h.r.Uncommitted = true
	if h.file, err = store.SavePendingRenamesFile(dataDir, time.Time{}, []store.Rename{h.r}); err != nil {
		h.close()
		return nil, fmt.Errorf("write the rename: %w", err)
	}
	return h, nil
}

func (h *renameHandover) close() {
	if h.release != nil {
		h.release()
		h.release = nil
	}
}

// discard takes back the rename after the config could not be saved.
func (h *renameHandover) discard() {
	for _, f := range []string{h.file, h.intent} {
		if f != "" {
			_ = os.Remove(f)
		}
	}
}

// apply finishes the handover once the config is saved. A running engine gets
// the rename in a file, written only now so that it is one the config has;
// without one it is applied to state.json here, after any rename an engine left
// unapplied before it stopped.
func (h *renameHandover) apply(cfg *config.Config) error {
	defer h.close()
	if h.handed {
		err := store.SavePendingRenames(h.dataDir, time.Time{}, []store.Rename{h.r})
		if err == nil && h.intent != "" {
			_ = os.Remove(h.intent) // the pending file says it now
		}
		return err
	}
	files, err := store.ClaimPendingRenames(h.dataDir)
	if err != nil {
		return err
	}
	rs, written, _ := store.ReadClaimedRenames(files)
	has := func(n string) bool { _, ok := cfg.Find(n); return ok }
	var valid []store.Rename
	for _, r := range rs {
		if r.Valid(has) {
			r.Uncommitted = false
			valid = append(valid, r)
		}
	}
	st := store.New(h.dataDir)
	state, err := st.LoadState()
	if err == nil {
		state.ApplyRenames(valid, nil)
		state.TidyRenames(has)
		err = st.SaveState(state)
	}
	if err != nil {
		// Give the files back for the engine to apply when it starts.
		_ = store.UnclaimRenames(files)
		return err
	}
	for _, f := range files {
		_ = os.Remove(f)
	}
	_ = written
	return nil
}
