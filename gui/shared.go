package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/browser"
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/KKazuhaK/e5-renew/internal/engine"
	"github.com/KKazuhaK/e5-renew/internal/ops"
	"github.com/KKazuhaK/e5-renew/internal/paths"
	"github.com/KKazuhaK/e5-renew/internal/service"
)

// autostartArgs are the arguments the login entry starts the app with.
var autostartArgs = []string{"--tray"}

// autostartID names the login entry: the launchd label on macOS, the Run value
// on Windows, the .desktop file on Linux. It must differ from service.Label,
// which is the bundle ID Wails would use by default on macOS: both would write
// ~/Library/LaunchAgents/<label>.plist, and the login item would replace the
// background service. The Windows installer removes the Run value by this name.
const autostartID = "io.github.kkazuhak.e5renew.tray"

// wailsAutostart adapts the Wails autostart manager to services.Autostart.
type wailsAutostart struct{ app *application.App }

func (w wailsAutostart) Enabled() (bool, error) { return w.app.Autostart.IsEnabled() }

func (w wailsAutostart) Set(on bool) error {
	if !on {
		return w.app.Autostart.Disable()
	}
	// A login item must point at a path that stays: not the disk image or the
	// temporary copy macOS runs a quarantined app from.
	if exe, err := os.Executable(); err == nil {
		if err := service.CheckExecutable(exe, os.TempDir()); err != nil {
			return err
		}
	}
	return w.app.Autostart.EnableWithOptions(application.AutostartOptions{Identifier: autostartID, Arguments: autostartArgs})
}

// handoverFile is how the window asks the tray to give up the engine.
func handoverFile() (string, error) {
	dir, err := paths.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gui-handover"), nil
}

// resumeFile is how the window tells the tray to take the engine back.
func resumeFile() (string, error) {
	dir, err := paths.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gui-resume"), nil
}

// cancelHandover withdraws a handover request after a failed install: the
// request file is removed (if the tray has not read it yet) and the tray is
// told to end its quiet time.
func cancelHandover() {
	if f, err := handoverFile(); err == nil {
		_ = os.Remove(f)
	}
	if f, err := resumeFile(); err == nil {
		_ = paths.WriteFileAtomic(f, []byte("resume\n"))
	}
}

// takeFile reports whether the file exists and removes it.
func takeFile(path string, err error) bool {
	if err != nil {
		return false
	}
	if _, err := os.Stat(path); err != nil {
		return false
	}
	_ = os.Remove(path)
	return true
}

// requestHandover asks the tray to release the engine and waits until the
// engine lock is free. It does nothing when the tray does not run the engine.
func requestHandover(ctx context.Context) error {
	dir, err := paths.DataDir()
	if err != nil {
		return err
	}
	if !engine.Running(dir) || ops.EngineHost(dir) != ops.HostGUI {
		return nil
	}
	f, err := handoverFile()
	if err != nil {
		return err
	}
	if err := paths.WriteFileAtomic(f, []byte("handover\n")); err != nil {
		return err
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for engine.Running(dir) {
		select {
		case <-ctx.Done():
			_ = os.Remove(f)
			return errors.New("the tray app did not hand over the engine in time; quit the tray app and try again")
		case <-tick.C:
		}
	}
	return nil
}

// cliPath finds the e5renew command-line binary next to this program. The
// background service has to run the CLI (`e5renew run`), not the app.
func cliPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	name := "e5renew"
	if strings.HasSuffix(strings.ToLower(self), ".exe") {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(self), name)
	if _, err := os.Stat(p); err != nil {
		return "", errors.New("the background service needs the e5renew command-line program next to this app; it was not found in " + filepath.Dir(self))
	}
	return p, nil
}

// newOps returns the operations layer of the app.
func newOps() *ops.Ops {
	o := ops.New()
	o.Exe = cliPath
	return o
}

// openMainWindow starts the window process and does not wait for it.
// page, when not empty, names the page to show first (passed as --page).
func openMainWindow(log *slog.Logger, page string) {
	exe, err := os.Executable()
	if err != nil {
		log.Error("cannot open the window", "err", err.Error())
		return
	}
	args := []string{"--window"}
	if page != "" {
		args = append(args, "--page", page)
	}
	cmd := exec.Command(exe, append(args, passThroughArgs()...)...)
	if err := cmd.Start(); err != nil {
		log.Error("cannot open the window", "err", err.Error())
		return
	}
	go func() { _ = cmd.Wait() }()
}

// passThroughArgs are the folder flags this process was started with.
func passThroughArgs() []string {
	var out []string
	for _, a := range []struct{ flag, env string }{{"--config-dir", "E5RENEW_CONFIG_DIR"}, {"--data-dir", "E5RENEW_DATA_DIR"}} {
		if v := os.Getenv(a.env); v != "" {
			out = append(out, a.flag, v)
		}
	}
	return out
}

func openURL(url string) error {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return errors.New("refusing to open a non-web address")
	}
	return browser.OpenURL(url)
}
