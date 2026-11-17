// Package subscription keeps what is known about a tenant's subscriptions: the
// last status and end date of each, and the renewals seen. It makes no calls;
// the engine reads Graph and records the outcome here.
package subscription

import (
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
	if ts.Failures > 0 {
		return ts.Attempted.Add(Backoff(ts.Failures))
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
		if !st.NextLifecycle.IsZero() && !next.IsZero() && next.After(st.NextLifecycle) {
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
// missing. That is a state to show, not an error, and not retried before the
// next interval.
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
