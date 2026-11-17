package engine

import (
	"os"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// claimedRenames are account renames taken from the handover file
// (store.PendingRenameFile) and not applied yet.
type claimedRenames struct {
	rs      []store.Rename
	written time.Time
}

// claimRenames takes the account renames `account rename` handed over. The
// caller applies them with applyRenamesLocked to the state it is about to
// match against a configuration, so the state of a renamed account is moved
// before syncAccountsLocked would drop it as unknown. It returns nil when
// there are none, or when they have waited too long.
func (e *Engine) claimRenames() *claimedRenames {
	files, err := store.ClaimPendingRenames(e.o.DataDir)
	if err != nil {
		e.log.Warn("claim the account renames", "err", err)
		return nil
	}
	if len(files) == 0 {
		return nil
	}
	rs, written, errs := store.ReadClaimedRenames(files)
	for _, err := range errs {
		e.log.Warn("ignoring the account renames", "err", err)
	}
	for _, f := range files {
		_ = os.Remove(f)
	}
	if len(rs) == 0 {
		return nil
	}
	if e.clock.Now().Sub(written) > importMaxAge {
		e.event("", "rename_expired", "account renames older than 7 days were ignored")
		return nil
	}
	return &claimedRenames{rs: rs, written: written}
}

// applyRenamesLocked applies claimed renames to e.state against e.cfg. Renames
// whose new name the configuration does not have yet are put back for the
// next reload, for up to importWait. It returns the renames it applied. The
// caller holds e.mu and calls syncAccountsLocked afterwards.
func (e *Engine) applyRenamesLocked(c *claimedRenames) []store.Rename {
	if c == nil {
		return nil
	}
	inCfg := func(n string) bool { _, ok := e.cfg.Find(n); return ok }
	applied, later := e.state.ApplyRenames(c.rs, inCfg)
	if len(later) > 0 && e.clock.Now().Sub(c.written) < importWait {
		if err := store.SavePendingRenames(e.o.DataDir, c.written, later); err != nil {
			e.log.Warn("keep the account renames for later", "err", err)
		}
	}
	return applied
}

// tidyRenamesLocked drops the earlier names of accounts that are gone.
func (e *Engine) tidyRenamesLocked() {
	e.state.TidyRenames(func(n string) bool { _, ok := e.cfg.Find(n); return ok })
}

// reportRenames logs the renames that were applied.
func (e *Engine) reportRenames(applied []store.Rename) {
	for _, r := range applied {
		e.event(r.To, "account_renamed", "renamed from "+r.From)
	}
}
