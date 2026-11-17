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
	// Expires is the end date (YYYY-MM-DD, local time) and DaysLeft the days until it;
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
	// FixAccount is the account whose app registration lacks the permission to
	// read the subscriptions (the first of the tenant), FixKind its identity
	// ("delegated" or "app") and FixCommand the command line that repairs it.
	FixAccount string `json:"fixAccount"`
	FixKind    string `json:"fixKind"`
	FixCommand string `json:"fixCommand"`
	// WatchedSKUs are the SKU prefixes that alert (subscription.skus, or the default).
	WatchedSKUs []string `json:"watchedSkus"`
}

// Label names a tenant key for display.
func Label(key string) string {
	if name, ok := strings.CutPrefix(key, "account:"); ok {
		return name
	}
	return key
}

// DaysUntil counts calendar days from now's date to the date of t, both in
// now's time zone.
func DaysUntil(now, t time.Time) int {
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	y, m, d = t.In(now.Location()).Date()
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
	for _, g := range Group(cfg, st) {
		v := TenantView{Tenant: g.Key, Label: g.Label, Access: AccessUnchecked, Accounts: []string{}, Subscriptions: []SubscriptionView{}}
		for _, a := range g.Accounts {
			v.Accounts = append(v.Accounts, a.Name)
			if a.SubscriptionExpires != "" && v.ManualExpires == "" {
				v.ManualExpires = a.SubscriptionExpires
			}
		}
		v.FixAccount, v.FixKind, v.FixCommand = FixFor(g.Accounts[0])
		v.WatchedSKUs = cfg.Subscription.SKUPrefixes()
		out = append(out, v)
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
					sv.Expires = s.NextLifecycle.In(now.Location()).Format("2006-01-02")
					d := DaysUntil(now, s.NextLifecycle)
					sv.DaysLeft = &d
				}
				for _, r := range s.History {
					sv.Renewals = append(sv.Renewals, RenewalView{From: r.From.In(now.Location()).Format("2006-01-02"), To: r.To.In(now.Location()).Format("2006-01-02"), SeenAt: r.SeenAt.Format(time.RFC3339)})
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

// FixFor says how to give an account's app registration the permission that
// reads the subscriptions, without adding an account or a client secret.
func FixFor(a config.Account) (account, kind, command string) {
	if a.Auth == config.AuthClientSecret {
		return a.Name, "app", "e5extend app setup --app-only --account " + a.Name + " --no-new-secret"
	}
	cmd := "e5extend app setup"
	switch strings.ToLower(a.Tenant) {
	case "organizations", "common", "consumers", "":
	default:
		cmd += " --tenant " + a.Tenant
	}
	return a.Name, "delegated", cmd + " --adopt " + a.ClientID
}
