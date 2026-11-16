package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/auth"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/engine"
	"github.com/KKazuhaK/e5-renew/internal/graph"
	"github.com/KKazuhaK/e5-renew/internal/paths"
	"github.com/KKazuhaK/e5-renew/internal/store"
	"github.com/KKazuhaK/e5-renew/internal/version"
)

// engineOptions wires the engine to the vault, proxy and config file.
func (a *app) engineOptions(cfg *config.Config, cfgPath string) (engine.Options, error) {
	dataDir, err := paths.DataDir()
	if err != nil {
		return engine.Options{}, err
	}
	httpClient := auth.NewHTTPClient(cfg.Network.Proxy)

	// Open the vault on first use, so a missing master key shows up as an
	// account problem with a hint instead of stopping the whole engine.
	var once sync.Once
	var v auth.Store
	var vErr error
	openVault := func() (auth.Store, error) {
		once.Do(func() {
			vv, err := a.openVault(cfg)
			v, vErr = vv, err
		})
		return v, vErr
	}

	o := engine.DefaultOptions()
	o.Config, o.ConfigPath, o.DataDir = cfg, cfgPath, dataDir
	o.Version = version.Version
	o.Graph = &graph.Client{HTTP: httpClient, Base: a.graphBase}
	o.Hint = hint
	if a.noCallGap {
		o.CallGapMin, o.CallGapMax = 0, 0
		o.LockWait = 50 * time.Millisecond
	}
	o.NewAuth = func(acct config.Account) (auth.Authenticator, error) {
		s, err := openVault()
		if err != nil {
			return nil, err
		}
		return a.newAuth(acct, s, httpClient)
	}
	return o, nil
}

func (a *app) newRunCmd() *cobra.Command {
	var logFormat string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the engine in the foreground until interrupted",
		Long: "Run the scheduler in the foreground. It stops cleanly on Ctrl+C or SIGTERM.\n" +
			"On Unix, SIGHUP reloads the configuration. Run logs go to stderr.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var h slog.Handler
			switch logFormat {
			case "text":
				h = slog.NewTextHandler(cmd.ErrOrStderr(), nil)
			case "json":
				h = slog.NewJSONHandler(cmd.ErrOrStderr(), nil)
			default:
				return fmt.Errorf("--log-format must be text or json, got %q", logFormat)
			}
			cfg, path, err := a.loadConfig()
			if err != nil {
				return err
			}
			o, err := a.engineOptions(cfg, path)
			if err != nil {
				return err
			}
			o.Logger = slog.New(h)
			e, err := engine.New(o)
			if err != nil {
				return err
			}
			if len(cfg.Accounts) == 0 {
				o.Logger.Warn("no accounts configured; add one with: e5renew account add")
			}
			ctx := contextOrBackground(cmd)
			reloadOnSIGHUP(ctx, e.Reload)
			if err := e.Run(ctx); err != nil {
				if errors.Is(err, engine.ErrRunning) {
					return errors.New("an engine is already running for this data directory (see: e5renew status)")
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&logFormat, "log-format", "text", "run log format: text or json")
	return cmd
}

func (a *app) newRunNowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run-now [name...]",
		Short: "Run a round now for the named accounts (all when none are named)",
		Long: "If the engine is running, ask it to start the round (it notices within about 30 seconds).\n" +
			"Otherwise run the round in this process and print the result.",
		RunE: func(cmd *cobra.Command, names []string) error {
			cfg, path, err := a.loadConfig()
			if err != nil {
				return err
			}
			for _, n := range names {
				if _, err := a.findAccount(cfg, n); err != nil {
					return err
				}
			}
			if len(cfg.Accounts) == 0 {
				return errors.New("no accounts configured; add one with: e5renew account add")
			}
			targets := names
			if len(targets) == 0 {
				for _, ac := range cfg.Accounts {
					targets = append(targets, ac.Name)
				}
			}
			out := cmd.OutOrStdout()
			o, err := a.engineOptions(cfg, path)
			if err != nil {
				return err
			}
			if engine.Running(o.DataDir) {
				for _, n := range targets {
					if err := engine.TriggerRun(o.DataDir, n); err != nil {
						return err
					}
				}
				fmt.Fprintf(out, "the engine is running; asked it to run: %s\nit starts within about 30 seconds (see: e5renew logs --follow)\n", strings.Join(targets, ", "))
				return nil
			}

			fmt.Fprintln(out, "no engine is running; running one round here")
			var problems []string
			o.OnEntry = func(en store.Entry) {
				switch {
				case en.API != "":
					fmt.Fprintf(out, "  %-8s %-18s %3d %-12s %dms\n", en.Account, en.API, en.Status, en.Class, en.LatencyMS)
				case en.Event == "round_end":
					fmt.Fprintf(out, "  %-8s round done: %d ok, %d failed%s\n", en.Account, en.OK, en.Failed, skippedNote(en))
					if msg := roundProblem(en); msg != "" {
						problems = append(problems, msg)
					}
				case en.Event == "trigger_ignored":
					fmt.Fprintf(out, "  %-8s skipped: %s\n", en.Account, en.Message)
				}
			}
			if err := engine.RunOnce(contextOrBackground(cmd), o, targets); err != nil {
				if errors.Is(err, engine.ErrRunning) {
					return errors.New("an engine started meanwhile; try again")
				}
				return err
			}
			if len(problems) > 0 {
				return errors.New(strings.Join(problems, "\n"))
			}
			return nil
		},
	}
}

func skippedNote(en store.Entry) string {
	if en.Skipped == "" {
		return ""
	}
	return " (skipped: " + en.Skipped + ")"
}

// roundProblem turns a skipped round into an error message for run-now.
func roundProblem(en store.Entry) string {
	switch en.Skipped {
	case "network":
		return fmt.Sprintf("account %s: could not reach the sign-in or Graph service; %s",
			en.Account, hint(auth.ErrTransient, en.Account))
	case "needs_attention":
		return fmt.Sprintf("account %s needs attention: %s", en.Account, en.Message)
	case "panic":
		return fmt.Sprintf("account %s: the round failed unexpectedly", en.Account)
	}
	return ""
}
