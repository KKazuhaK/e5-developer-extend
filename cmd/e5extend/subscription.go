package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func (a *app) newSubscriptionCmd() *cobra.Command {
	var asJSON, cached, clearExpires bool
	var setExpires string
	cmd := &cobra.Command{
		Use:   "subscription [account]",
		Short: "Show when the E5 developer subscription ends and whether it was renewed",
		Long: "Show the subscriptions of the tenant of an account (every tenant without one): status, end date,\n" +
			"days left, trial, when it was last read and how often it was renewed.\n" +
			"The subscriptions are read from Microsoft Graph (GET /directory/subscriptions, which needs the\n" +
			"Organization.Read.All permission) first; --cached shows what the engine saved at its last daily check.\n" +
			"If the permission is missing, run: e5extend app setup, or write the end date yourself with --set-expires.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			account := ""
			if len(args) == 1 {
				account = args[0]
			}
			o := a.ops()
			out := cmd.OutOrStdout()
			if setExpires != "" && clearExpires {
				return errors.New("use --set-expires or --clear-expires, not both")
			}
			if setExpires != "" || clearExpires {
				if account == "" {
					return errors.New("name the account whose tenant the date belongs to")
				}
				if err := o.SetSubscriptionExpires(contextOrBackground(cmd), account, setExpires); err != nil {
					return err
				}
				o.ReloadEngine()
				if clearExpires {
					fmt.Fprintf(out, "removed the end date you entered for the tenant of %q\n", account)
				} else {
					fmt.Fprintf(out, "the subscription of the tenant of %q ends on %s (entered by hand)\n", account, setExpires)
				}
			}

			var tenants []ops.TenantSubscriptions
			var queued bool
			var err error
			if cached {
				tenants, err = o.Subscriptions(account)
			} else {
				var res ops.SubscriptionRefresh
				if res, err = o.RefreshSubscriptions(contextOrBackground(cmd), account); err == nil {
					tenants, queued = res.Tenants, res.Queued
				}
			}
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(tenants)
			}
			if len(tenants) == 0 {
				fmt.Fprintln(out, "no accounts; add one with: e5extend account add")
				return nil
			}
			if queued {
				fmt.Fprintln(cmd.ErrOrStderr(), "the running engine has not answered yet; showing what it saved before")
			}
			writeSubscriptions(out, tenants, time.Now())
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&asJSON, "json", false, "print JSON")
	f.BoolVar(&cached, "cached", false, "do not call Microsoft; show what the engine saved at its last check")
	f.StringVar(&setExpires, "set-expires", "", "write the end date by hand (YYYY-MM-DD) for the tenant of the account, for when the subscription cannot be read")
	f.BoolVar(&clearExpires, "clear-expires", false, "remove the end date entered by hand")
	return cmd
}

// writeSubscriptions prints the table and, under it, a note for every tenant
// whose subscriptions could not be shown.
func writeSubscriptions(out io.Writer, tenants []ops.TenantSubscriptions, now time.Time) {
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TENANT\tSKU\tSTATUS\tENDS\tDAYS LEFT\tTRIAL\tCHECKED\tRENEWALS")
	var notes []string
	for _, tv := range tenants {
		rows := 0
		for _, s := range tv.Subscriptions {
			sku := s.SKU
			if !s.Watched {
				sku += " (not watched)"
			}
			trial := "no"
			if s.IsTrial {
				trial = "yes"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n", tv.Label, sku, dash(s.Status), dash(s.Expires),
				daysCell(s.DaysLeft), trial, localTime(s.CheckedAt), len(s.Renewals))
			rows++
		}
		if tv.ManualInUse {
			fmt.Fprintf(tw, "%s\t(entered by hand)\t-\t%s\t%s\t-\t-\t-\n", tv.Label, tv.ManualExpires, daysCell(tv.ManualDaysLeft))
			rows++
		}
		if rows == 0 {
			fmt.Fprintf(tw, "%s\t-\t-\t-\t-\t-\t%s\t-\n", tv.Label, localTimePtr(tv.CheckedAt))
		}
		if n := tenantNote(tv); n != "" {
			notes = append(notes, tv.Label+": "+n)
		}
	}
	_ = tw.Flush()
	if len(notes) > 0 {
		fmt.Fprintln(out, "\nnotes:")
		for _, n := range notes {
			fmt.Fprintln(out, "  "+n)
		}
	}
	for _, tv := range tenants {
		for _, s := range tv.Subscriptions {
			if len(s.Renewals) == 0 {
				continue
			}
			fmt.Fprintf(out, "\nrenewals of %s (%s):\n", s.SKU, tv.Label)
			for _, r := range s.Renewals {
				fmt.Fprintf(out, "  %s -> %s (seen %s)\n", r.From, r.To, localTime(r.SeenAt))
			}
		}
	}
}

func tenantNote(tv ops.TenantSubscriptions) string {
	var parts []string
	switch tv.Access {
	case store.AccessNoPermission:
		parts = append(parts, "cannot read the subscription: Organization.Read.All is needed (run: e5extend app setup to add it)")
		if tv.ManualExpires == "" {
			parts = append(parts, "or enter the end date yourself: e5extend subscription <account> --set-expires YYYY-MM-DD")
		}
	case store.AccessError:
		parts = append(parts, "the last check failed: "+tv.Detail)
	case "unchecked":
		parts = append(parts, "not checked yet")
	}
	if tv.ManualRedundant {
		parts = append(parts, "the end date you entered ("+tv.ManualExpires+") is no longer needed; remove it with --clear-expires")
	}
	if tv.ManualInUse && tv.Access == store.AccessOK {
		parts = append(parts, "no watched subscription was found; the date you entered is used")
	}
	return strings.Join(parts, "; ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func daysCell(d *int) string {
	if d == nil {
		return "-"
	}
	return fmt.Sprint(*d)
}

func localTime(rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func localTimePtr(s *string) string {
	if s == nil {
		return "never"
	}
	return localTime(*s)
}
