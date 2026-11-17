package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/sdnotify"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func (a *app) newRunCmd() *cobra.Command {
	var logFormat, logFile string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the engine in the foreground until interrupted",
		Long: "Run the scheduler in the foreground. It stops cleanly on Ctrl+C or SIGTERM.\n" +
			"On Unix, SIGHUP reloads the configuration. Run logs go to stderr.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Without a console (the Windows background build) stderr is not
			// usable, so with --log-file everything that would go to the
			// console, startup errors and crash dumps included, goes there.
			var w io.Writer = cmd.ErrOrStderr()
			if logFile != "" {
				rf, err := openRotating(logFile, maxLogFile)
				if err != nil {
					return fmt.Errorf("open the log file: %w", err)
				}
				defer rf.Close()
				w = rf
				// Crash dumps get their own file: a handle to the rotating log
				// would not follow rotation (and on Windows would block it).
				if cf, err := openCrashLog(logFile); err == nil {
					defer cf.Close()
					if err := debug.SetCrashOutput(cf, debug.CrashOptions{}); err == nil {
						defer debug.SetCrashOutput(nil, debug.CrashOptions{})
					}
				}
			}
			var h slog.Handler
			switch logFormat {
			case "json":
				h = slog.NewJSONHandler(w, nil)
			default:
				h = slog.NewTextHandler(w, nil)
			}
			log := slog.New(h)
			var err error
			if logFormat != "text" && logFormat != "json" {
				err = fmt.Errorf("--log-format must be text or json, got %q", logFormat)
			} else {
				err = a.runEngine(cmd, log)
			}
			if err != nil && logFile != "" {
				log.Error("e5extend run failed", "err", err.Error())
			}
			return err
		},
	}
	cmd.Flags().StringVar(&logFormat, "log-format", "text", "run log format: text or json")
	cmd.Flags().StringVar(&logFile, "log-file", "", "append the run log to this file instead of stderr (kept to about 5 MB plus one older file)")
	return cmd
}

func (a *app) runEngine(cmd *cobra.Command, log *slog.Logger) error {
	o := a.ops()
	e, cfg, err := o.NewEngine(log, engine.Hooks{
		Ready:    func() { logSDNotify(log, sdnotify.Ready()) },
		Tick:     func() { logSDNotify(log, sdnotify.Watchdog()) },
		Stopping: func() { logSDNotify(log, sdnotify.Stopping()) },
	})
	if err != nil {
		return err
	}
	if len(cfg.Accounts) == 0 {
		log.Warn("no accounts configured; add one with: e5extend account add")
	}
	ctx := contextOrBackground(cmd)
	reloadOnSIGHUP(ctx, e.Reload)
	if err := e.Run(ctx); err != nil {
		if errors.Is(err, engine.ErrRunning) {
			return errors.New("an engine is already running for this data directory (see: e5extend status)")
		}
		return err
	}
	return nil
}

// logSDNotify logs a failed systemd notification at debug level; it only
// means systemd is not listening.
func logSDNotify(log *slog.Logger, err error) {
	if err != nil {
		log.Debug("systemd notification failed", "err", err)
	}
}

func (a *app) newRunNowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run-now [name...]",
		Short: "Run a round now for the named accounts (all when none are named)",
		Long: "If the engine is running, ask it to start the round (it notices within about 30 seconds).\n" +
			"Otherwise run the round in this process and print the result.",
		RunE: func(cmd *cobra.Command, names []string) error {
			out := cmd.OutOrStdout()
			res, err := a.ops().RunNow(contextOrBackground(cmd), names, ops.RunNowOptions{
				OnLocalRun: func() { fmt.Fprintln(out, "no engine is running; running one round here") },
				OnEntry: func(en store.Entry) {
					switch {
					case en.API != "":
						fmt.Fprintf(out, "  %-8s %-18s %3d %-12s %dms\n", en.Account, en.API, en.Status, en.Class, en.LatencyMS)
					case en.Event == "round_end":
						fmt.Fprintf(out, "  %-8s round done: %d ok, %d failed%s\n", en.Account, en.OK, en.Failed, ops.SkippedNote(en))
					case en.Event == "trigger_ignored":
						fmt.Fprintf(out, "  %-8s skipped: %s\n", en.Account, en.Message)
					}
				},
			})
			if err != nil {
				return err
			}
			if res.Queued {
				fmt.Fprintf(out, "the engine is running; asked it to run: %s\nit starts within about 30 seconds (see: e5extend logs --follow)\n", strings.Join(res.Targets, ", "))
			}
			return nil
		},
	}
}
