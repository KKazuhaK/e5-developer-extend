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

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/callstat"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
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

// Hooks are optional callbacks that let a caller attach platform behaviour
// (for example systemd readiness and watchdog messages) without the engine
// knowing about it. Any of them may be nil. They run on the engine's own
// goroutine and must return quickly.
type Hooks struct {
	Ready    func() // the lock is held and the state is loaded
	Tick     func() // every pass of the main loop
	Stopping func() // shutdown has begun
}

// Notifier receives the alerts the engine decides to send. Enabled must not
// block (it runs on the tick). Close is called once when the engine stops and may wait briefly for
// queued alerts.
type Notifier interface {
	Enabled() bool
	// Send must not block. done, if not nil, later receives the delivery
	// result; an alert counts as sent only when it is called with nil.
	Send(ev notify.Event, done func(error))
	Close()
}

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

	// SaveHomeAccount stores the home account ID a password sign-in returned
	// into the configuration (under the configuration lock), provided the
	// account on disk still has the identity it signed in with; nil skips it.
	SaveHomeAccount func(ctx context.Context, signedIn config.Account, homeAccountID string) error

	// Hint returns a suggested next step for a sign-in error, or "".
	Hint func(err error, account string) string
	// OnEntry sees every call-log entry as it is written.
	OnEntry func(store.Entry)
	// Hooks attach platform integration; see Hooks.
	Hooks Hooks
	// Notifier receives alerts; nil turns alerts off.
	Notifier Notifier
	// NoSubscriptionCheck turns the daily subscription check off; tests that
	// count token requests use it.
	NoSubscriptionCheck bool

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
	// unsaved is set when the state in memory differs from state.json because a save failed.
	unsaved bool
	// roundsDone is set when the first round has finished; the subscription
	// checks wait for it. subRunning marks tenants being checked now.
	roundsDone bool
	subRunning map[string]bool
	subMu      sync.Mutex // one subscription check at a time

	// Touched by the tick goroutine only.
	lastTick  time.Time
	ticks     int
	lastPrune string
	forceSub  map[string]bool // tenants a trigger asked to check now
	cfgMod    time.Time
	started   time.Time // engine start, for the stale-alert grace period

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

// saveLocked writes the state; e.mu must be held. It reports whether the write worked.
func (e *Engine) saveLocked() bool {
	if err := e.store.SaveState(e.state); err != nil {
		e.log.Error("save state", "err", err)
		return false
	}
	return true
}

// Limits of a state handed over by `backup import` (store.PendingImportFile).
const (
	// importWait is how long entries for accounts that are not in the config yet are kept.
	importWait = 24 * time.Hour
	// importMaxAge is the age after which a handover file is ignored and deleted.
	importMaxAge = 7 * 24 * time.Hour
)

// hasActivity reports whether an account's state records any round. A state
// without one is what the engine creates for an account it has just learned
// about, so an imported state may take its place.
func hasActivity(as *store.AccountState) bool {
	return as.Totals != (store.Totals{}) || !as.LastSuccess.IsZero()
}

// adoptPending applies the state an import handed over. The file is claimed
// by renaming it first (see store.ClaimPendingImports), so a second import
// during this call writes a new file for the next call. Adoption is one-shot:
// a claimed file is deleted once applied, even if the save that follows fails
// (the state is then saved on the next tick). Entries it cannot apply yet stay
// in a new pending file: those of accounts that are not in the config (an
// import writes the handover just before the config, so a reload may not have
// seen the account yet; dropped after importWait) and those of accounts with a
// round in flight (applied on the first tick after it ends).
func (e *Engine) adoptPending() {
	files, err := store.ClaimPendingImports(e.o.DataDir)
	if err != nil {
		e.log.Warn("claim the imported state", "err", err)
		return
	}
	for _, f := range files {
		e.adoptFile(f)
		_ = os.Remove(f)
	}
}

func (e *Engine) adoptFile(path string) {
	p, err := store.ReadPendingFile(path)
	if err != nil {
		e.log.Warn("ignoring the imported state", "err", err)
		return
	}
	if p == nil {
		// The importer withdrew its handover (its config save failed) after we claimed it.
		return
	}
	now := e.clock.Now()
	age := now.Sub(p.Written)
	if age > importMaxAge {
		e.event("", "state_import_expired", "an imported state older than 7 days was ignored")
		return
	}
	later := map[string]*store.AccountState{}
	var adopted []string
	expired := 0
	var fixed []string
	e.withLock(func() {
		for name, as := range p.Accounts {
			rt := e.rt[name]
			switch _, inCfg := e.cfg.Find(name); {
			case !inCfg:
				if age < importWait {
					later[name] = as
				} else {
					expired++
				}
				continue
			case rt != nil && rt.running:
				later[name] = as
				continue
			}
			// A merge never replaces what an account has recorded here; the
			// empty state the engine made for a new account is not that.
			if have := e.state.Accounts[name]; have != nil && p.Mode == store.PendingMerge && hasActivity(have) {
				continue
			}
			e.state.Accounts[name] = as
			adopted = append(adopted, name)
			// An imported next run in the past would start a round at once, as after a restart.
			if (as.Status == store.StatusOK || as.Status == store.StatusNeedsAttention) && !as.NextRun.After(now) {
				e.setNextLocked(as, now.Add(e.rnd(time.Minute, 5*time.Minute)))
			}
		}
		if len(adopted) > 0 {
			fixed = e.fixFarFutureLocked(now)
			if !e.saveLocked() {
				e.unsaved = true
			}
		}
	})
	e.reportClockFix(fixed)
	if len(later) > 0 {
		err := store.SavePendingImport(e.o.DataDir, &store.PendingImport{Mode: p.Mode, Written: p.Written, Accounts: later})
		if err != nil {
			e.log.Warn("keep the imported state for later", "err", err)
		}
	}
	if len(adopted) > 0 {
		e.event("", "state_import", "adopted the state of an imported backup")
	}
	if expired > 0 {
		e.event("", "state_import_expired", "dropped the imported state of accounts that never appeared in the configuration")
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
	defer e.closeNotifier()

	e.guard("init", e.init)
	e.hook("ready", e.o.Hooks.Ready)
	for {
		e.guard("tick", func() { e.tick(ctx) })
		select {
		case <-ctx.Done():
			e.hook("stopping", e.o.Hooks.Stopping)
			e.wg.Wait()
			e.withLock(func() { e.saveLocked() })
			e.event("", "stop", "")
			return nil
		case <-e.reload:
			e.guard("reload", e.reloadConfig)
		case <-e.clock.After(e.o.Tick):
		}
	}
}

// hook runs an optional callback; a panic in it is contained like any other.
func (e *Engine) hook(name string, f func()) {
	if f != nil {
		e.guard("hook "+name, f)
	}
}

func (e *Engine) closeNotifier() {
	if e.o.Notifier != nil {
		e.o.Notifier.Close()
	}
}

// evalNotify decides which alerts are due and hands them to the Notifier.
// What has been announced is kept in the state, so a condition is announced
// once. Without a destination nothing is evaluated or recorded, so a webhook
// added later still hears about conditions that are already present.
func (e *Engine) evalNotify() {
	n := e.o.Notifier
	if n == nil || !n.Enabled() {
		return
	}
	var evs []notify.Event
	calls := e.recentCalls(e.clock.Now())
	e.withLock(func() {
		if e.state == nil {
			return
		}
		var changed bool
		evs, changed = notify.Evaluate(notify.Input{
			Now: e.clock.Now(), Start: e.started, Config: e.cfg.Notify,
			Accounts: e.cfg.Accounts, State: e.state, Calls: calls, APIs: e.apis,
			Subscription: e.cfg.Subscription,
		})
		if changed {
			e.saveLocked()
		}
	})
	for _, ev := range evs {
		n.Send(ev, func(err error) {
			settle := ev
			var pe *notify.PartialError
			switch {
			case err == nil:
			case errors.As(err, &pe) && len(pe.Delivered) > 0:
				// Some alerts of a split digest arrived: settle those, retry the rest.
				settle = notify.Event{Event: notify.Digest, Items: pe.Delivered}
			default:
				return
			}
			e.withLock(func() {
				if e.state != nil && notify.MarkEvent(e.state, settle) {
					e.saveLocked()
				}
			})
		})
		e.event(ev.Account, "alert", ev.Event)
	}
}

// callWindow is how far back the call log is read to judge a failure spike.
const callWindow = 48 * time.Hour

// recentCalls returns the call-log entries of the last callWindow, per
// account. It is nil when the log cannot be read, which tells Evaluate not to
// judge failure spikes this time.
func (e *Engine) recentCalls(now time.Time) map[string][]store.Entry {
	entries, err := e.store.Read(store.Filter{Since: now.Add(-callWindow)})
	if err != nil {
		e.log.Warn("call log not read for the failure check", "err", err.Error())
		return nil
	}
	// Calls made under an earlier name count for the account's current one.
	current := map[string]string{}
	e.withLock(func() {
		if e.state == nil {
			return
		}
		for acct, olds := range e.state.RenamedFrom {
			for _, o := range olds {
				current[o] = acct
			}
		}
	})
	calls := map[string][]store.Entry{}
	for _, en := range entries {
		if callstat.IsCall(en) {
			name := en.Account
			if n, ok := current[name]; ok {
				name = n
			}
			calls[name] = append(calls[name], en)
		}
	}
	return calls
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
	var renamed []store.Rename
	claimed := e.claimRenames()
	e.withLock(func() {
		e.state = st
		now := e.clock.Now()
		e.started = now
		renamed = e.applyRenamesLocked(claimed)
		e.syncAccountsLocked(now, true)
		e.tidyRenamesLocked()
		e.pruneTenantsLocked()
		fixed = e.fixFarFutureLocked(now)
		e.saveLocked()
	})
	e.reportRenames(renamed)
	e.reportClockFix(fixed)
	e.applyPasswordResets()
	e.adoptPending()
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
	e.hook("tick", e.o.Hooks.Tick)

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
	if store.HasPendingImport(e.o.DataDir) {
		e.adoptPending() // also when the reload trigger of the import was lost, or a round was in flight
	}

	e.forceSub = map[string]bool{}
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
		if changed || e.unsaved {
			e.unsaved = !e.saveLocked()
		}
	})
	e.reportClockFix(fixed)

	e.subscriptionTick(ctx, now, e.forceSub)
	e.evalNotify()

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
		case strings.HasSuffix(name, passwordSuffix):
			e.resetPasswordGate(strings.TrimSuffix(name, passwordSuffix))
		case strings.HasSuffix(name, subscriptionSuffix):
			e.handleSubscriptionTrigger(name, e.forceSub)
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
	var renamed []store.Rename
	claimed := e.claimRenames()
	e.withLock(func() {
		restart = cfg.Network.Proxy != e.cfg.Network.Proxy || cfg.Vault.Backend != e.cfg.Vault.Backend
		e.cfg, e.sched = cfg, sc
		// Renames first: the state of a renamed account moves to its new name
		// instead of being dropped with the old one.
		renamed = e.applyRenamesLocked(claimed)
		e.syncAccountsLocked(e.clock.Now(), false)
		e.tidyRenamesLocked()
		e.pruneTenantsLocked()
		e.saveLocked()
	})
	e.reportRenames(renamed)
	e.adoptPending()
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
	defer e.closeNotifier()
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
				skip[a.Name] = "account is paused; run: e5extend account resume " + a.Name
			case as != nil && as.Status == store.StatusPaused:
				skip[a.Name] = "account was paused automatically; run: e5extend account resume " + a.Name
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
