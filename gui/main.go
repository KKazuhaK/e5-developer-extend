// Command e5extend-gui is the desktop app of E5 Developer Extend. It starts in one of two
// modes (docs/design/gui.md, section 1):
//
//	--tray    (default) tray icon and menu, runs the engine; never creates a web view
//	--window  the main window; the process ends when the window closes
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// AppName is the product name shown in titles and the tray. The packages use
// the same name (packaging/, scripts/release/); the command stays "e5extend".
const AppName = "E5 Developer Extend"

// Single-instance IDs of the two modes.
const (
	trayID   = "io.github.kkazuhak.e5extend.tray"
	windowID = "io.github.kkazuhak.e5extend.window"
)

func main() {
	var tray, window bool
	var page string
	var configDir, dataDir string
	fs := flag.NewFlagSet("e5extend-gui", flag.ExitOnError)
	fs.BoolVar(&tray, "tray", false, "run the tray icon and the engine (default)")
	fs.BoolVar(&window, "window", false, "open the main window")
	fs.StringVar(&page, "page", "", "first page of the window (with --window): overview, accounts, logs, health, settings or about")
	fs.StringVar(&configDir, "config-dir", "", "configuration folder (same as E5EXTEND_CONFIG_DIR)")
	fs.StringVar(&dataDir, "data-dir", "", "data folder (same as E5EXTEND_DATA_DIR)")
	_ = fs.Parse(os.Args[1:])
	if tray && window {
		fmt.Fprintln(os.Stderr, "choose one of --tray and --window")
		os.Exit(2)
	}
	for _, f := range []struct{ env, val string }{{"E5EXTEND_CONFIG_DIR", configDir}, {"E5EXTEND_DATA_DIR", dataDir}} {
		if f.val == "" {
			continue
		}
		abs, err := filepath.Abs(f.val)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		_ = os.Setenv(f.env, abs)
	}

	newOps().CleanupUpdateLeftovers()

	var err error
	if window {
		err = runWindow(page)
	} else {
		err = runTray()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "e5extend-gui:", err)
		os.Exit(1)
	}
}
