package engine

import (
	"context"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
)

const (
	// updateGrace is how long after the start the first update check waits, so
	// a machine that has just logged in has its network up.
	updateGrace = time.Minute
	// updateRetry is how long to wait when the checker gives no time of its own.
	updateRetry = time.Hour
)

// updateTick starts the update check when it is due. It runs on a goroutine of
// its own, so a slow or unreachable GitHub never holds up the rounds, and a
// failure only means "try again later": the engine's real work is not affected.
func (e *Engine) updateTick(ctx context.Context, now time.Time) {
	u := e.o.Updater
	if u == nil {
		return
	}
	var channel string
	var enabled, busy bool
	e.withLock(func() {
		enabled, channel = e.cfg.Update.Check, e.cfg.Update.Channel
		if !enabled {
			e.update = nil // switched off: what was found is not news any more
			return
		}
		busy = e.updRunning || now.Sub(e.started) < updateGrace || now.Before(e.updNext)
		if !busy {
			e.updRunning = true
		}
	})
	if !enabled || busy {
		return
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.guard("update check", func() {
			st, err := u.Check(ctx, channel)
			e.withLock(func() {
				e.updRunning = false
				if err != nil {
					e.updNext = e.clock.Now().Add(updateRetry)
					return
				}
				e.update = &st
				e.updNext = st.Next
				if !e.updNext.After(e.clock.Now()) {
					e.updNext = e.clock.Now().Add(updateRetry)
				}
			})
			if err != nil {
				// No details: the error text may carry the proxy address.
				e.log.Info("update check failed; will try again later")
			}
		})
		e.withLock(func() { e.updRunning = false })
	}()
}

// updateOfferLocked is what the alert evaluation is told about updates; e.mu
// must be held. nil means "not known": nothing is announced or forgotten.
func (e *Engine) updateOfferLocked() *notify.UpdateOffer {
	if e.update == nil || !e.cfg.Update.Check {
		return nil
	}
	return &notify.UpdateOffer{Version: e.update.Version, Current: e.o.Version, URL: e.update.URL}
}
