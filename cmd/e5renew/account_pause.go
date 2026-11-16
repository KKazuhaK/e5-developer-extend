package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/engine"
	"github.com/KKazuhaK/e5-renew/internal/paths"
)

func (a *app) newAccountPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause <name>",
		Short: "Stop scheduling rounds for an account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.setPaused(cmd, args[0], true)
		},
	}
}

func (a *app) newAccountResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <name>",
		Short: "Resume a paused account, or clear an automatic pause or attention state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.setPaused(cmd, args[0], false)
		},
	}
}

func (a *app) setPaused(cmd *cobra.Command, name string, paused bool) error {
	cfg, _, err := a.loadConfig()
	if err != nil {
		return err
	}
	if _, err := a.findAccount(cfg, name); err != nil {
		return err
	}
	if err := a.updateAccount(cmd, name, func(ac *config.Account) { ac.Paused = paused }); err != nil {
		return err
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	running := engine.Running(dataDir)
	if running {
		// Ask for an immediate reload instead of waiting for the periodic check.
		_ = engine.TriggerReload(dataDir)
	}
	if paused {
		fmt.Fprintf(out, "paused account %q\n", name)
		return nil
	}
	// The engine alone writes state.json, so it clears an automatic pause itself.
	if err := engine.TriggerResume(dataDir, name); err != nil {
		return err
	}
	fmt.Fprintf(out, "resumed account %q\n", name)
	if running {
		fmt.Fprintln(out, "the engine picks this up within about 30 seconds; the next round follows in a few minutes")
	} else {
		fmt.Fprintln(out, "no engine is running; it takes effect when the engine starts")
	}
	return nil
}
