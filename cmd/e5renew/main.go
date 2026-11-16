// Command e5renew is the command-line interface and background engine of e5-renew.
package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/version"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "e5renew",
		Short:         "Keep a Microsoft 365 E5 developer tenant active",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newVersionCmd())
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
