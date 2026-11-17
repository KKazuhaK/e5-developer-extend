// Package trayhost is the logic behind the tray mode of the desktop app: it
// decides whether this process runs the engine, and turns the engine state
// into an icon, a tooltip and menu texts. It has no GUI dependency, so it is
// tested without a display.
package trayhost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
)

// Icon says which tray icon to show.
type Icon int

// Icons.
const (
	IconOK   Icon = iota // everything fine
	IconWarn             // an account needs attention or is paused
	IconIdle             // the engine is not running
)

// Mode says who runs the engine.
type Mode int

// Modes.
const (
	ModeStopped Mode = iota // no engine is running
	ModeHosted              // this process runs the engine
	ModeMonitor             // someone else (the background service) runs it
)

// State is what the tray shows after a refresh.
type State struct {
	Icon     Icon
	Mode     Mode
	Accounts int
	// Problems counts accounts that need attention, are paused, not signed in or past their run period.
	Problems int
	// Healthy counts accounts with status ok.
	Healthy int
	// AllPaused is true when there are accounts and all of them are paused.
	AllPaused bool
	// NextRun is the earliest planned run, zero when unknown.
	NextRun time.Time
	// ServiceDown is true when a background service is installed but its
	// engine is not running; the tray then shows a warning.
	ServiceDown bool
}

// Deps are the outside things the controller uses; tests replace them.
type Deps struct {
	// Snapshot reads the engine and account state.
	Snapshot func() (ops.Snapshot, error)
	// EngineRunning reports whether any engine holds the lock.
	EngineRunning func() bool
	// RunEngine runs the engine in this process until ctx ends. It returns
	// engine.ErrRunning when another process got the lock first.
	RunEngine func(ctx context.Context) error
	// HandoverRequested reports (and clears) a request from another process
	// to give up the engine, made before it installs the background service.
	HandoverRequested func() bool
	// ResumeRequested reports (and clears) a request to end the quiet time after
	// a handover whose install failed, so the tray takes the engine back at once.
	ResumeRequested func() bool
	// ServiceState reports whether the background service is installed. It
	// may be slow (it asks the platform), so a successful answer is reused for
	// ServiceCheckEvery. While the service is installed the tray never runs the
	// engine and gives up the lock if it holds it. An error is never cached:
	// the previous answer stays in force; without any answer the tray only
	// monitors, and after three failures in a row with no answer at all the
	// service counts as not installed.
	ServiceState func() (installed bool, err error)
	// ServiceCheckEvery is how long a successful ServiceState answer is reused;
	// 5 minutes when zero, never reused when negative. The cache is dropped after
	// a handover or resume request.
	ServiceCheckEvery time.Duration
	// DownAfter is how long an installed service's engine may be missing
	// (lock free, no fresh heartbeat) before the tray warns; 2 minutes when zero.
	DownAfter time.Duration
	// Now is time.Now when nil.
	Now func() time.Time
	Log *slog.Logger
	// HandoverQuiet is how long the controller leaves the engine alone after a handover.
	HandoverQuiet time.Duration
}

// Controller owns the in-process engine and the state shown in the tray.
type Controller struct {
	d      Deps
	parent context.Context

	mu         sync.Mutex
	own        bool // the engine goroutine is running
	cancel     context.CancelFunc
	done       chan struct{}
	quietUntil time.Time
	localRound int    // rounds the tray itself runs without an engine
	lastErr    string // last start error that was logged

	svcAt        time.Time // when ServiceState last answered
	svcKnown     bool      // there is an answer
	svcInstalled bool
	svcErrs      int       // failures in a row
	idleSince    time.Time // since when no engine holds the lock
	svcDown      bool      // installed, but its engine is missing
}

// New returns a controller. ctx bounds the engine it starts.
func New(ctx context.Context, d Deps) *Controller {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.DownAfter == 0 {
		d.DownAfter = 2 * time.Minute
	}
	if d.ServiceCheckEvery == 0 {
		d.ServiceCheckEvery = 5 * time.Minute
	}
	if d.HandoverQuiet == 0 {
		d.HandoverQuiet = 2 * time.Minute
	}
	return &Controller{d: d, parent: ctx}
}

// ShouldStartEngine is the decision at the heart of tray mode: run the engine
// here only when nothing else runs it and we are not stepping aside.
func ShouldStartEngine(ownRunning, otherRunning, quiet, serviceInstalled bool) bool {
	return !ownRunning && !otherRunning && !quiet && !serviceInstalled
}

// Refresh starts or stops the engine of this process as needed and returns
// the state to show. The rule is simple: while a background service is
// installed the tray only monitors (and lets go of the lock if it holds it);
// otherwise it runs the engine when nobody else does.
func (c *Controller) Refresh() State {
	c.CheckHandover()
	c.CheckResume()
	now := c.d.Now()

	c.mu.Lock()
	own := c.own
	quiet := now.Before(c.quietUntil) || c.localRound > 0
	c.mu.Unlock()
	installed, known := c.serviceState(now)
	other := c.d.EngineRunning()

	switch {
	case !known:
		// No answer yet: do not take a lock the service may need.
	case installed:
		if own {
			c.Stop()
		}
	case !own && ShouldStartEngine(own, other, quiet, false):
		c.start()
	}

	// One read of the state files per refresh, and none under the lock.
	snap, snapErr := c.d.Snapshot()
	c.mu.Lock()
	c.svcDown = false
	if installed && known && !own && !other {
		if c.idleSince.IsZero() {
			c.idleSince = now
		}
		hb := snap.Heartbeat
		c.svcDown = now.Sub(c.idleSince) > c.d.DownAfter && (hb.IsZero() || now.Sub(hb) > c.d.DownAfter)
	} else {
		c.idleSince = time.Time{}
	}
	c.mu.Unlock()
	return c.stateFrom(snap, snapErr)
}

// failuresBeforeGivingUp is how many failed status queries in a row, with no
// earlier answer, make the service count as not installed.
const failuresBeforeGivingUp = 3

// serviceState returns whether the service is installed. known is false when
// there is no answer yet (the tray then only monitors).
func (c *Controller) serviceState(now time.Time) (installed, known bool) {
	if c.d.ServiceState == nil {
		return false, true
	}
	c.mu.Lock()
	if c.svcKnown && !c.svcAt.IsZero() && c.d.ServiceCheckEvery > 0 && now.Sub(c.svcAt) < c.d.ServiceCheckEvery {
		defer c.mu.Unlock()
		return c.svcInstalled, true
	}
	c.mu.Unlock()
	inst, err := c.d.ServiceState()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.svcErrs++
		switch {
		case c.svcKnown:
			return c.svcInstalled, true // keep the previous answer; ask again next time
		case c.svcErrs >= failuresBeforeGivingUp:
			return false, true // never answered: treat as not installed
		}
		return false, false
	}
	c.svcErrs = 0
	c.svcAt, c.svcKnown, c.svcInstalled = now, true, inst
	return inst, true
}

func (c *Controller) invalidateService() {
	c.mu.Lock()
	c.svcAt = time.Time{}
	c.mu.Unlock()
}

// CheckResume ends the quiet time after a failed handover. It reports whether it did.
func (c *Controller) CheckResume() bool {
	if c.d.ResumeRequested == nil || !c.d.ResumeRequested() {
		return false
	}
	c.mu.Lock()
	c.quietUntil = time.Time{}
	c.mu.Unlock()
	c.invalidateService()
	return true
}

// LocalRound marks that the tray itself runs a round (no engine was running).
// While it lasts the tray counts as the host and starts no engine. Call the
// returned function when the round ends.
func (c *Controller) LocalRound() func() {
	c.mu.Lock()
	c.localRound++
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.localRound--
		c.mu.Unlock()
	}
}

// CheckHandover gives the engine up when another process asked for it. It
// reports whether it did.
func (c *Controller) CheckHandover() bool {
	if c.d.HandoverRequested == nil || !c.d.HandoverRequested() {
		return false
	}
	c.handover()
	c.invalidateService()
	return true
}

func (c *Controller) start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.own {
		return
	}
	ctx, cancel := context.WithCancel(c.parent)
	c.own, c.cancel, c.done = true, cancel, make(chan struct{})
	done := c.done
	go func() {
		err := c.d.RunEngine(ctx)
		c.mu.Lock()
		c.own = false
		c.mu.Unlock()
		close(done)
		c.mu.Lock()
		msg := ""
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, engine.ErrRunning) {
			msg = err.Error()
		}
		changed := msg != c.lastErr
		c.lastErr = msg
		c.mu.Unlock()
		if msg != "" && changed {
			// Typical: no config file yet. The next refresh tries again; the
			// same failure is logged once.
			c.d.Log.Info("engine did not start", "err", msg)
		}
	}()
}

// handover stops the engine of this process and leaves the lock alone for a while.
func (c *Controller) handover() {
	c.mu.Lock()
	c.quietUntil = c.d.Now().Add(c.d.HandoverQuiet)
	c.mu.Unlock()
	c.Stop()
}

// Stop stops the engine of this process and waits for it.
func (c *Controller) Stop() {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// Hosting reports whether this process runs the engine.
func (c *Controller) Hosting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.own
}

func (c *Controller) serviceDown() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.svcDown
}

func (c *Controller) localRounds() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.localRound
}

// state reads the snapshot itself; Refresh passes the one it has read to stateFrom.
func (c *Controller) state() State {
	snap, err := c.d.Snapshot()
	return c.stateFrom(snap, err)
}

func (c *Controller) stateFrom(snap ops.Snapshot, err error) State {
	st := State{Icon: IconIdle, Mode: ModeStopped}
	if err != nil {
		return st
	}
	switch {
	case !snap.Engine.Running && !c.Hosting() && c.localRounds() == 0:
		st.Mode = ModeStopped
	case c.Hosting() || c.localRounds() > 0:
		st.Mode = ModeHosted
	default:
		st.Mode = ModeMonitor
	}
	st.ServiceDown = c.serviceDown()
	st.Accounts = len(snap.Accounts)
	paused := 0
	for _, a := range snap.Accounts {
		switch a.Status {
		case ops.StatusOK:
			st.Healthy++
		case ops.StatusPaused:
			st.Problems++
			paused++
		default:
			st.Problems++
		}
		if a.NextRun != nil {
			if t, err := time.Parse(time.RFC3339, *a.NextRun); err == nil && (st.NextRun.IsZero() || t.Before(st.NextRun)) {
				st.NextRun = t
			}
		}
	}
	st.AllPaused = st.Accounts > 0 && paused == st.Accounts
	switch {
	case st.ServiceDown:
		st.Icon = IconWarn
	case st.Mode == ModeStopped:
		st.Icon = IconIdle
	case st.Problems > 0:
		st.Icon = IconWarn
	default:
		st.Icon = IconOK
	}
	return st
}

// ---- texts ----

// Lang is a display language of the tray.
type Lang string

// Languages.
const (
	LangEN Lang = "en"
	LangZH Lang = "zh"
)

// Texts are the strings of the tray menu.
type Texts struct {
	OpenWindow, Diagnose, RunNow, PauseAll, ResumeAll, StartAtLogin, Quit string
}

// MenuTexts returns the menu labels in lang.
func MenuTexts(lang Lang) Texts {
	if lang == LangZH {
		return Texts{"打开主窗口", "检查问题…", "立即运行一轮（全部账号）", "暂停全部", "恢复全部", "开机启动", "退出"}
	}
	return Texts{"Open main window", "Diagnose…", "Run a round now (all accounts)", "Pause all", "Resume all", "Start at login", "Quit"}
}

// StatusLine is the read-only status row of the menu.
func StatusLine(lang Lang, s State) string {
	zh := lang == LangZH
	switch {
	case s.ServiceDown && zh:
		return "后台服务已安装但没有运行"
	case s.ServiceDown:
		return "Background service installed but not running"
	case s.Mode == ModeStopped:
		if zh {
			return "引擎未运行"
		}
		return "Engine is not running"
	case s.Accounts == 0:
		if zh {
			return "还没有账号"
		}
		return "No accounts yet"
	}
	next := ""
	if !s.NextRun.IsZero() {
		next = s.NextRun.Local().Format("15:04")
	}
	switch {
	case s.Problems > 0 && zh:
		return fmt.Sprintf("%d 个账号需要处理", s.Problems)
	case s.Problems > 0:
		return fmt.Sprintf("%d account(s) need attention", s.Problems)
	case zh && next != "":
		return fmt.Sprintf("%d 个账号正常，下次运行 %s", s.Healthy, next)
	case zh:
		return fmt.Sprintf("%d 个账号正常", s.Healthy)
	case next != "":
		return fmt.Sprintf("%d account(s) OK, next run %s", s.Healthy, next)
	}
	return fmt.Sprintf("%d account(s) OK", s.Healthy)
}

// HostLine says who runs the engine ("" when it is this process, which needs no note).
func HostLine(lang Lang, m Mode) string {
	zh := lang == LangZH
	switch m {
	case ModeMonitor:
		if zh {
			return "由后台服务运行"
		}
		return "Run by the background service"
	case ModeHosted:
		if zh {
			return "引擎在此程序中运行"
		}
		return "Engine runs in this app"
	}
	return ""
}

// Tooltip is the tray tooltip.
func Tooltip(lang Lang, app string, s State) string {
	return app + " - " + StatusLine(lang, s)
}
