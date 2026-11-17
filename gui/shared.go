package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pkg/browser"
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
	"github.com/KKazuhaK/e5-developer-extend/internal/update"
)

// autostartArgs are the arguments the login entry starts the app with.
var autostartArgs = []string{"--tray"}

// autostartID names the login entry: the launchd label on macOS, the Run value
// on Windows, the .desktop file on Linux. It must differ from service.Label,
// which is the bundle ID Wails would use by default on macOS: both would write
// ~/Library/LaunchAgents/<label>.plist, and the login item would replace the
// background service. The Windows installer removes the Run value by this name.
const autostartID = "io.github.kkazuhak.e5extend.tray"

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

// quitFile is how the tray tells an open window that the app is quitting. Its
// content is the time of the request; the tray removes it at its next start.
func quitFile() (string, error) {
	dir, err := paths.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gui-quit"), nil
}

// shouldQuit reports whether a window started at start has to close, given the
// content and modification time of the quit file (exists is false when there
// is none). The request time is the content, an RFC 3339 timestamp; when that
// does not parse, the modification time stands in. A request that predates the
// window is left from an earlier quit and is ignored.
func shouldQuit(start time.Time, content []byte, mtime time.Time, exists bool) bool {
	if !exists {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(content)))
	if err != nil {
		at = mtime
	}
	return at.After(start)
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

// cliPath finds the e5extend command-line binary next to this program. The
// background service has to run the CLI (`e5extend run`), not the app.
func cliPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	name := "e5extend"
	if strings.HasSuffix(strings.ToLower(self), ".exe") {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(self), name)
	if _, err := os.Stat(p); err != nil {
		return "", errors.New("the background service needs the e5extend command-line program next to this app; it was not found in " + filepath.Dir(self))
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
	for _, a := range []struct{ flag, env string }{{"--config-dir", "E5EXTEND_CONFIG_DIR"}, {"--data-dir", "E5EXTEND_DATA_DIR"}} {
		if v := os.Getenv(a.env); v != "" {
			out = append(out, a.flag, v)
		}
	}
	return out
}

// restartDelay is how many seconds the helper waits before it starts the app
// again, so the old tray has let go of its single-instance lock.
const restartDelay = "6"

// restartApp closes the app and starts it again in a few seconds, so an
// upgraded program runs: the tray when one was in use (it hosts the engine or
// starts at login), the window otherwise. It does nothing on Windows, where
// the installer restarts the tray itself. The quit request closes this window
// and the tray, as the tray's Quit item does.
func restartApp(log *slog.Logger, trayInUse bool) error {
	if runtime.GOOS == "windows" {
		return errors.New("the installer restarts the app on Windows")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	mode := "--window"
	if trayInUse {
		mode = "--tray"
	}
	args := append([]string{mode}, passThroughArgs()...)
	script, first := `sleep `+restartDelay+`; exec "$0" "$@"`, exe
	if i := strings.Index(exe, ".app/Contents/MacOS/"); runtime.GOOS == "darwin" && i >= 0 {
		// A bundle is started through LaunchServices.
		script, first = `sleep `+restartDelay+`; exec open -n "$0" --args "$@"`, exe[:i+len(".app")]
	}
	cmd := append([]string{"-c", script, first}, args...)
	if err := update.StartDetached("sh", cmd...); err != nil {
		return err
	}
	if f, err := quitFile(); err == nil {
		if err := paths.WriteFileAtomic(f, []byte(time.Now().Format(time.RFC3339Nano)+"\n")); err != nil {
			log.Warn("quit request", "err", err)
		}
	}
	return nil
}

func openURL(url string) error {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return errors.New("refusing to open a non-web address")
	}
	return browser.OpenURL(url)
}

// backupFilter is the file type filter of the backup dialogs.
const backupFilter = "*.e5backup"

// saveDialogOptions describes the "save as" dialog of a backup: titled title,
// starting on defaultName, offering only *.e5backup files. This version of the
// toolkit has no SetTitle on the save dialog, so the title goes in the options;
// the message is set too for macOS, which shows it instead of a title.
func saveDialogOptions(title, defaultName string) *application.SaveFileDialogOptions {
	return &application.SaveFileDialogOptions{
		Title:                title,
		Message:              title,
		Filename:             defaultName,
		CanCreateDirectories: true,
		Filters:              []application.FileFilter{{DisplayName: "e5extend backup", Pattern: backupFilter}},
	}
}

// pickSaveFile asks where to save a backup. It returns "" when the user cancels.
func pickSaveFile(app *application.App, win *application.WebviewWindow, title, defaultName string) (string, error) {
	opts := saveDialogOptions(title, defaultName)
	if win != nil {
		opts.Window = win
	}
	// The overwrite question is the dialog's own, and the backup service relies on it
	// (it overwrites the exact path returned). The GTK3 build of Wails (linux_cgo_gtk3.go,
	// build tag gtk3) does not turn that confirmation on; release builds use GTK4, which does.
	return app.Dialog.SaveFileWithOptions(opts).PromptForSingleSelection()
}

// pickOpenFile asks which backup to open. It returns "" when the user cancels.
func pickOpenFile(app *application.App, win *application.WebviewWindow, title string) (string, error) {
	d := app.Dialog.OpenFile().SetTitle(title).CanChooseFiles(true).CanChooseDirectories(false).
		AddFilter("e5extend backup", backupFilter).AddFilter("All files", "*")
	if win != nil {
		d.AttachToWindow(win)
	}
	return d.PromptForSingleSelection()
}
