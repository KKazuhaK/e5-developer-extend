// Command e5extend is the command-line interface and background engine of E5 Developer Extend.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-developer-extend/internal/version"
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
		Use:           "e5extend",
		Short:         "Keep a Microsoft 365 E5 developer tenant active",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	var configDir, dataDir string
	root.PersistentFlags().StringVar(&configDir, "config-dir", "", "configuration folder (same as E5EXTEND_CONFIG_DIR)")
	root.PersistentFlags().StringVar(&dataDir, "data-dir", "", "data folder (same as E5EXTEND_DATA_DIR)")
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		// The flags exist for job runners that cannot set environment
		// variables (Windows scheduled tasks); they simply set them.
		for _, f := range []struct{ flag, env, val string }{
			{"config-dir", "E5EXTEND_CONFIG_DIR", configDir},
			{"data-dir", "E5EXTEND_DATA_DIR", dataDir},
		} {
			if f.val == "" {
				continue
			}
			abs, err := filepath.Abs(f.val)
			if err != nil {
				return fmt.Errorf("--%s: %w", f.flag, err)
			}
			if err := os.Setenv(f.env, abs); err != nil {
				return err
			}
		}
		return nil
	}
	root.AddCommand(newVersionCmd(), a.newConfigCmd(), a.newAccountCmd(), a.newVaultCmd(),
		a.newRunCmd(), a.newRunNowCmd(), a.newStatusCmd(), a.newLogsCmd(), a.newCatalogCmd(),
		a.newServiceCmd(), a.newDoctorCmd(), a.newNotifyCmd(), a.newInitCmd(), a.newBackupCmd(), a.newAppCmd(), a.newSubscriptionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "e5extend %s (commit %s, built %s, %s/%s)\n",
				version.Version, version.Commit, version.Date, runtime.GOOS, runtime.GOARCH)
		},
	}
}
