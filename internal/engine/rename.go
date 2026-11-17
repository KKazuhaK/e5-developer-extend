package engine

import (
	"os"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// detachedTTL is how long the state of an account the configuration no
// longer has is kept in memory: a rename that arrives a moment after the
// configuration that holds the new name was loaded still finds it.
const detachedTTL = time.Hour

// detachedState is the state of an account that the loaded configuration did not have.
type detachedState struct {
	as *store.AccountState
	at time.Time
}

// claimedRenames are account renames taken from the handover file
// (store.PendingRenameFile) and not applied yet.
type claimedRenames struct {
	rs      []store.Rename
	written time.Time
}

// claimRenames takes the account renames `account rename` handed over. It must
// run before the configuration is loaded: a rename is written only after the
// configuration that holds it was saved, so a configuration loaded after the
// claim has every claimed rename in it. It returns nil when there are none, or
// when they have waited too long.
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

// unclaimRenames puts claimed renames back, in front of newer ones, when the
// configuration could not be loaded.
func (e *Engine) unclaimRenames(c *claimedRenames) {
	if c == nil {
		return
	}
	if err := store.RestorePendingRenames(e.o.DataDir, c.written, c.rs); err != nil {
		e.log.Warn("keep the account renames for later", "err", err)
	}
}

// applyRenamesLocked applies claimed renames, in order, to the state and the
// runtime records, and returns the ones it applied. A round in flight keeps
// its runtime record, so it stays marked as running and books its result under
// the new name. The caller holds e.mu and calls syncAccountsLocked afterwards.
func (e *Engine) applyRenamesLocked(c *claimedRenames) []store.Rename {
	if c == nil {
		return nil
	}
	detached := map[string]*store.AccountState{}
	for n, d := range e.detached {
		detached[n] = d.as
	}
	for _, r := range c.rs {
		if r.From == r.To {
			continue
		}
		e.state.ApplyRename(r.From, r.To, detached)
		if rt := e.rt[r.From]; rt != nil {
			rt.name = r.To
			e.rt[r.To] = rt
			delete(e.rt, r.From)
		} else {
			delete(e.rt, r.To)
		}
		if e.renamedTo == nil {
			e.renamedTo = map[string]bool{}
		}
		e.renamedTo[r.To] = true
	}
	for n := range e.detached {
		if _, still := detached[n]; !still {
			delete(e.detached, n)
		}
	}
	return c.rs
}

// detachLocked sets the state of an account the configuration does not have
// aside instead of dropping it.
func (e *Engine) detachLocked(name string, as *store.AccountState, now time.Time) {
	if e.detached == nil {
		e.detached = map[string]*detachedState{}
	}
	e.detached[name] = &detachedState{as: as, at: now}
}

// recoverLocked returns the state set aside under name when a rename made it
// the account's, and forgets old entries.
func (e *Engine) recoverLocked(name string, now time.Time) *store.AccountState {
	for n, d := range e.detached {
		if now.Sub(d.at) > detachedTTL {
			delete(e.detached, n)
		}
	}
	if !e.renamedTo[name] {
		return nil
	}
	d := e.detached[name]
	if d == nil {
		return nil
	}
	delete(e.detached, name)
	return d.as
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
