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
	e    *Engine
	name string
}

func (e *Engine) gateContext(ctx context.Context, name string) context.Context {
	return auth.AllowPasswordFallback(ctx, &passwordGate{e: e, name: name})
}

func (g *passwordGate) Allow() (ok bool, blocked string) {
	g.e.withLock(func() {
		as := g.e.state.Accounts[g.name]
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
		g.e.saveLocked()
		ok = true
	})
	return ok, blocked
}

func (g *passwordGate) Failed(code string) {
	g.e.withLock(func() {
		if as := g.e.state.Accounts[g.name]; as != nil {
			as.PasswordBlocked = &store.PasswordBlock{Code: code, At: g.e.clock.Now()}
			g.e.saveLocked()
		}
	})
	g.e.event(g.name, "password_signin_blocked", code)
}

// resetPasswordGate clears the block and the attempt time of an account.
func (e *Engine) resetPasswordGate(name string) {
	cleared := false
	e.withLock(func() {
		if as := e.state.Accounts[name]; as != nil && (as.PasswordBlocked != nil || !as.LastPasswordAttempt.IsZero()) {
			as.PasswordBlocked, as.LastPasswordAttempt = nil, time.Time{}
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
