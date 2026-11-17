package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/KKazuhaK/e5-developer-extend/gui/services"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
)

// runWindow runs window mode: the main window with the services behind it.
// It runs no engine. When the window closes the process ends, which also ends
// the web view's helper processes (they do not exit on their own with GTK4).
func runWindow(page string) error {
	var win *application.WebviewWindow
	var app *application.App // set below; the second-instance callback needs it
	var closing atomic.Bool  // the window is going away; do not raise it again
	app = application.New(application.Options{
		Name:        AppName,
		Description: "Keeps a Microsoft 365 developer tenant active",
		Assets:      application.AssetOptions{Handler: application.BundledAssetFileServer(assetsFS())},
		Mac:         application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
		LogLevel:    slog.LevelWarn,
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: windowID,
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				if win != nil && !closing.Load() {
					win.Restore()
					win.Show()
					win.Focus()
					// A second launch that names a page asks the open window to go there.
					if page := pageFromArgs(data.Args); page != "" {
						app.Event.Emit(NavigateEvent, NavigatePayload{Page: page})
					}
				}
			},
		},
	})

	cfgDir, err := paths.ConfigDir()
	if err != nil {
		return err
	}
	backend := services.NewBackend(&services.Backend{
		Ops:             newOps(),
		ConfigDir:       cfgDir,
		Emit:            func(name string, data any) { app.Event.Emit(name, data) },
		Autostart:       wailsAutostart{app},
		OpenURL:         openURL,
		PickSaveFile:    func(title, name string) (string, error) { return pickSaveFile(app, win, title, name) },
		PickOpenFile:    func(title string) (string, error) { return pickOpenFile(app, win, title) },
		RequestHandover: requestHandover,
		CancelHandover:  cancelHandover,
		Exit:            func() { app.Quit() },
		RestartApp: func() error {
			// A tray is in use when it runs, hosts the engine or starts at login.
			tray := runningTrayPID()
			on, _ := wailsAutostart{app}.Enabled()
			if dir, err := paths.DataDir(); err == nil && ops.EngineHost(dir) == ops.HostGUI {
				on = true
			}
			return restartApp(slog.Default(), on || tray > 0, tray)
		},
	})
	defer backend.Close()
	app.RegisterService(application.NewService(services.NewStatusService(backend)))
	app.RegisterService(application.NewService(services.NewAccountService(backend)))
	app.RegisterService(application.NewService(services.NewAppSetupService(backend)))
	app.RegisterService(application.NewService(services.NewLogService(backend)))
	app.RegisterService(application.NewService(services.NewSettingsService(backend)))
	app.RegisterService(application.NewService(services.NewHealthService(backend)))
	app.RegisterService(application.NewService(services.NewCatalogService(backend)))
	app.RegisterService(application.NewService(services.NewBackupService(backend)))
	app.RegisterService(application.NewService(services.NewSubscriptionService(backend)))
	app.RegisterService(application.NewService(services.NewUpdateService(backend)))

	win = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     AppName,
		Width:     1100,
		Height:    720,
		MinWidth:  900,
		MinHeight: 600,
		URL:       windowURL(page),
	})
	// Closing the window ends the process. The application quits by itself
	// when its last window is gone; the timer is for a quit that hangs.
	// Sign-ins and a local round are cancelled at once. An install or an
	// uninstall in progress is waited for, but not for ever. Wails frees the
	// single-instance lock only at the very end of the shutdown, after this
	// wait, and offers no way to free it earlier; a second launch during the
	// wait is ignored (see "closing").
	const idleWait = 60 * time.Second
	win.RegisterHook(events.Common.WindowClosing, func(*application.WindowEvent) {
		closing.Store(true)
		backend.Close()
		time.AfterFunc(5*time.Second, func() {
			backend.WaitIdle(idleWait)
			os.Exit(0)
		})
	})
	app.OnShutdown(func() { backend.WaitIdle(idleWait) })

	// The tray's "Quit" quits the whole app: it leaves a request in the data
	// directory, and the window closes through the normal path (the hook above).
	start := time.Now()
	stopWatch := make(chan struct{})
	app.OnShutdown(func() { close(stopWatch) })
	go watchQuit(start, stopWatch, func() { application.InvokeAsync(func() { win.Close() }) })

	if err := app.Run(); err != nil {
		return fmt.Errorf("window: %w", err)
	}
	return nil
}

// watchQuit calls closeWindow once, when the tray has asked for a quit after
// start. It checks every second until stop is closed.
func watchQuit(start time.Time, stop <-chan struct{}, closeWindow func()) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		f, err := quitFile()
		if err != nil {
			continue
		}
		fi, err := os.Stat(f)
		if err != nil {
			continue
		}
		content, _ := os.ReadFile(f)
		if shouldQuit(start, content, fi.ModTime(), true) {
			closeWindow()
			return
		}
	}
}

// NavigateEvent is emitted to the open window when a second launch passes
// --page; its data is a NavigatePayload.
const NavigateEvent = "navigate"

// NavigatePayload is the data of NavigateEvent: {"page": "<name>"}.
type NavigatePayload struct {
	Page string `json:"page"`
}

// knownPage reports whether page is one of the pages of the window.
func knownPage(page string) bool {
	switch page {
	case "overview", "accounts", "logs", "health", "settings", "about":
		return true
	}
	return false
}

// pageFromArgs returns the validated value of --page in args, or "".
func pageFromArgs(args []string) string {
	for i, a := range args {
		var v string
		switch {
		case a == "--page" || a == "-page":
			if i+1 < len(args) {
				v = args[i+1]
			}
		case strings.HasPrefix(a, "--page="):
			v = strings.TrimPrefix(a, "--page=")
		case strings.HasPrefix(a, "-page="):
			v = strings.TrimPrefix(a, "-page=")
		}
		if knownPage(v) {
			return v
		}
	}
	return ""
}

// windowURL is the address the window opens. A known page is passed as
// ?page=<name> for the frontend to pick up; the backend does not depend on it.
func windowURL(page string) string {
	if knownPage(page) {
		return "/?page=" + page
	}
	return "/"
}
