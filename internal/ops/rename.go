package ops

import (
	"context"
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
// entries are copied to the new names, the state is handed over, config.toml is
// saved, and only then are the old vault entries deleted. If the config cannot
// be saved the copies are removed and nothing has changed.
//
// The state belongs to the engine, which keeps it in memory. With an engine
// running the rename goes to it in a file (store.PendingRenameFile) that it
// applies when it loads the config with the new name; without one the state
// is rewritten here, with the engine lock held so none starts halfway. Call
// ReloadEngine afterwards.
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
	undo, err := snapshotVault(v, keys)
	if err != nil {
		return nil, fmt.Errorf("read the vault entries: %w", err)
	}
	abort := func(cause error) error {
		if failed := undo.restore(); len(failed) > 0 {
			return fmt.Errorf("%w\nthe vault entries %v could not be put back; run `e5extend doctor`", cause, failed)
		}
		return fmt.Errorf("%w\nnothing was changed", cause)
	}
	var dropped []string // old entries that were not copied because they cannot be read
	for _, p := range pairs {
		src, dst := p[0], p[1]
		if !undo.had[src] {
			if undo.had[dst] { // left behind by an account that was removed earlier; the new one must not inherit it
				if err := v.Delete(dst); err != nil && !errors.Is(err, vault.ErrNotFound) {
					return nil, abort(err)
				}
			}
			continue
		}
		// The entry is encrypted together with its name, so it is read and written again.
		val, err := v.Get(src)
		switch {
		case err == nil:
		case src == vault.TokenCacheKey(from) && (errors.Is(err, vault.ErrWrongKey) || errors.Is(err, vault.ErrCorrupt)):
			// An unreadable sign-in cannot be moved; it needs a new login either way.
			dropped = append(dropped, src)
			continue
		default:
			return nil, abort(WithHint(fmt.Errorf("read vault entry %s: %w", src, err), from))
		}
		if err := v.Put(dst, val); err != nil {
			return nil, abort(WithHint(fmt.Errorf("write vault entry %s: %w", dst, err), to))
		}
	}

	// State: to a running engine before the config is saved, so a reload that
	// sees the new name finds the rename too.
	h, err := prepareRename(dataDir, from, to)
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
	if len(dropped) > 0 {
		warnings = append(warnings, fmt.Sprintf("the saved sign-in could not be read and was not moved; run `e5extend account login %s`", to))
	}
	for _, p := range pairs {
		if !undo.had[p[0]] {
			continue
		}
		if err := v.Delete(p[0]); err != nil && !errors.Is(err, vault.ErrNotFound) {
			warnings = append(warnings, err.Error())
		}
	}
	engine.RenameTriggers(dataDir, from, to)
	if err := store.RenamePendingImport(dataDir, from, to); err != nil {
		warnings = append(warnings, fmt.Sprintf("an imported state waiting for the engine was not renamed: %v", err))
	}
	if err := h.apply(cfg); err != nil {
		warnings = append(warnings, fmt.Sprintf("the statistics and alert state of %q were not carried over: %v", from, err))
	}
	return warnings, nil
}

// renameHandover gives the renamed account's state to whoever owns the state
// file: the running engine (a pending file), or this process (with the engine
// lock held, it rewrites state.json).
type renameHandover struct {
	dataDir string
	handed  bool   // an engine is running and gets the rename in a file
	release func() // the engine lock, when held
	prev    []byte // the pending-rename file as it was, nil if there was none
}

func prepareRename(dataDir, from, to string) (*renameHandover, error) {
	h := &renameHandover{dataDir: dataDir}
	if b, err := os.ReadFile(store.PendingRenamePath(dataDir)); err == nil {
		h.prev = b
	}
	release, err := acquireEngineLock(dataDir)
	switch {
	case errors.Is(err, engine.ErrRunning):
		h.handed = true
	case err != nil:
		return nil, err
	default:
		h.release = release
	}
	// Both ways go through the pending file, which also takes in a rename an
	// earlier run left behind (a to b, now b to c becomes a to c).
	if err := store.SavePendingRenames(dataDir, time.Time{}, []store.Rename{{From: from, To: to}}); err != nil {
		h.close()
		return nil, fmt.Errorf("hand the rename to the state: %w", err)
	}
	return h, nil
}

func (h *renameHandover) close() {
	if h.release != nil {
		h.release()
		h.release = nil
	}
}

// discard takes back the handover after the config could not be saved.
func (h *renameHandover) discard() {
	_ = store.RemovePendingRename(h.dataDir)
	if h.prev != nil {
		_ = paths.WriteFileAtomic(store.PendingRenamePath(h.dataDir), h.prev)
	}
}

// apply finishes the handover once the config is saved. A running engine does
// it itself; without one the pending renames are applied to state.json here.
func (h *renameHandover) apply(cfg *config.Config) error {
	defer h.close()
	if h.handed {
		return nil
	}
	files, err := store.ClaimPendingRenames(h.dataDir)
	if err != nil {
		return err
	}
	rs, written, _ := store.ReadClaimedRenames(files)
	cleanup := func() {
		for _, f := range files {
			_ = os.Remove(f)
		}
	}
	inCfg := func(n string) bool { _, ok := cfg.Find(n); return ok }
	st := store.New(h.dataDir)
	state, err := st.LoadState()
	if err != nil {
		// Leave the renames for the engine to apply when it starts.
		cleanup()
		_ = store.SavePendingRenames(h.dataDir, written, rs)
		return err
	}
	_, later := state.ApplyRenames(rs, inCfg)
	state.TidyRenames(inCfg)
	if err := st.SaveState(state); err != nil {
		cleanup()
		_ = store.SavePendingRenames(h.dataDir, written, rs)
		return err
	}
	cleanup()
	if len(later) > 0 {
		_ = store.SavePendingRenames(h.dataDir, written, later)
	}
	return nil
}
