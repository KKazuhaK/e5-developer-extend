// Command e5renew is the command-line interface and background engine of e5-renew.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// After the first signal restore default handling, so a second one kills the process.
	go func() { <-ctx.Done(); stop() }()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		stop()
		var ee exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		os.Exit(1)
	}
}

// exitError makes the process exit with a specific code without printing a message.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

func newRootCmd() *cobra.Command { return newRootCmdWith(newApp()) }

func newRootCmdWith(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:           "e5renew",
		Short:         "Keep a Microsoft 365 E5 developer tenant active",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newVersionCmd(), a.newConfigCmd(), a.newAccountCmd(), a.newVaultCmd(),
		a.newRunCmd(), a.newRunNowCmd(), a.newStatusCmd(), a.newLogsCmd(), a.newCatalogCmd(),
		a.newServiceCmd(), a.newDoctorCmd(), a.newNotifyCmd(), a.newInitCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "e5renew %s (commit %s, built %s, %s/%s)\n",
				version.Version, version.Commit, version.Date, runtime.GOOS, runtime.GOARCH)
		},
	}
}
