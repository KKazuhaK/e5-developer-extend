// Package engine schedules and runs rounds of read-only Graph calls for every
// configured account. It compares wall-clock times on a short tick instead of
// sleeping until the next round, so suspend, clock changes and DST are safe.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/e5-renew/internal/auth"
	"github.com/KKazuhaK/e5-renew/internal/catalog"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/graph"
	"github.com/KKazuhaK/e5-renew/internal/paths"
	"github.com/KKazuhaK/e5-renew/internal/store"
)

// Tunables that are not worth a config option.
const (
	// AutoPauseRounds is the number of consecutive failed rounds that pauses an account.
	AutoPauseRounds   = 3
	forbiddenLimit    = 3
	forbiddenSkip     = 7 * 24 * time.Hour
	attentionRetry    = 6 * time.Hour
	maxRetryAfterWait = 5 * time.Minute
	defaultRetryAfter = 30 * time.Second
)

// Options configure an Engine. Use DefaultOptions and override fields.
type Options struct {
	Config     *config.Config // initial configuration (required)
	ConfigPath string         // reloaded when it changes; empty disables reloading
	DataDir    string         // state, logs, lock, heartbeat, triggers

	// NewAuth builds the authenticator for an account.
	NewAuth func(acct config.Account) (auth.Authenticator, error)
	Graph   *graph.Client
	Clock   Clock
	Logger  *slog.Logger

	// Hint returns a suggested next step for a sign-in error, or "".
	Hint func(err error, account string) string
	// OnEntry sees every call-log entry as it is written.
	OnEntry func(store.Entry)

	Tick             time.Duration // how often the main loop wakes
	ConfigCheckEvery int           // ticks between config file checks
	CallGapMin       time.Duration // pause between calls in a round
	CallGapMax       time.Duration
	KeepLogDays      int
	LockWait         time.Duration // how long Run and RunOnce wait for the engine lock
	Seed             uint64        // 0: random
	Version          string        // recorded in the start event
}

// DefaultOptions returns the production timing values.
func DefaultOptions() Options {
	return Options{
		Clock:            SystemClock{},
		Tick:             30 * time.Second,
		ConfigCheckEvery: 30,
		CallGapMin:       time.Second,
		CallGapMax:       10 * time.Second,
		KeepLogDays:      30,
		LockWait:         3 * time.Second,
	}
}

type accountRT struct {
	running   bool
	wasPaused bool
	au        auth.Authenticator
	auAcct    config.Account
	userID    string // cached target user for application identities
	userDay   string
	userFail  string // day on which the user lookup failed; not retried that day
	forceTok  bool   // fetch a fresh token next round (Graph rejected the last one)
}

// Engine runs rounds for all accounts of a configuration.
type Engine struct {
	o     Options
	store *store.Store
	clock Clock
	log   *slog.Logger
	apis  []catalog.API

	rndMu sync.Mutex
	rng   *rand.Rand

	mu    sync.Mutex // guards cfg, sched, state and rt
	cfg   *config.Config
	sched *schedule
	state *store.State
	rt    map[string]*accountRT

	// Touched by the tick goroutine only.
	lastTick  time.Time
	ticks     int
	lastPrune string
	cfgMod    time.Time

	reload chan struct{}
	wg     sync.WaitGroup
}

// New validates the options and returns an Engine.
func New(o Options) (*Engine, error) {
	if o.Config == nil {
		return nil, fmt.Errorf("engine: no configuration")
	}
	if o.DataDir == "" {
		return nil, fmt.Errorf("engine: no data directory")
	}
	if o.NewAuth == nil {
		return nil, fmt.Errorf("engine: no authenticator factory")
	}
	if o.Clock == nil {
		o.Clock = SystemClock{}
	}
	if o.Graph == nil {
		o.Graph = &graph.Client{}
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Tick <= 0 {
		o.Tick = 30 * time.Second
	}
	if o.ConfigCheckEvery <= 0 {
		o.ConfigCheckEvery = 30
	}
	if o.KeepLogDays <= 0 {
		o.KeepLogDays = 30
	}
	apis, err := catalog.Load()
	if err != nil {
		return nil, err
	}
	sc, err := newSchedule(o.Config.Schedule)
	if err != nil {
		return nil, fmt.Errorf("engine: schedule: %w", err)
	}
	seed := o.Seed
	if seed == 0 {
		seed = rand.Uint64()
	}
	return &Engine{
		o: o, store: store.New(o.DataDir), clock: o.Clock, log: o.Logger, apis: apis,
		rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		cfg: o.Config, sched: sc, rt: map[string]*accountRT{},
		reload: make(chan struct{}, 1),
	}, nil
}

// Reload asks the main loop to reload the configuration at the next opportunity.
func (e *Engine) Reload() {
	select {
	case e.reload <- struct{}{}:
	default:
	}
}

// ---- randomness ----

func (e *Engine) rnd(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	e.rndMu.Lock()
	defer e.rndMu.Unlock()
	return lo + time.Duration(e.rng.Int64N(int64(hi-lo)+1))
}

func (e *Engine) intn(n int) int {
	e.rndMu.Lock()
	defer e.rndMu.Unlock()
	return e.rng.IntN(n)
}

func (e *Engine) perm(n int) []int {
	e.rndMu.Lock()
	defer e.rndMu.Unlock()
	return e.rng.Perm(n)
}

// ---- logging and persistence ----

func (e *Engine) emit(ent store.Entry) {
	if ent.TS.IsZero() {
		ent.TS = e.clock.Now()
	}
	if err := e.store.Append(ent); err != nil {
		e.log.Error("write call log", "err", err)
	}
	if e.o.OnEntry != nil {
		e.o.OnEntry(ent)
	}
}

func (e *Engine) event(account, name, msg string) {
	e.emit(store.Entry{Account: account, Event: name, Message: msg})
	e.log.Info("event", "event", name, "account", account, "detail", msg)
}

// saveLocked writes the state; e.mu must be held.
func (e *Engine) saveLocked() {
	if err := e.store.SaveState(e.state); err != nil {
		e.log.Error("save state", "err", err)
	}
}

// ---- lifecycle ----

// Run holds the single-instance lock and runs until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	release, err := e.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	e.guard("init", e.init)
	for {
		e.guard("tick", func() { e.tick(ctx) })
		select {
		case <-ctx.Done():
			e.wg.Wait()
			e.withLock(e.saveLocked)
			e.event("", "stop", "")
			return nil
		case <-e.reload:
			e.guard("reload", e.reloadConfig)
		case <-e.clock.After(e.o.Tick):
		}
	}
}

// acquire takes the engine lock, waiting briefly: `status` probes the lock and
// may hold it for a moment.
func (e *Engine) acquire(ctx context.Context) (func(), error) {
	deadline := time.Now().Add(e.o.LockWait)
	for {
		release, err := AcquireLock(e.o.DataDir)
		if !errors.Is(err, ErrRunning) || !time.Now().Before(deadline) {
			return release, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// guard runs f and turns a panic into a logged event, so one bad step cannot
// take the whole process down.
func (e *Engine) guard(what string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("engine step panicked", "step", what, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			e.event("", "panic", what+" panicked; see the engine log")
		}
	}()
	f()
}

// withLock runs f with e.mu held; the lock is released even if f panics.
func (e *Engine) withLock(f func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f()
}

// init loads the saved state and schedules every account.
func (e *Engine) init() {
	// Leftovers of interrupted atomic writes.
	paths.RemoveStaleTemp(e.o.DataDir, time.Minute)
	paths.RemoveStaleTemp(filepath.Join(e.o.DataDir, "vault"), time.Minute)
	if e.o.ConfigPath != "" {
		paths.RemoveStaleTemp(filepath.Dir(e.o.ConfigPath), time.Minute)
	}
	st, err := e.store.LoadState()
	if err != nil {
		e.log.Warn("starting with empty state", "err", err)
		e.event("", "state_reset", err.Error())
	}
	var fixed []string
	e.withLock(func() {
		e.state = st
		now := e.clock.Now()
		e.syncAccountsLocked(now, true)
		fixed = e.fixFarFutureLocked(now)
		e.saveLocked()
	})
	e.reportClockFix(fixed)
	if e.o.ConfigPath != "" {
		if fi, err := os.Stat(e.o.ConfigPath); err == nil {
			e.cfgMod = fi.ModTime()
		}
	}
	e.event("", "start", e.o.Version)
}

// syncAccountsLocked makes the state match the configured accounts. On
// startup, rounds missed while the engine was off are not made up: an overdue
// next_run becomes a short delay.
func (e *Engine) syncAccountsLocked(now time.Time, startup bool) {
	names := map[string]bool{}
	for _, a := range e.cfg.Accounts {
		names[a.Name] = true
		as := e.state.Accounts[a.Name]
		if as == nil {
			as = &store.AccountState{Status: store.StatusOK}
			e.state.Accounts[a.Name] = as
			e.setNextLocked(as, now.Add(e.rnd(time.Minute, 5*time.Minute)))
		} else if startup && (as.Status == store.StatusOK || as.Status == store.StatusNeedsAttention) && !as.NextRun.After(now) {
			e.setNextLocked(as, now.Add(e.rnd(time.Minute, 5*time.Minute)))
		}
		if e.rt[a.Name] == nil {
			e.rt[a.Name] = &accountRT{}
		}
	}
	for n := range e.state.Accounts {
		if !names[n] {
			delete(e.state.Accounts, n)
			delete(e.rt, n)
		}
	}
}

// setNextLocked schedules the account at t, moved into the work window. If no
// round fits before the end of the run period, the account waits for the end
// (and becomes period_ended then) instead of ending early.
func (e *Engine) setNextLocked(as *store.AccountState, t time.Time) {
	placed, ended := e.sched.place(t, e.rnd)
	if !ended {
		as.NextRun = placed
		return
	}
	now := e.clock.Now()
	if e.sched.ended(now) {
		as.Status, as.Reason, as.NextRun = store.StatusPeriodEnded, "the run period has ended", time.Time{}
		return
	}
	as.NextRun = e.sched.periodTo.at(now.Location(), 0, 0).AddDate(0, 0, 1)
}

// fixFarFutureLocked reschedules accounts whose next_run lies implausibly far
// ahead, which happens when the clock was wrong when it was saved and has been
// corrected since. It returns the names it changed.
func (e *Engine) fixFarFutureLocked(now time.Time) []string {
	limit := now.Add(e.sched.max + 8*24*time.Hour)
	var fixed []string
	for name, as := range e.state.Accounts {
		if rt := e.rt[name]; rt != nil && rt.running {
			continue
		}
		if as.NextRun.IsZero() || !as.NextRun.After(limit) {
			continue
		}
		// A distant period_start also puts next_run far ahead; that is legitimate.
		cand, ended := e.sched.next(now, e.rnd)
		if ended || as.NextRun.Sub(cand) <= 24*time.Hour {
			continue
		}
		as.NextRun = cand
		fixed = append(fixed, name)
	}
	return fixed
}

func (e *Engine) reportClockFix(names []string) {
	for _, n := range names {
		e.event(n, "clock_fix", "next run was implausibly far ahead; rescheduled")
	}
}

// ---- tick ----

func (e *Engine) tick(ctx context.Context) {
	now := e.clock.Now()
	if err := writeHeartbeat(e.o.DataDir, now); err != nil {
		e.log.Warn("write heartbeat", "err", err)
	}

	if !e.lastTick.IsZero() {
		forward := now.Sub(e.lastTick) > 3*e.o.Tick
		backward := e.lastTick.Sub(now) > e.o.Tick
		if backward {
			e.event("", "clock_change", fmt.Sprintf("wall clock moved back %s", e.lastTick.Sub(now).Round(time.Second)))
		}
		if forward {
			e.event("", "resume", fmt.Sprintf("wall clock moved forward %s since the last tick", now.Sub(e.lastTick).Round(time.Second)))
			// Wait for the network to come back before the overdue rounds start.
			e.withLock(func() {
				for name, as := range e.state.Accounts {
					if rt := e.rt[name]; rt != nil && !rt.running && !as.NextRun.IsZero() && !as.NextRun.After(now) {
						e.setNextLocked(as, now.Add(e.rnd(time.Minute, 3*time.Minute)))
					}
				}
				e.saveLocked()
			})
		}
	}
	e.lastTick = now

	e.ticks++
	if e.o.ConfigPath != "" && e.ticks%e.o.ConfigCheckEvery == 0 {
		if fi, err := os.Stat(e.o.ConfigPath); err == nil && !fi.ModTime().Equal(e.cfgMod) {
			e.reloadConfig()
		}
	}

	manual := e.processTriggers(now)

	var fixed []string
	e.withLock(func() {
		fixed = e.fixFarFutureLocked(now)
		changed := len(fixed) > 0
		for _, acct := range e.cfg.Accounts {
			as, rt := e.state.Accounts[acct.Name], e.rt[acct.Name]
			if as == nil || rt == nil {
				continue
			}
			if acct.Paused {
				rt.wasPaused = true
				continue
			}
			if rt.wasPaused {
				rt.wasPaused = false
				if !as.NextRun.After(now) {
					e.setNextLocked(as, now.Add(e.rnd(time.Minute, 5*time.Minute)))
				}
			}
			if as.Status == store.StatusPeriodEnded {
				// The period may have been extended in the config.
				if !e.sched.ended(now) {
					as.Status, as.Reason = store.StatusOK, ""
					e.setNextLocked(as, now.Add(e.rnd(time.Minute, 5*time.Minute)))
					changed = true
				}
				continue
			}
			if as.Status == store.StatusPaused || rt.running {
				continue
			}
			if e.sched.ended(now) {
				as.Status, as.Reason, as.NextRun = store.StatusPeriodEnded, "the run period has ended", time.Time{}
				changed = true
				continue
			}
			if manual[acct.Name] || !as.NextRun.After(now) {
				rt.running = true
				e.wg.Add(1)
				go e.round(ctx, acct, manual[acct.Name])
			}
		}
		if changed {
			e.saveLocked()
		}
	})
	e.reportClockFix(fixed)

	if day := now.Format("2006-01-02"); day != e.lastPrune {
		e.lastPrune = day
		if n, err := e.store.Prune(now, e.o.KeepLogDays); err != nil {
			e.log.Warn("prune logs", "err", err)
		} else if n > 0 {
			e.log.Info("pruned old log files", "files", n)
		}
	}
}

// wait blocks until all running rounds have finished.
func (e *Engine) wait() { e.wg.Wait() }

// processTriggers consumes trigger files and returns the accounts to run now.
func (e *Engine) processTriggers(now time.Time) map[string]bool {
	manual := map[string]bool{}
	dir := triggerDir(e.o.DataDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return manual
	}
	for _, de := range ents {
		name := de.Name()
		if de.IsDir() || strings.Contains(name, ".tmp") {
			continue
		}
		// Remove first, so a file that cannot be removed does not fire every tick.
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			e.log.Warn("remove trigger", "err", err)
			continue
		}
		switch {
		case name == ".reload":
			e.reloadConfig()
		case strings.HasSuffix(name, ".resume"):
			e.resumeAccount(strings.TrimSuffix(name, ".resume"), now)
		default:
			var ok bool
			e.withLock(func() {
				as := e.state.Accounts[name]
				var acct *config.Account
				if as != nil {
					acct, _ = e.cfg.Find(name)
				}
				ok = acct != nil && !acct.Paused && as.Status != store.StatusPaused
			})
			if ok {
				manual[name] = true
				e.event(name, "trigger", "")
			} else {
				e.event(name, "trigger_ignored", "unknown or paused account")
			}
		}
	}
	return manual
}

// resumeAccount clears an automatic pause or an attention state.
func (e *Engine) resumeAccount(name string, now time.Time) {
	resumed := false
	e.withLock(func() {
		as := e.state.Accounts[name]
		if as == nil || as.Status == store.StatusOK {
			return
		}
		as.Status, as.Reason, as.ConsecutiveFailedRound = store.StatusOK, "", 0
		e.setNextLocked(as, now.Add(e.rnd(time.Minute, 5*time.Minute)))
		e.saveLocked()
		resumed = true
	})
	if resumed {
		e.event(name, "account_resume", "")
	}
}

// reloadConfig loads the config file again; an invalid file keeps the old one.
func (e *Engine) reloadConfig() {
	if e.o.ConfigPath == "" {
		return
	}
	if fi, err := os.Stat(e.o.ConfigPath); err == nil {
		e.cfgMod = fi.ModTime()
	}
	cfg, err := config.Load(e.o.ConfigPath)
	var sc *schedule
	if err == nil {
		sc, err = newSchedule(cfg.Schedule)
	}
	if err != nil {
		e.event("", "config_error", "keeping the previous configuration: "+firstLine(err.Error()))
		return
	}
	var restart bool
	e.withLock(func() {
		restart = cfg.Network.Proxy != e.cfg.Network.Proxy || cfg.Vault.Backend != e.cfg.Vault.Backend
		e.cfg, e.sched = cfg, sc
		e.syncAccountsLocked(e.clock.Now(), false)
		e.saveLocked()
	})
	msg := ""
	if restart {
		msg = "restart required for network/vault changes"
	}
	e.event("", "config_reload", msg)
}

func firstLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// authFor returns the cached authenticator for acct, rebuilt if the account changed.
func (e *Engine) authFor(acct config.Account) (auth.Authenticator, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rt := e.rt[acct.Name]
	if rt == nil {
		rt = &accountRT{}
		e.rt[acct.Name] = rt
	}
	if rt.au != nil && reflect.DeepEqual(rt.auAcct, acct) {
		return rt.au, nil
	}
	au, err := e.o.NewAuth(acct)
	if err != nil {
		return nil, err
	}
	rt.au, rt.auAcct = au, acct
	return au, nil
}

// RunOnce runs one round for the named accounts (all when none are named) in
// this process. It fails with ErrRunning while a background engine holds the lock.
func RunOnce(ctx context.Context, o Options, names []string) error {
	e, err := New(o)
	if err != nil {
		return err
	}
	release, err := e.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	e.init()

	var run []config.Account
	skip := map[string]string{}
	e.withLock(func() {
		for _, a := range e.cfg.Accounts {
			if len(names) > 0 && !contains(names, a.Name) {
				continue
			}
			switch as := e.state.Accounts[a.Name]; {
			case a.Paused:
				skip[a.Name] = "account is paused; run: e5renew account resume " + a.Name
			case as != nil && as.Status == store.StatusPaused:
				skip[a.Name] = "account was paused automatically; run: e5renew account resume " + a.Name
			case as != nil && as.Status == store.StatusPeriodEnded:
				skip[a.Name] = "the run period has ended"
			}
			run = append(run, a)
		}
	})
	for _, a := range run {
		if why := skip[a.Name]; why != "" {
			e.event(a.Name, "trigger_ignored", why)
			continue
		}
		e.withLock(func() { e.rt[a.Name].running = true })
		e.wg.Add(1)
		e.round(ctx, a, true)
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
