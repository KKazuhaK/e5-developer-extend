package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
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
	o := a.ops()
	ctx := contextOrBackground(cmd)
	var res ops.PauseResult
	var err error
	if paused {
		res, err = o.Pause(ctx, name)
	} else {
		res, err = o.Resume(ctx, name)
	}
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if paused {
		fmt.Fprintf(out, "paused account %q\n", name)
		return nil
	}
	fmt.Fprintf(out, "resumed account %q\n", name)
	if res.EngineRunning {
		fmt.Fprintln(out, "the engine picks this up within about 30 seconds; the next round follows in a few minutes")
	} else {
		fmt.Fprintln(out, "no engine is running; it takes effect when the engine starts")
	}
	return nil
}
