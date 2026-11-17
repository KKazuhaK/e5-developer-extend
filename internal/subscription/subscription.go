// Package subscription keeps what is known about a tenant's subscriptions: the
// last status and end date of each, and the renewals seen. It makes no calls;
// the engine reads Graph and records the outcome here.
package subscription

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

const (
	// Interval is the time between two checks of a tenant.
	Interval = 24 * time.Hour
	// MaxBackoff is the longest wait after failed checks.
	MaxBackoff  = 6 * time.Hour
	baseBackoff = 5 * time.Minute
	// NoPermissionRetry is how soon a tenant without the permission is looked
	// at again: the permission may be granted any minute, and the check is cheap.
	NoPermissionRetry = time.Hour
)

// Backoff is the wait after the given number of failed checks in a row:
// 5 minutes, doubling, at most 6 hours.
func Backoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	d := baseBackoff
	for i := 1; i < failures && d < MaxBackoff; i++ {
		d *= 2
	}
	return min(d, MaxBackoff)
}

// NextCheck is when a tenant is due, given what the last check left behind.
// A tenant never tried is due at once.
func NextCheck(ts *store.TenantState) time.Time {
	if ts == nil || ts.Attempted.IsZero() {
		return time.Time{}
	}
	switch {
	case ts.Failures > 0:
		return ts.Attempted.Add(Backoff(ts.Failures))
	case ts.Access == store.AccessNoPermission:
		return ts.Attempted.Add(NoPermissionRetry)
	}
	return ts.Attempted.Add(Interval)
}

// Apply records a successful read: status, end date and check time of every
// subscription; a later end date than before is a renewal. Subscriptions that
// are no longer listed are forgotten. It returns the renewals recorded.
func Apply(ts *store.TenantState, subs []graph.Subscription, now time.Time) (renewals int) {
	ts.Access, ts.Detail, ts.Failures = store.AccessOK, "", 0
	ts.Checked, ts.Attempted = now, now
	if ts.Subscriptions == nil {
		ts.Subscriptions = map[string]*store.SubscriptionState{}
	}
	seen := map[string]bool{}
	for _, g := range subs {
		if g.ID == "" {
			continue
		}
		seen[g.ID] = true
		if g.OwnerTenantID != "" {
			ts.TenantID = g.OwnerTenantID
		}
		st := ts.Subscriptions[g.ID]
		if st == nil {
			st = &store.SubscriptionState{}
			ts.Subscriptions[g.ID] = st
		}
		next := g.NextLifecycleDateTime.UTC()
		if g.NextLifecycleDateTime.IsZero() {
			next = time.Time{}
		}
		// Only an Enabled subscription is renewed: when the status gets worse the date
		// jumps to the next stage (for example the end of a grace period), which is
		// the status change, not a renewal.
		if g.Status == "Enabled" && !st.NextLifecycle.IsZero() && !next.IsZero() && next.After(st.NextLifecycle) {
			st.History = append(st.History, store.Renewal{From: st.NextLifecycle, To: next, SeenAt: now})
			if over := len(st.History) - store.MaxRenewals; over > 0 {
				st.History = slices.Delete(st.History, 0, over)
			}
			renewals++
		}
		st.SKU, st.Status, st.NextLifecycle, st.CheckedAt = g.SKUPartNumber, g.Status, next, now
		st.IsTrial, st.TotalLicenses = g.IsTrial, g.TotalLicenses
	}
	for id := range ts.Subscriptions {
		if !seen[id] {
			delete(ts.Subscriptions, id)
		}
	}
	return renewals
}

// NoPermission records that Graph refused the read: the permission is
// missing. That is a state to show, not an error; it is looked at again after
// NoPermissionRetry.
func NoPermission(ts *store.TenantState, now time.Time) {
	ts.Access, ts.Detail, ts.Failures, ts.Attempted = store.AccessNoPermission, "", 0, now
}

// Failed records a check that could not be done (network, sign-in, server).
// detail has fixed wording: it never carries provider text.
func Failed(ts *store.TenantState, now time.Time, detail string) {
	ts.Access, ts.Detail, ts.Attempted = store.AccessError, detail, now
	ts.Failures++
}

// Watched returns the subscriptions of ts that the configuration watches,
// sorted by SKU then ID.
func Watched(ts *store.TenantState, c config.Subscription) []string {
	var ids []string
	for id, s := range ts.Subscriptions {
		if s != nil && c.Watches(s.SKU) {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b string) int {
		if c := strings.Compare(ts.Subscriptions[a].SKU, ts.Subscriptions[b].SKU); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return ids
}

// permissionScopes are the permissions (delegated scopes or application roles)
// that let GET /directory/subscriptions through, the least privileged first.
var permissionScopes = []string{"Organization.Read.All", "Organization.ReadWrite.All", "Directory.Read.All", "Directory.ReadWrite.All", "Directory.AccessAsUser.All",
	"LicenseAssignment.Read.All", "LicenseAssignment.ReadWrite.All"}

// MissingPermission reports whether a token's permissions are known and none of
// them can read the subscriptions. A token with no permission claims is not
// judged: the call decides.
func MissingPermission(scopes []string) bool {
	if len(scopes) == 0 {
		return false
	}
	for _, have := range scopes {
		for _, want := range permissionScopes {
			if strings.EqualFold(have, want) {
				return false
			}
		}
	}
	return true
}

// Usable reports whether the account can be asked now: not paused, not paused
// by the engine, its run period not over, and (unless forced) not waiting for
// the person. A forced check also tries accounts that need attention: the
// sign-in may work again.
func Usable(st *store.State, a config.Account, forced bool) bool {
	if a.Paused {
		return false
	}
	var as *store.AccountState
	if st != nil {
		as = st.Accounts[a.Name]
	}
	if as == nil {
		return true // never run yet: the engine creates its state when it starts
	}
	switch as.Status {
	case store.StatusPaused, store.StatusPeriodEnded:
		return false
	case store.StatusNeedsAttention:
		return forced
	}
	return true
}

// KeyOf is the state key of the tenant of an account: the tenant GUID once a
// token has revealed it, else what the configuration says (config.Account.TenantKey).
func KeyOf(st *store.State, a config.Account) string {
	k := a.TenantKey()
	if st != nil {
		if g := st.TenantAliases[k]; g != "" {
			return g
		}
	}
	return k
}

// Tenant is a group of accounts that belong to one tenant.
type Tenant struct {
	Key      string // state key, see KeyOf
	Label    string // what to call it: a domain, a GUID or an account name
	Accounts []config.Account
}

// Group groups the configured accounts by tenant, in configuration order.
func Group(cfg *config.Config, st *store.State) []Tenant {
	var out []Tenant
	index := map[string]int{}
	for _, a := range cfg.Accounts {
		k := KeyOf(st, a)
		i, ok := index[k]
		if !ok {
			i = len(out)
			index[k] = i
			out = append(out, Tenant{Key: k})
		}
		out[i].Accounts = append(out[i].Accounts, a)
	}
	for i := range out {
		out[i].Label = labelOf(out[i].Accounts, out[i].Key)
	}
	return out
}

var guidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// labelOf prefers a domain over a GUID, and a GUID over an account name.
func labelOf(accts []config.Account, key string) string {
	best := ""
	for _, a := range accts {
		t := strings.TrimSpace(a.Tenant)
		switch k := a.TenantKey(); {
		case strings.HasPrefix(k, "account:"):
			if best == "" {
				best = strings.TrimPrefix(k, "account:")
			}
		case guidRe.MatchString(t):
			if best == "" || !strings.Contains(best, ".") {
				best = t
			}
		default:
			return t
		}
	}
	if best == "" {
		best = Label(key)
	}
	return best
}

// Merge makes tenant key from an alias of tenant to, and moves the state kept
// under from into to: the newer attempt decides the access state, and
// subscriptions and notices are united.
func Merge(st *store.State, from, to string) {
	if from == to {
		return
	}
	if st.TenantAliases == nil {
		st.TenantAliases = map[string]string{}
	}
	st.TenantAliases[from] = to
	// Anything that pointed at "from" points at "to" now.
	for k, v := range st.TenantAliases {
		if v == from {
			st.TenantAliases[k] = to
		}
	}
	src := st.Tenants[from]
	if src == nil {
		return
	}
	delete(st.Tenants, from)
	if st.Tenants == nil {
		st.Tenants = map[string]*store.TenantState{}
	}
	dst := st.Tenants[to]
	if dst == nil {
		st.Tenants[to] = src
		return
	}
	if src.Attempted.After(dst.Attempted) {
		dst.Access, dst.Detail, dst.Failures = src.Access, src.Detail, src.Failures
		dst.Checked, dst.Attempted = src.Checked, src.Attempted
	}
	if dst.TenantID == "" {
		dst.TenantID = src.TenantID
	}
	for id, s := range src.Subscriptions {
		if dst.Subscriptions == nil {
			dst.Subscriptions = map[string]*store.SubscriptionState{}
		}
		dst.Subscriptions[id] = mergeSubscription(dst.Subscriptions[id], s)
	}
	for k, n := range src.Notified {
		if dst.Notified == nil {
			dst.Notified = map[string]store.Notice{}
		}
		if _, ok := dst.Notified[k]; !ok {
			dst.Notified[k] = n
		}
	}
	for k, t := range src.LastSent {
		if dst.LastSent == nil {
			dst.LastSent = map[string]time.Time{}
		}
		if t.After(dst.LastSent[k]) {
			dst.LastSent[k] = t
		}
	}
}

// mergeSubscription unites two copies of one subscription: the newer reading
// wins, the renewal histories are combined (the same renewal once, the newest
// MaxRenewals kept), and the alert markers keep the later of the two so nothing
// already announced is announced again.
func mergeSubscription(a, b *store.SubscriptionState) *store.SubscriptionState {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	base, other := a, b
	if b.CheckedAt.After(a.CheckedAt) {
		base, other = b, a
	}
	out := *base
	out.History = append([]store.Renewal(nil), base.History...)
	for _, r := range other.History {
		if !slices.ContainsFunc(out.History, func(x store.Renewal) bool { return x.From.Equal(r.From) && x.To.Equal(r.To) }) {
			out.History = append(out.History, r)
		}
	}
	slices.SortStableFunc(out.History, func(x, y store.Renewal) int { return x.SeenAt.Compare(y.SeenAt) })
	if over := len(out.History) - store.MaxRenewals; over > 0 {
		out.History = out.History[over:]
	}
	if other.RenewalNotified.After(out.RenewalNotified) {
		out.RenewalNotified = other.RenewalNotified
	}
	if out.AnnouncedStatus == "" {
		out.AnnouncedStatus = other.AnnouncedStatus
	}
	out.Notified = mergeNotices(base.Notified, other.Notified)
	out.LastSent = map[string]time.Time{}
	for _, m := range []map[string]time.Time{base.LastSent, other.LastSent} {
		for k, t := range m {
			if t.After(out.LastSent[k]) {
				out.LastSent[k] = t
			}
		}
	}
	if len(out.LastSent) == 0 {
		out.LastSent = nil
	}
	return &out
}

// mergeNotices keeps, per kind, the later notice.
func mergeNotices(a, b map[string]store.Notice) map[string]store.Notice {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := map[string]store.Notice{}
	for _, m := range []map[string]store.Notice{a, b} {
		for k, n := range m {
			if have, ok := out[k]; !ok || n.At.After(have.At) {
				out[k] = n
			}
		}
	}
	return out
}
