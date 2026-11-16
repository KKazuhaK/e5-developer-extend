package main

import (
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/doctor"
)

func (a *app) newDoctorCmd() *cobra.Command {
	var offline, asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check that everything needed for long-term background running is in place",
		Long: "Run health checks and print one line per check with the command that fixes it.\n" +
			"Exit code: 0 all ok, 1 warnings, 2 failures.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checks, err := a.ops().Doctor(contextOrBackground(cmd), offline)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				if err := doctor.WriteJSON(out, checks); err != nil {
					return err
				}
			} else {
				doctor.WriteText(out, checks, asciiConsole(), offline)
			}
			if code := doctor.ExitCode(checks); code != 0 {
				return exitError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "skip the checks that need the network (sign-in, clock, network)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the results as a JSON array")
	return cmd
}

// asciiConsole reports whether the check marks should be plain letters: the
// classic Windows console cannot show them.
func asciiConsole() bool {
	if os.Getenv("E5RENEW_ASCII") != "" {
		return true
	}
	return runtime.GOOS == "windows" && os.Getenv("WT_SESSION") == ""
}
