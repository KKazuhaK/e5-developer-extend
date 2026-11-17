package main

import (
	"context"
	"log/slog"
	"runtime"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/KKazuhaK/e5-developer-extend/gui/trayhost"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
)

// applicationStarted is the event that says the event loop is running.
var applicationStarted = events.Common.ApplicationStarted

// trayUI is the tray icon and its menu.
type trayUI struct {
	app  *application.App
	log  *slog.Logger
	tray *application.SystemTray
	menu *application.Menu

	mu                                              sync.Mutex
	open, runNow, pauseAll, resumeAll, status, host *application.MenuItem
	autostart, quit, diagnose, upgrade              *application.MenuItem
	lastIcon                                        trayhost.Icon
	haveIcon                                        bool
	lastState                                       trayhost.State
	lastLang                                        trayhost.Lang
	haveState                                       bool
	lastAuto                                        bool
}

func newTrayUI(app *application.App, log *slog.Logger) *trayUI {
	return &trayUI{app: app, log: log}
}

// build creates the icon and the menu. onQuit stops the engine and ends the app.
func (t *trayUI) build(ctx context.Context, tx trayhost.Texts, auto wailsAutostart, o *ops.Ops, ctl *trayhost.Controller, onQuit func()) {
	t.tray = t.app.SystemTray.New()
	if runtime.GOOS == "darwin" {
		t.tray.SetTemplateIcon(templatePNG())
	} else {
		t.tray.SetIcon(iconPNG(trayhost.IconIdle))
	}
	t.menu = t.app.NewMenu()
	t.open = t.menu.Add(tx.OpenWindow)
	t.open.OnClick(func(*application.Context) { openMainWindow(t.log, "") })
	t.diagnose = t.menu.Add(tx.Diagnose).SetHidden(true)
	t.diagnose.OnClick(func(*application.Context) { openMainWindow(t.log, "health") })
	// The upgrade item opens the window on the Overview page, where the banner
	// does the upgrade: the window process is the one that may replace this app.
	t.upgrade = t.menu.Add("").SetHidden(true)
	t.upgrade.OnClick(func(*application.Context) { openMainWindow(t.log, "overview") })
	t.menu.AddSeparator()
	t.runNow = t.menu.Add(tx.RunNow)
	t.runNow.OnClick(func(*application.Context) {
		go func() {
			// Without an engine the round runs here; the tray counts as its
			// host meanwhile, and quitting cancels it.
			defer ctl.LocalRound()()
			if _, err := o.RunNow(ctx, nil, ops.RunNowOptions{}); err != nil {
				t.log.Info("run now failed", "err", err.Error())
			}
		}()
	})
	t.pauseAll = t.menu.Add(tx.PauseAll)
	t.pauseAll.OnClick(func(*application.Context) { go forEachAccount(o, t.log, true) })
	t.resumeAll = t.menu.Add(tx.ResumeAll)
	t.resumeAll.OnClick(func(*application.Context) { go forEachAccount(o, t.log, false) })
	t.menu.AddSeparator()
	t.status = t.menu.Add("").SetEnabled(false)
	t.host = t.menu.Add("").SetEnabled(false).SetHidden(true)
	t.menu.AddSeparator()
	t.autostart = t.menu.AddCheckbox(tx.StartAtLogin, false)
	t.autostart.OnClick(func(ctx *application.Context) {
		on := ctx.ClickedMenuItem().Checked()
		if err := auto.Set(on); err != nil {
			t.log.Info("start at login failed", "err", err.Error())
			ctx.ClickedMenuItem().SetChecked(!on)
		}
	})
	t.menu.AddSeparator()
	t.quit = t.menu.Add(tx.Quit)
	t.quit.OnClick(func(*application.Context) { onQuit() })
	t.tray.SetMenu(t.menu)
	t.tray.OnClick(func() { openMainWindow(t.log, "") })
}

// forEachAccount pauses or resumes every account.
func forEachAccount(o *ops.Ops, log *slog.Logger, pause bool) {
	list, err := o.ListAccounts()
	if err != nil {
		log.Info("cannot list accounts", "err", err.Error())
		return
	}
	for _, e := range list {
		var err error
		if pause {
			_, err = o.Pause(context.Background(), e.Account.Name)
		} else {
			_, err = o.Resume(context.Background(), e.Account.Name)
		}
		if err != nil {
			log.Info("pause or resume failed", "err", err.Error())
		}
	}
}

// apply shows st: icon, tooltip and menu texts.
func (t *trayUI) apply(lang trayhost.Lang, st trayhost.State, auto wailsAutostart) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// "Start at login" can be changed from the window, so it takes part in the comparison.
	startAtLogin, _ := auto.Enabled()
	if t.haveState && t.lastState == st && t.lastLang == lang && t.lastAuto == startAtLogin {
		return // nothing to redraw
	}
	t.lastState, t.lastLang, t.lastAuto, t.haveState = st, lang, startAtLogin, true
	tx := trayhost.MenuTexts(lang)
	if runtime.GOOS != "darwin" && (!t.haveIcon || t.lastIcon != st.Icon) {
		t.tray.SetIcon(iconPNG(st.Icon))
		t.lastIcon, t.haveIcon = st.Icon, true
	}
	t.tray.SetTooltip(trayhost.Tooltip(lang, AppName, st))
	t.open.SetLabel(tx.OpenWindow)
	t.diagnose.SetLabel(tx.Diagnose).SetHidden(!st.ServiceDown)
	if st.UpdateVersion != "" {
		t.upgrade.SetLabel(tx.UpgradeLabel(st.UpdateVersion)).SetHidden(false)
	} else {
		t.upgrade.SetHidden(true)
	}
	t.runNow.SetLabel(tx.RunNow)
	t.pauseAll.SetLabel(tx.PauseAll)
	t.resumeAll.SetLabel(tx.ResumeAll)
	t.pauseAll.SetHidden(st.AllPaused)
	t.resumeAll.SetHidden(!st.AllPaused)
	t.autostart.SetLabel(tx.StartAtLogin)
	t.quit.SetLabel(tx.Quit)
	t.status.SetLabel(trayhost.StatusLine(lang, st))
	if line := trayhost.HostLine(lang, st.Mode); line != "" {
		t.host.SetLabel(line).SetHidden(false)
	} else {
		t.host.SetHidden(true)
	}
	t.autostart.SetChecked(startAtLogin)
	t.menu.Update()
}
