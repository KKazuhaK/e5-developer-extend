package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/KKazuhaK/e5-developer-extend/gui/services"
	"github.com/KKazuhaK/e5-developer-extend/gui/trayhost"
	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
)

// refreshEvery is how often the tray reads the state.
const refreshEvery = 30 * time.Second

// runTray runs tray mode. It never creates a window or a web view: the main
// window is a separate process (see window.go).
func runTray() error {
	cfgDir, err := paths.ConfigDir()
	if err != nil {
		return err
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	log, closeLog := openLog(dataDir)
	defer closeLog()
	o := newOps()

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	var ctl *trayhost.Controller
	app := application.New(application.Options{
		Name:        AppName,
		Description: "Keeps a Microsoft 365 developer tenant active",
		LogLevel:    slog.LevelWarn,
		Mac:         application.MacOptions{ActivationPolicy: application.ActivationPolicyAccessory},
		// However the app ends (menu, SIGTERM, logout), stop the engine cleanly first.
		OnShutdown: func() {
			stop()
			if ctl != nil {
				ctl.Stop()
			}
		},
		// No window is ever created here; do not let the app end on its own.
		Linux:   application.LinuxOptions{DisableQuitOnLastWindowClosed: true},
		Windows: application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: trayID,
			// Starting the app again opens the window.
			OnSecondInstanceLaunch: func(application.SecondInstanceData) { openMainWindow(log, "") },
		},
	})

	ctl = trayhost.New(ctx, trayhost.Deps{
		Snapshot:          o.Snapshot,
		EngineRunning:     func() bool { r, _ := o.EngineRunning(); return r },
		RunEngine:         func(ctx context.Context) error { return runEngine(ctx, o, dataDir, log) },
		HandoverRequested: func() bool { f, err := handoverFile(); return takeFile(f, err) },
		ResumeRequested:   func() bool { f, err := resumeFile(); return takeFile(f, err) },
		ServiceState: func() (bool, error) {
			st, err := o.ServiceStatus()
			if errors.Is(err, service.ErrUnsupported) {
				return false, nil
			}
			return st.Installed, err
		},
		Log: log,
	})

	// Any mark of an engine host found now is left from an earlier process.
	ops.ClearEngineHost(dataDir)
	for _, f := range []func() (string, error){handoverFile, resumeFile} {
		if p, err := f(); err == nil {
			_ = os.Remove(p)
		}
	}

	auto := wailsAutostart{app}
	t := newTrayUI(app, log)
	t.build(ctx, trayhost.MenuTexts(language(cfgDir)), auto, o, ctl, func() {
		// Stopping the engine can take a while; keep the UI thread free.
		stop()
		go func() {
			ctl.Stop()
			app.Quit()
		}()
	})

	var once sync.Once
	refresh := func() {
		lang := language(cfgDir)
		st := ctl.Refresh()
		t.apply(lang, st, auto)
	}
	app.Event.OnApplicationEvent(applicationStarted, func(*application.ApplicationEvent) {
		once.Do(func() {
			refresh()
			go func() {
				tick := time.NewTicker(refreshEvery)
				fast := time.NewTicker(3 * time.Second)
				defer tick.Stop()
				defer fast.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-tick.C:
						refresh()
					case <-fast.C:
						// A handover request or a resume should not wait for the next refresh.
						if ctl.CheckHandover() || ctl.CheckResume() {
							refresh()
						}
					}
				}
			}()
		})
	})

	return app.Run()
}

// runEngine runs the engine of the tray process until ctx ends.
func runEngine(ctx context.Context, o *ops.Ops, dataDir string, log *slog.Logger) error {
	var remove func()
	e, _, err := o.NewEngine(log, engine.Hooks{
		// The lock is held from here on; tell other processes who runs the engine.
		Ready: func() {
			if r, err := ops.MarkEngineHost(dataDir); err == nil {
				remove = r
			}
		},
	})
	if err != nil {
		return err
	}
	defer func() {
		if remove != nil {
			remove()
		}
	}()
	return e.Run(ctx)
}

// language resolves the tray language from gui.json and the environment.
func language(cfgDir string) trayhost.Lang {
	switch services.ReadUI(filepath.Join(cfgDir, "gui.json")).Language {
	case "zh":
		return trayhost.LangZH
	case "en":
		return trayhost.LangEN
	}
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		if s := os.Getenv(v); s != "" {
			if strings.HasPrefix(strings.ToLower(s), "zh") {
				return trayhost.LangZH
			}
			return trayhost.LangEN
		}
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		// The OS language is not read here; English is the safe default.
		return trayhost.LangEN
	}
	return trayhost.LangEN
}

// maxLog is the size at which gui.log is moved to gui.log.1.
const maxLog = 2 << 20

// sizeRotating is an append-only log file that keeps one older generation.
type sizeRotating struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func openSizeRotating(path string) (*sizeRotating, error) {
	r := &sizeRotating{path: path}
	return r, r.open()
}

func (r *sizeRotating) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, fi.Size()
	return nil
}

func (r *sizeRotating) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > maxLog {
		_ = r.f.Close()
		_ = os.Remove(r.path + ".1")
		if err := os.Rename(r.path, r.path+".1"); err != nil {
			_ = os.Truncate(r.path, 0)
		}
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *sizeRotating) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// openLog opens the log file of the tray process. The engine logs account
// names and event details, and error texts can carry tenant or user names, so
// those attributes are dropped or replaced by fixed text.
func openLog(dataDir string) (*slog.Logger, func()) {
	nop := func() {}
	if err := paths.EnsureDir(dataDir); err != nil {
		return slog.New(slog.DiscardHandler), nop
	}
	w, err := openSizeRotating(filepath.Join(dataDir, "gui.log"))
	if err != nil {
		return slog.New(slog.DiscardHandler), nop
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo, ReplaceAttr: redactAttr})
	return slog.New(h), func() { _ = w.Close() }
}

// redactAttr keeps names and free text out of the log.
func redactAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Key {
	case "account", "detail", "panic":
		return slog.Attr{}
	case "err":
		return slog.String("err", "(details left out; run the health check)")
	}
	return a
}
