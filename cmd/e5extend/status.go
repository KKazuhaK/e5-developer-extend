package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// staleAfter marks an account whose last success is older than this.
const staleAfter = ops.StaleAfter

func (a *app) newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the engine is running and how each account is doing",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snap, err := a.ops().Snapshot()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			now := time.Now()

			if snap.Engine.Running {
				line := "engine: running"
				if !snap.Heartbeat.IsZero() {
					line += fmt.Sprintf(" (last heartbeat %s ago)", now.Sub(snap.Heartbeat).Round(time.Second))
				}
				fmt.Fprintln(out, line)
			} else {
				line := "engine: not running"
				if !snap.Heartbeat.IsZero() {
					line += " (last seen " + snap.Heartbeat.Local().Format("2006-01-02 15:04") + ")"
				}
				fmt.Fprintln(out, line+"; "+a.ops().EngineStoppedHint())
			}

			if snap.StateErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", snap.StateErr)
			}
			if len(snap.Accounts) == 0 {
				fmt.Fprintln(out, "no accounts; add one with: e5extend account add")
				return nil
			}

			color := isTerminal(out) && os.Getenv("NO_COLOR") == ""
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSTATUS\tNEXT RUN\tLAST SUCCESS\tTODAY OK/FAILED")
			var notes []string
			for _, sum := range snap.Accounts {
				as := sum.State
				fmt.Fprintln(tw, statusRow(sum.Account, as, now, color, sum.TodayOK, sum.TodayFailed))
				if as != nil && as.Reason != "" {
					notes = append(notes, fmt.Sprintf("  %s: %s", sum.Name, as.Reason))
				}
				if sum.AutoResumeAt != nil {
					if at, err := time.Parse(time.RFC3339, *sum.AutoResumeAt); err == nil {
						notes = append(notes, fmt.Sprintf("  %s: tried again automatically at %s", sum.Name, at.Local().Format("2006-01-02 15:04")))
					}
				}
				if sum.PausedAPIs > 0 {
					notes = append(notes, fmt.Sprintf("  %s: %d APIs paused for 24 h (refused with 403 or 404)", sum.Name, sum.PausedAPIs))
				}
				if sum.TodayThrottled > 0 {
					notes = append(notes, fmt.Sprintf("  %s: %d calls throttled today (not counted as failures)", sum.Name, sum.TodayThrottled))
				}
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if len(notes) > 0 {
				fmt.Fprintln(out, "\nnotes:")
				for _, n := range notes {
					fmt.Fprintln(out, n)
				}
			}
			return nil
		},
	}
}

func statusRow(acct config.Account, as *store.AccountState, now time.Time, color bool, ok, failed int) string {
	status, next, last := "not started", "-", "never"
	stale := false
	if as != nil {
		status = as.Status
		if !as.NextRun.IsZero() {
			next = as.NextRun.Local().Format("2006-01-02 15:04")
		}
		if !as.LastSuccess.IsZero() {
			last = as.LastSuccess.Local().Format("2006-01-02 15:04")
			stale = now.Sub(as.LastSuccess) > staleAfter
		}
	}
	if acct.Paused {
		status = "paused (config)"
		next = "-"
	}
	if stale {
		if color {
			last = "\x1b[31m" + last + "\x1b[0m"
		} else {
			last += " (stale)"
		}
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%d/%d", acct.Name, status, next, last, ok, failed)
}

func isTerminal(w any) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (a *app) newLogsCmd() *cobra.Command {
	var since time.Duration
	var limit int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs [name]",
		Short: "Show the call log",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := a.ops().Store()
			if err != nil {
				return err
			}
			var account string
			if len(args) == 1 {
				account = args[0]
			}
			out := cmd.OutOrStdout()
			f := store.Filter{Account: account, Since: time.Now().Add(-since), Limit: limit}
			entries, err := st.Read(f)
			if err != nil {
				return err
			}
			if len(entries) == 0 && !follow {
				fmt.Fprintln(out, "no log entries")
				return nil
			}
			// Entries sharing the newest timestamp are remembered so the next poll does not repeat them.
			var last time.Time
			seen := map[string]bool{}
			show := func(list []store.Entry) {
				for _, en := range list {
					key := entryKey(en)
					if seen[key] || en.TS.Before(last) {
						continue
					}
					if en.TS.After(last) {
						last, seen = en.TS, map[string]bool{}
					}
					seen[key] = true
					fmt.Fprintln(out, formatEntry(en))
				}
			}
			show(entries)
			if !follow {
				return nil
			}
			return followLog(contextOrBackground(cmd), st, account, func(list []store.Entry) { show(list) }, func() time.Time { return last })
		},
	}
	cmd.Flags().DurationVar(&since, "since", 24*time.Hour, "show entries newer than this")
	cmd.Flags().IntVarP(&limit, "lines", "n", 50, "show at most this many (newest) entries; 0 for all")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new entries")
	return cmd
}

func followLog(ctx context.Context, st *store.Store, account string, show func([]store.Entry), last func() time.Time) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			since := last()
			if since.IsZero() {
				since = time.Now().Add(-time.Minute)
			}
			list, err := st.Read(store.Filter{Account: account, Since: since})
			if err != nil {
				return err
			}
			show(list)
		}
	}
}

func entryKey(en store.Entry) string {
	b, _ := json.Marshal(en)
	return string(b)
}

func formatEntry(en store.Entry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %-8s", en.TS.Local().Format("2006-01-02 15:04:05"), orDash(en.Account))
	switch {
	case en.API != "":
		fmt.Fprintf(&sb, " %-18s %3d %-12s %dms", en.API, en.Status, en.Class, en.LatencyMS)
	case en.Event == "round_end":
		fmt.Fprintf(&sb, " round_end ok=%d failed=%d", en.OK, en.Failed)
		if en.Skipped != "" {
			fmt.Fprintf(&sb, " skipped=%s", en.Skipped)
		}
		if en.Message != "" {
			fmt.Fprintf(&sb, " %s", en.Message)
		}
	default:
		fmt.Fprintf(&sb, " event=%s", en.Event)
		if en.Message != "" {
			fmt.Fprintf(&sb, " %s", en.Message)
		}
	}
	return sb.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
