package engine

import (
	"context"
	"errors"
	"maps"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// An account the engine paused after repeated failed rounds is tried again
// with one call: 6 hours after it was paused, 12 hours after the first try,
// then every 24 hours. A manual pause (the account's paused setting) is never
// probed.
const (
	probeFirst  = 6 * time.Hour
	probeSecond = 12 * time.Hour
	probeEvery  = 24 * time.Hour
	// A probe that could not tell (network error, throttling) is repeated
	// after probeRetryMin to probeRetryMax; it does not count as a try.
	probeRetryMin = 15 * time.Minute
	probeRetryMax = 30 * time.Minute
)

// probeDelay is the wait before the next probe, after done probes have failed.
func probeDelay(done int) time.Duration {
	switch done {
	case 0:
		return probeFirst
	case 1:
		return probeSecond
	}
	return probeEvery
}

// probeAPI picks the cheapest call that needs no {user}: the signed-in user
// for a delegated account, else the organization, else the first API the
// token allows, leaving out the APIs that answered 403/404 within the last 24
// hours. ok is false when none is left: the account then stays paused.
func (e *Engine) probeAPI(acct config.Account, scopes []string, skipUntil map[string]time.Time, now time.Time) (a catalog.API, path string, ok bool) {
	kind := catalog.Delegated
	if !acct.Delegated() {
		kind = catalog.App
	}
	var plain []catalog.API
	for _, c := range catalog.Available(e.apis, kind, scopes, acct.Categories) {
		if !c.UsesUser(kind) && !skipUntil[c.ID].After(now) {
			plain = append(plain, c)
		}
	}
	for _, id := range []string{"user.me", "org.info"} {
		for _, c := range plain {
			if c.ID == id {
				return c, c.Path(kind), true
			}
		}
	}
	if len(plain) > 0 {
		return plain[0], plain[0].Path(kind), true
	}
	return catalog.API{}, "", false
}

// probe runs one probe of an automatically paused account in the calling
// goroutine. The caller has set the account's running flag and added to e.wg.
func (e *Engine) probe(ctx context.Context, acct config.Account) {
	defer e.wg.Done()
	defer func() {
		e.withLock(func() {
			if rt := e.rt[acct.Name]; rt != nil {
				rt.running = false
			}
		})
	}()
	e.guard("probe "+acct.Name, func() { e.runProbe(ctx, acct) })
}

// probeVerdict is what a probe found out.
type probeVerdict int

const (
	probeUnclear probeVerdict = iota // network error or throttling: try again soon, no count
	probeFailed                      // the account still fails: next step of the schedule
)

func (e *Engine) runProbe(ctx context.Context, acct config.Account) {
	tok, err := e.signIn(ctx, acct)
	switch {
	case ctx.Err() != nil:
		return
	case errors.Is(err, auth.ErrTransient):
		e.afterProbe(acct, "network", probeUnclear)
		return
	case err != nil:
		// Signing in fails for a reason only the person can fix: that is an
		// attention state, not a pause to wait out.
		reason := e.attentionReason(err, acct.Name)
		now := e.clock.Now()
		e.withLock(func() {
			if as := e.state.Accounts[acct.Name]; as != nil && as.Status == store.StatusPaused {
				as.Status, as.Reason = store.StatusNeedsAttention, reason
				as.NextProbe, as.Probes = time.Time{}, 0
				e.setNextLocked(as, now.Add(attentionRetry))
				e.saveLocked()
			}
		})
		e.emit(store.Entry{Account: acct.Name, Event: "probe", Failed: 1, Message: "sign-in failed"})
		e.event(acct.Name, "needs_attention", reason)
		e.evalNotify()
		return
	}

	class, api := graph.OK, "token"
	var skipUntil map[string]time.Time
	e.withLock(func() {
		if as := e.state.Accounts[acct.Name]; as != nil {
			skipUntil = maps.Clone(as.APISkipUntil)
		}
	})
	a, path, ok := e.probeAPI(acct, tok.Scopes, skipUntil, e.clock.Now())
	if !ok {
		// No call to judge by, and a token alone proves nothing: the round
		// that paused the account would only pause it again.
		e.afterProbe(acct, "no api", probeFailed)
		return
	}
	api = a.ID
	class = e.o.Graph.Call(ctx, tok.AccessToken, path, e.pickQuery(a)).Class
	if ctx.Err() != nil {
		return
	}
	switch class {
	case graph.OK:
		e.resumeFromProbe(acct, api)
	case graph.Network, graph.Throttled, graph.Canceled:
		e.afterProbe(acct, string(class), probeUnclear)
	default:
		if class == graph.AuthFailed { // do not trust the cached token at the next probe
			e.withLock(func() {
				if rt := e.rt[acct.Name]; rt != nil {
					rt.forceTok = true
				}
			})
		}
		e.afterProbe(acct, string(class), probeFailed)
	}
}

// afterProbe books a probe that did not resume the account.
func (e *Engine) afterProbe(acct config.Account, why string, v probeVerdict) {
	now := e.clock.Now()
	e.withLock(func() {
		as := e.state.Accounts[acct.Name]
		if as == nil || as.Status != store.StatusPaused {
			return
		}
		if v == probeFailed {
			as.Probes++
			as.NextProbe = now.Add(probeDelay(as.Probes))
		} else {
			as.NextProbe = now.Add(e.rnd(probeRetryMin, probeRetryMax))
		}
		e.saveLocked()
	})
	e.emit(store.Entry{Account: acct.Name, Event: "probe", Failed: 1, Message: why})
}

// resumeFromProbe resumes an account whose probe worked and lets the alert
// rules announce the recovery.
func (e *Engine) resumeFromProbe(acct config.Account, api string) {
	now := e.clock.Now()
	resumed := false
	e.withLock(func() {
		as := e.state.Accounts[acct.Name]
		if as == nil || as.Status != store.StatusPaused {
			return
		}
		as.Status, as.Reason, as.ConsecutiveFailedRound = store.StatusOK, "", 0
		as.NextProbe, as.Probes = time.Time{}, 0
		as.LastSuccess, as.ResumedAt = now, now
		e.setNextLocked(as, now.Add(e.rnd(time.Minute, 5*time.Minute)))
		e.saveLocked()
		resumed = true
	})
	e.emit(store.Entry{Account: acct.Name, Event: "probe", OK: 1, Message: api})
	if resumed {
		e.event(acct.Name, "auto_resume", "the paused account answered a probe call")
		e.evalNotify()
	}
}
