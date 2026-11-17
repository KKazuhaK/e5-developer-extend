package notify

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

const (
	// manualID names the subscription a manually entered expiry date stands for.
	manualID = "manual"
	// renewalFresh is how old a renewal may be and still be announced: after a
	// long time without a destination, old news is not worth a message.
	renewalFresh = 7 * 24 * time.Hour
	// statusDeleted is the status of a subscription that is gone.
	statusDeleted = "Deleted"
)

// tenantLabel names a tenant in a message.
func tenantLabel(key string) string {
	if name, ok := strings.CutPrefix(key, "account:"); ok {
		return "of account " + name
	}
	return "of tenant " + key
}

// evalSubscriptions raises the subscription alerts of every tenant that has
// accounts: expiring (30, 7 and 1 day before, once each), status changes and
// renewals. A date entered by hand stands in until real data is read.
func (ev *evaluator) evalSubscriptions() {
	in := ev.in
	manual := map[string]string{}
	var keys []string
	for _, a := range in.Accounts {
		key := a.TenantKey()
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
		if a.SubscriptionExpires != "" && manual[key] == "" {
			manual[key] = a.SubscriptionExpires
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		ts := in.State.Tenants[key]
		if ts == nil {
			if manual[key] == "" {
				continue
			}
			ts = &store.TenantState{}
			if in.State.Tenants == nil {
				in.State.Tenants = map[string]*store.TenantState{}
			}
			in.State.Tenants[key] = ts
			ev.changed = true
		}
		where := tenantLabel(key)
		haveReal := false
		ids := make([]string, 0, len(ts.Subscriptions))
		for id := range ts.Subscriptions {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			sub := ts.Subscriptions[id]
			if sub == nil || !in.Subscription.Watches(sub.SKU) {
				continue
			}
			haveReal = true
			ev.evalSubscription(key, where, id, sub)
		}
		s := scope{ev: ev, tenant: key, subscription: manualID, notified: &ts.Notified, sent: &ts.LastSent}
		if !haveReal && manual[key] != "" {
			date := manual[key]
			s.expiry(SubscriptionExpiring, in.Now, date, func(days int) string {
				return expiryMessage("E5 subscription "+where, "", days, date)
			})
			s.retry()
		} else {
			s.del(SubscriptionExpiring) // real data wins over the date entered by hand
		}
	}
}

func (ev *evaluator) evalSubscription(key, where, id string, sub *store.SubscriptionState) {
	now := ev.in.Now
	s := scope{ev: ev, tenant: key, subscription: id, notified: &sub.Notified, sent: &sub.LastSent}
	subject := "subscription " + sub.SKU + " " + where

	// A change of status. The first time a subscription is seen only a status
	// other than Enabled is worth a message.
	if sub.Status != "" && sub.Status != sub.AnnouncedStatus {
		prev := sub.AnnouncedStatus
		switch {
		case prev == "" && sub.Status == StatusEnabled:
		case prev == "":
			_, _ = s.announce(SubscriptionStatus, sub.Status, fmt.Sprintf(
				"%s is %s; check it in the Microsoft 365 admin center (Billing > Your products)", subject, sub.Status))
		case sub.Status == StatusEnabled:
			_, _ = s.announce(SubscriptionStatus, sub.Status, fmt.Sprintf("%s is Enabled again (was %s)", subject, prev))
		default:
			_, _ = s.announce(SubscriptionStatus, sub.Status, fmt.Sprintf(
				"%s changed from %s to %s; check it in the Microsoft 365 admin center (Billing > Your products)", subject, prev, sub.Status))
		}
		sub.AnnouncedStatus = sub.Status
		ev.changed = true
	}

	// A renewal: the end date moved later. Only the newest unannounced one is
	// told; older ones are not worth a message of their own.
	if n := len(sub.History); n > 0 {
		last := sub.History[n-1]
		if last.SeenAt.After(sub.RenewalNotified) {
			if now.Sub(last.SeenAt) <= renewalFresh {
				_, _ = s.announce(SubscriptionRenewed, last.To.UTC().Format("2006-01-02"), fmt.Sprintf(
					"%s was renewed: the end date moved from %s to %s",
					subject, last.From.UTC().Format("2006-01-02"), last.To.UTC().Format("2006-01-02")))
			}
			sub.RenewalNotified = last.SeenAt
			ev.changed = true
		}
	}

	if sub.Status != statusDeleted && !sub.NextLifecycle.IsZero() {
		date := sub.NextLifecycle.UTC().Format("2006-01-02")
		status := sub.Status
		s.expiry(SubscriptionExpiring, now.UTC(), date, func(days int) string {
			return expiryMessage(subject, status, days, date)
		})
	} else {
		s.del(SubscriptionExpiring)
	}
	s.retry()
}

// expiry announces an end date at the 30, 7 and 1 day thresholds, once each;
// a changed date starts over (the subscription was renewed).
func (s scope) expiry(kind string, now time.Time, date string, msg func(days int) string) {
	days, ok := daysLeft(now, date)
	if !ok {
		return
	}
	level := secretLevel(days)
	prevLevel, prevDate, had := 1<<30, "", false
	if n, ok := s.get(kind); ok {
		had = true
		lv, d, _ := strings.Cut(n.Key, "@")
		prevLevel, prevDate = atoiOr(lv, 1<<30), d
	}
	key := strconv.Itoa(level) + "@" + date
	switch {
	case level < 0:
		s.del(kind)
	case had && prevDate != date:
		s.del(kind)
		_, _ = s.announce(kind, key, msg(days))
	case !had || level < prevLevel:
		_, _ = s.announce(kind, key, msg(days))
	}
}

// expiryMessage says when a subscription ends. status is "" for a date entered by hand.
func expiryMessage(subject, status string, days int, date string) string {
	verb := "expires"
	if status != "" && status != StatusEnabled {
		verb = "moves on from " + status + " to its next stage"
	}
	const act = "; renew it in the Microsoft 365 admin center"
	switch {
	case days < 0:
		return fmt.Sprintf("%s passed its end date %s (%d days ago)%s", subject, date, -days, act)
	case days == 0:
		return fmt.Sprintf("%s %s today (%s)%s", subject, verb, date, act)
	case days == 1:
		return fmt.Sprintf("%s %s tomorrow (%s)%s", subject, verb, date, act)
	}
	return fmt.Sprintf("%s %s in %d days (%s)%s", subject, verb, days, date, act)
}
