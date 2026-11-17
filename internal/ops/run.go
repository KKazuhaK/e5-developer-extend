package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/e5-renew/internal/auth"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/engine"
	"github.com/KKazuhaK/e5-renew/internal/graph"
	"github.com/KKazuhaK/e5-renew/internal/paths"
	"github.com/KKazuhaK/e5-renew/internal/store"
	"github.com/KKazuhaK/e5-renew/internal/version"
)

// EngineOptions wires the engine to the vault, proxy and config file.
func (o *Ops) EngineOptions(cfg *config.Config, cfgPath string) (engine.Options, error) {
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
			vv, err := o.OpenVault(cfg)
			v, vErr = vv, err
		})
		return v, vErr
	}

	eo := engine.DefaultOptions()
	eo.Config, eo.ConfigPath, eo.DataDir = cfg, cfgPath, dataDir
	eo.Version = version.Version
	eo.Graph = &graph.Client{HTTP: httpClient, Base: o.GraphBase}
	eo.Hint = Hint
	if o.NoCallGap {
		eo.CallGapMin, eo.CallGapMax = 0, 0
		eo.LockWait = 50 * time.Millisecond
	}
	eo.NewAuth = func(acct config.Account) (auth.Authenticator, error) {
		s, err := openVault()
		if err != nil {
			return nil, err
		}
		return o.newAuth(acct, s, httpClient)
	}
	return eo, nil
}

// NewEngine builds the background engine from the config file, with alerts
// wired up. The caller runs it with Engine.Run. cfg is the configuration it
// starts from.
func (o *Ops) NewEngine(log *slog.Logger, hooks engine.Hooks) (*engine.Engine, *config.Config, error) {
	cfg, path, err := o.LoadConfig()
	if err != nil {
		return nil, nil, err
	}
	eo, err := o.EngineOptions(cfg, path)
	if err != nil {
		return nil, nil, err
	}
	eo.Logger = log
	eo.Hooks = hooks
	eo.Notifier = o.Notifier(cfg, path, log)
	e, err := engine.New(eo)
	if err != nil {
		eo.Notifier.Close()
		return nil, nil, err
	}
	return e, cfg, nil
}

// EngineRunning reports whether an engine, in any process, holds the lock.
func (o *Ops) EngineRunning() (bool, error) {
	dir, err := paths.DataDir()
	if err != nil {
		return false, err
	}
	return engine.Running(dir), nil
}

// ReloadEngine asks a running engine to re-read the configuration now instead
// of at its next periodic check. It does nothing when no engine runs.
func (o *Ops) ReloadEngine() {
	dir, err := paths.DataDir()
	if err != nil {
		return
	}
	if engine.Running(dir) {
		_ = engine.TriggerReload(dir)
	}
}

// RunNowOptions are the callbacks of RunNow.
type RunNowOptions struct {
	// OnLocalRun is called, with no engine running, just before the round starts in this process.
	OnLocalRun func()
	// OnEntry sees every log entry of a round run in this process.
	OnEntry func(store.Entry)
}

// RunNowResult says what RunNow did.
type RunNowResult struct {
	// Targets are the accounts it ran or asked the engine to run.
	Targets []string
	// Queued is true when a running engine was asked to start the round
	// (it notices within about 30 seconds); false when the round ran here.
	Queued bool
}

// RunNow runs a round for the named accounts (all when none are named). When
// an engine is running it is asked to do it with trigger files; otherwise the
// round runs in this process. A round that could not do its work is returned
// as an error after it ended.
func (o *Ops) RunNow(ctx context.Context, names []string, opts RunNowOptions) (RunNowResult, error) {
	cfg, path, err := o.LoadConfig()
	if err != nil {
		return RunNowResult{}, err
	}
	for _, n := range names {
		if _, err := o.FindAccount(cfg, n); err != nil {
			return RunNowResult{}, err
		}
	}
	if len(cfg.Accounts) == 0 {
		return RunNowResult{}, errors.New("no accounts configured; add one with: e5extend account add")
	}
	targets := names
	if len(targets) == 0 {
		for _, ac := range cfg.Accounts {
			targets = append(targets, ac.Name)
		}
	}
	res := RunNowResult{Targets: targets}
	eo, err := o.EngineOptions(cfg, path)
	if err != nil {
		return res, err
	}
	eo.Notifier = o.Notifier(cfg, path, nil)
	if engine.Running(eo.DataDir) {
		eo.Notifier.Close()
		for _, n := range targets {
			if err := engine.TriggerRun(eo.DataDir, n); err != nil {
				return res, err
			}
		}
		res.Queued = true
		return res, nil
	}

	if opts.OnLocalRun != nil {
		opts.OnLocalRun()
	}
	var problems []string
	eo.OnEntry = func(en store.Entry) {
		if en.Event == "round_end" {
			if msg := roundProblem(en); msg != "" {
				problems = append(problems, msg)
			}
		}
		if opts.OnEntry != nil {
			opts.OnEntry(en)
		}
	}
	if err := engine.RunOnce(ctx, eo, targets); err != nil {
		if errors.Is(err, engine.ErrRunning) {
			return res, errors.New("an engine started meanwhile; try again")
		}
		return res, err
	}
	if len(problems) > 0 {
		return res, errors.New(strings.Join(problems, "\n"))
	}
	return res, nil
}

// SkippedNote describes why a round was skipped, for display after the counts.
func SkippedNote(en store.Entry) string {
	if en.Skipped == "" {
		return ""
	}
	return " (skipped: " + en.Skipped + ")"
}

// roundProblem turns a skipped round into an error message for RunNow.
func roundProblem(en store.Entry) string {
	switch en.Skipped {
	case "network":
		return fmt.Sprintf("account %s: could not reach the sign-in or Graph service; %s",
			en.Account, Hint(auth.ErrTransient, en.Account))
	case "needs_attention":
		return fmt.Sprintf("account %s needs attention: %s", en.Account, en.Message)
	case "panic":
		return fmt.Sprintf("account %s: the round failed unexpectedly", en.Account)
	}
	return ""
}
