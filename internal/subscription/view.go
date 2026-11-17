package subscription

import (
	"slices"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// Access states of a tenant view; "unchecked" is a tenant no check has run for.
const AccessUnchecked = "unchecked"

// RenewalView is one renewal, for display.
type RenewalView struct {
	From   string `json:"from"`   // YYYY-MM-DD
	To     string `json:"to"`     // YYYY-MM-DD
	SeenAt string `json:"seenAt"` // RFC 3339
}

// SubscriptionView is one subscription of a tenant.
type SubscriptionView struct {
	ID       string `json:"id"`
	SKU      string `json:"sku"`
	Watched  bool   `json:"watched"`
	Status   string `json:"status"`
	IsTrial  bool   `json:"isTrial"`
	Licenses int    `json:"licenses"`
	// Expires is the end date (YYYY-MM-DD, UTC) and DaysLeft the days until it;
	// both are empty/nil when Microsoft gave no date.
	Expires   string        `json:"expires"`
	DaysLeft  *int          `json:"daysLeft"`
	CheckedAt string        `json:"checkedAt"`
	Renewals  []RenewalView `json:"renewals"`
}

// TenantView is what is known about the subscriptions of one tenant, for the
// command line and the desktop app. It is built from the configuration and the
// saved state; it makes no call.
type TenantView struct {
	// Tenant is the key of the tenant (see config.Account.TenantKey); Label is
	// what to show for it.
	Tenant   string   `json:"tenant"`
	Label    string   `json:"label"`
	TenantID string   `json:"tenantId"`
	Accounts []string `json:"accounts"`
	// Access is ok, no_permission, error or unchecked.
	Access        string             `json:"access"`
	Detail        string             `json:"detail"`
	CheckedAt     *string            `json:"checkedAt"`
	AttemptedAt   *string            `json:"attemptedAt"`
	NextCheckAt   *string            `json:"nextCheckAt"`
	Subscriptions []SubscriptionView `json:"subscriptions"`
	// ManualExpires is the date entered by hand; ManualDaysLeft the days until
	// it. ManualInUse is true while it stands in for missing data,
	// ManualRedundant when real data exists and the date can be removed.
	ManualExpires   string `json:"manualExpires"`
	ManualDaysLeft  *int   `json:"manualDaysLeft"`
	ManualInUse     bool   `json:"manualInUse"`
	ManualRedundant bool   `json:"manualRedundant"`
}

// Label names a tenant key for display.
func Label(key string) string {
	if name, ok := strings.CutPrefix(key, "account:"); ok {
		return name
	}
	return key
}

// DaysUntil counts calendar days from now's date to the date of t, in UTC.
func DaysUntil(now, t time.Time) int {
	y, m, d := now.UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	y, m, d = t.UTC().Date()
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Sub(today) / (24 * time.Hour))
}

// DaysUntilDate counts calendar days from now's local date to a YYYY-MM-DD date.
func DaysUntilDate(now time.Time, date string) (int, bool) {
	exp, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0, false
	}
	y, m, d := now.Date()
	return int(exp.Sub(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)) / (24 * time.Hour)), true
}

func rfc(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

// Build returns one view per tenant of the configured accounts, in the order
// the tenants first appear. st may be nil.
func Build(cfg *config.Config, st *store.State, now time.Time) []TenantView {
	var out []TenantView
	index := map[string]int{}
	for _, a := range cfg.Accounts {
		key := a.TenantKey()
		i, ok := index[key]
		if !ok {
			i = len(out)
			index[key] = i
			out = append(out, TenantView{Tenant: key, Label: Label(key), Access: AccessUnchecked, Accounts: []string{},
				Subscriptions: []SubscriptionView{}})
		}
		v := &out[i]
		v.Accounts = append(v.Accounts, a.Name)
		if a.SubscriptionExpires != "" && v.ManualExpires == "" {
			v.ManualExpires = a.SubscriptionExpires
		}
	}
	for i := range out {
		v := &out[i]
		var ts *store.TenantState
		if st != nil {
			ts = st.Tenants[v.Tenant]
		}
		if ts != nil {
			if ts.Access != "" {
				v.Access = ts.Access
			}
			v.Detail, v.TenantID = ts.Detail, ts.TenantID
			v.CheckedAt, v.AttemptedAt = rfc(ts.Checked), rfc(ts.Attempted)
			if !ts.Attempted.IsZero() {
				v.NextCheckAt = rfc(NextCheck(ts))
			}
			for id, s := range ts.Subscriptions {
				if s == nil {
					continue
				}
				sv := SubscriptionView{ID: id, SKU: s.SKU, Watched: cfg.Subscription.Watches(s.SKU), Status: s.Status,
					IsTrial: s.IsTrial, Licenses: s.TotalLicenses, CheckedAt: s.CheckedAt.Format(time.RFC3339), Renewals: []RenewalView{}}
				if !s.NextLifecycle.IsZero() {
					sv.Expires = s.NextLifecycle.UTC().Format("2006-01-02")
					d := DaysUntil(now, s.NextLifecycle)
					sv.DaysLeft = &d
				}
				for _, r := range s.History {
					sv.Renewals = append(sv.Renewals, RenewalView{From: r.From.UTC().Format("2006-01-02"), To: r.To.UTC().Format("2006-01-02"), SeenAt: r.SeenAt.Format(time.RFC3339)})
				}
				v.Subscriptions = append(v.Subscriptions, sv)
			}
			slices.SortFunc(v.Subscriptions, func(a, b SubscriptionView) int {
				// Watched ones first, then by SKU and ID.
				if a.Watched != b.Watched {
					if a.Watched {
						return -1
					}
					return 1
				}
				if c := strings.Compare(a.SKU, b.SKU); c != 0 {
					return c
				}
				return strings.Compare(a.ID, b.ID)
			})
		}
		haveReal := slices.ContainsFunc(v.Subscriptions, func(s SubscriptionView) bool { return s.Watched })
		if v.ManualExpires != "" {
			if d, ok := DaysUntilDate(now, v.ManualExpires); ok {
				v.ManualDaysLeft = &d
			}
			v.ManualInUse, v.ManualRedundant = !haveReal, haveReal
		}
	}
	return out
}
