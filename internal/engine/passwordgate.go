package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// passwordSuffix marks the trigger that clears an account's password block.
const passwordSuffix = ".pwreset"

// TriggerPasswordReset asks the engine to let an account sign in with its
// password again: the stored password or the account was changed, or a person
// signed in. The engine alone writes state.json, so it clears the block itself.
func TriggerPasswordReset(dataDir, account string) error {
	return writeTrigger(dataDir, account+passwordSuffix)
}

// passwordGate is the engine's decision on automatic password sign-in for one
// account, kept in state.json so it holds across restarts and processes.
type passwordGate struct {
	e       *Engine
	name    string
	rt      *accountRT // follows the account through a rename; nil outside a round
	attempt time.Time  // the attempt this gate recorded in Allow
}

func (e *Engine) gateContext(ctx context.Context, name string) context.Context {
	return auth.AllowPasswordFallback(ctx, &passwordGate{e: e, name: name, rt: rtOf(ctx)})
}

// nameLocked is the account's current name; the caller holds e.mu.
func (g *passwordGate) nameLocked() string {
	if g.rt != nil && g.rt.name != "" {
		return g.rt.name
	}
	return g.name
}

func (g *passwordGate) Allow() (ok bool, blocked string) {
	g.e.withLock(func() {
		as := g.e.state.Accounts[g.nameLocked()]
		if as == nil {
			return
		}
		if as.PasswordBlocked != nil {
			blocked = as.PasswordBlocked.Code
			return
		}
		now := g.e.clock.Now()
		if !as.LastPasswordAttempt.IsZero() && now.Sub(as.LastPasswordAttempt) < auth.MinPasswordInterval && !now.Before(as.LastPasswordAttempt) {
			return
		}
		as.LastPasswordAttempt = now
		g.attempt = now
		g.e.saveLocked()
		ok = true
	})
	return ok, blocked
}

// current returns the account's state when it still belongs to this gate's
// attempt; a reset while the request ran (a person set a new password, say)
// changes the attempt time, and then the outcome of the old attempt is dropped.
func (g *passwordGate) current() *store.AccountState {
	as := g.e.state.Accounts[g.nameLocked()]
	if as == nil || g.attempt.IsZero() || !as.LastPasswordAttempt.Equal(g.attempt) {
		return nil
	}
	return as
}

func (g *passwordGate) Result(code string) {
	g.e.withLock(func() {
		if as := g.current(); as != nil {
			as.LastPasswordCode = code
			g.e.saveLocked()
		}
	})
}

func (g *passwordGate) LastCode() (code string) {
	g.e.withLock(func() {
		if as := g.e.state.Accounts[g.nameLocked()]; as != nil {
			code = as.LastPasswordCode
		}
	})
	return code
}

func (g *passwordGate) Failed(code string) {
	blocked := false
	g.e.withLock(func() {
		if as := g.current(); as != nil {
			as.PasswordBlocked = &store.PasswordBlock{Code: code, At: g.e.clock.Now()}
			g.e.saveLocked()
			blocked = true
		}
	})
	if blocked {
		g.e.event(g.name, "password_signin_blocked", code)
	}
}

// resetPasswordGate clears the block and the attempt time of an account.
func (e *Engine) resetPasswordGate(name string) {
	cleared := false
	e.withLock(func() {
		if as := e.state.Accounts[name]; as != nil && (as.PasswordBlocked != nil || !as.LastPasswordAttempt.IsZero()) {
			as.PasswordBlocked, as.LastPasswordAttempt, as.LastPasswordCode = nil, time.Time{}, ""
			e.saveLocked()
			cleared = true
		}
	})
	if cleared {
		e.event(name, "password_signin_reset", "")
	}
}

// applyPasswordResets handles the reset triggers waiting at start-up, so a
// one-off run (`run-now` without an engine) sees them too.
func (e *Engine) applyPasswordResets() {
	dir := triggerDir(e.o.DataDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, de := range ents {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, passwordSuffix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			continue
		}
		e.resetPasswordGate(strings.TrimSuffix(name, passwordSuffix))
	}
}
