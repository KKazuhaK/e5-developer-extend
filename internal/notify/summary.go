package notify

import (
	"fmt"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/callstat"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
	"github.com/KKazuhaK/e5-developer-extend/internal/subscription"
)

// summaryWindow is how far back the daily summary counts calls.
const summaryWindow = 24 * time.Hour

// summaryGrace is how late after its time a summary may still go out (the
// machine was off or busy). The length of the quiet hours is added to it, so
// a summary that fell into them is sent when they end.
const summaryGrace = 2 * time.Hour

// summaryDue returns the moment of the daily summary that is due at now: the
// latest occurrence of the configured time that is not after now. ok is false
// when the summary is off.
func (ev *evaluator) summaryDue() (slot time.Time, ok bool) {
	in := ev.in
	if !ev.allowed(DailySummary) {
		return time.Time{}, false
	}
	at, on, err := in.Config.ParseDailySummaryAt()
	if !on || err != nil {
		return time.Time{}, false
	}
	y, m, d := in.Now.Date()
	slot = time.Date(y, m, d, at/60, at%60, 0, 0, in.Now.Location())
	if slot.After(in.Now) {
		slot = time.Date(y, m, d-1, at/60, at%60, 0, 0, in.Now.Location())
	}
	return slot, true
}

// quietLength is the length of the quiet hours window, zero when there is none.
func quietLength(n config.Notify) time.Duration {
	start, end, ok, err := config.ParseQuietHours(n.QuietHours)
	if !ok || err != nil {
		return 0
	}
	mins := end - start
	if mins <= 0 {
		mins += 24 * 60
	}
	return time.Duration(mins) * time.Minute
}

// evalDailySummary hands out the summary of the day once its time has come.
// What was sent is kept in the state (Notified, keyed by the day of the
// slot): a restart does not send it twice, an undelivered one is tried again
// after RetryAfter, and during the quiet hours it waits for their end.
func (ev *evaluator) evalDailySummary() {
	in := ev.in
	slot, ok := ev.summaryDue()
	if !ok {
		return
	}
	key := slot.Format("2006-01-02")
	late := in.Now.Sub(slot) > summaryGrace+quietLength(in.Config)
	if in.State.Notified == nil {
		in.State.Notified = map[string]store.Notice{}
	}
	n, had := in.State.Notified[DailySummary]
	if had && n.Key == key {
		if !n.Pending || late || ev.quiet || in.Now.Sub(n.Tried) < RetryAfter {
			return // sent already, or not yet time to try again
		}
	} else {
		if late {
			// Switched on after the time of the day, or the machine was off
			// for long: this day's summary is skipped, the next one is on time.
			in.State.Notified[DailySummary] = store.Notice{At: in.Now, Key: key}
			ev.changed = true
			return
		}
		if ev.quiet {
			return
		}
	}
	msg, rows := ev.buildSummary()
	in.State.Notified[DailySummary] = store.Notice{At: in.Now, Key: key, Pending: true, Tried: in.Now}
	ev.changed = true
	ev.events = append(ev.events, Event{Event: DailySummary, Message: msg, Time: in.Now, Attempt: in.Now, Summary: rows})
	if in.State.LastSent == nil {
		in.State.LastSent = map[string]time.Time{}
	}
	in.State.LastSent[DailySummary] = in.Now
}

// buildSummary counts the last 24 hours of every configured account.
func (ev *evaluator) buildSummary() (string, []SummaryRow) {
	in := ev.in
	since := in.Now.Add(-summaryWindow)
	days := ev.subscriptionDays()
	var rows []SummaryRow
	var ok, failed, throttled int
	for _, acct := range in.Accounts {
		row := SummaryRow{Account: acct.Name, State: SumOK}
		as := in.State.Accounts[acct.Key()]
		cn := callstat.Tally(in.Calls[acct.Key()], since)
		row.OK, row.Failed, row.Throttled = cn.OK, cn.Failed, cn.Throttled
		switch {
		case acct.Paused:
			row.State = SumPausedByUser
		case as == nil:
		case as.Status == store.StatusNeedsAttention:
			row.State = SumNeedsAttention
		case as.Status == store.StatusPaused:
			row.State = SumPaused
		case as.Status == store.StatusPeriodEnded:
			row.State = SumPeriodEnded
		}
		if as != nil {
			row.LastSuccess = as.LastSuccess
			row.PausedAPIs = callstat.PausedAPIs(as.APISkipUntil, in.Now)
		}
		if d, known := days[acct.Name]; known {
			row.SubDays, row.HasSubDays = d, true
		}
		ok, failed, throttled = ok+row.OK, failed+row.Failed, throttled+row.Throttled
		rows = append(rows, row)
	}
	return fmt.Sprintf("daily summary: %d accounts, %d calls ok, %d failed, %d throttled in the last 24 hours",
		len(rows), ok, failed, throttled), rows
}

// subscriptionDays returns, per account, the days left of the E5 subscription
// of its tenant when that is known: the nearest end date of the watched
// subscriptions, else the date entered by hand.
func (ev *evaluator) subscriptionDays() map[string]int {
	in := ev.in
	cfg := &config.Config{Accounts: in.Accounts, Subscription: in.Subscription}
	byTenant := map[string]int{}
	for _, v := range subscription.Build(cfg, in.State, in.Now) {
		days, found := 0, false
		for _, s := range v.Subscriptions {
			if s.Watched && s.DaysLeft != nil && s.Status != statusDeleted && (!found || *s.DaysLeft < days) {
				days, found = *s.DaysLeft, true
			}
		}
		if !found && v.ManualInUse && v.ManualDaysLeft != nil {
			days, found = *v.ManualDaysLeft, true
		}
		if found {
			byTenant[v.Tenant] = days
		}
	}
	out := map[string]int{}
	for _, a := range in.Accounts {
		if d, ok := byTenant[subscription.KeyOf(in.State, a)]; ok {
			out[a.Name] = d
		}
	}
	return out
}
