package main

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func (a *app) newStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats [account]",
		Short: "Show how often each API was called and how often it worked",
		Long: "Show the per-API counters the engine keeps: calls that worked, calls that failed,\n" +
			"and the last result. Without an account the counters of all accounts are added up.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var account string
			if len(args) == 1 {
				account = args[0]
			}
			rows, err := a.ops().APIStats(account)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(rows) == 0 {
				fmt.Fprintln(out, "no API calls recorded yet")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			defer tw.Flush()
			fmt.Fprintln(tw, "API\tCATEGORY\tOK\tFAILED\tTOTAL\tLAST RESULT\tLAST CALL")
			for _, r := range rows {
				last := "-"
				if r.LastAt != nil {
					if t, err := time.Parse(time.RFC3339, *r.LastAt); err == nil {
						last = t.Local().Format("2006-01-02 15:04")
					}
				}
				result := r.LastClass
				if r.LastStatus != 0 {
					result = fmt.Sprintf("%s (%d)", r.LastClass, r.LastStatus)
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\t%s\n", r.ID, r.Category, r.OK, r.Failed, r.Total, result, last)
			}
			return nil
		},
	}
}
