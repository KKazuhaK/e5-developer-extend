package notify

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/store"
)

const (
	// staleRepeat is the shortest time between two stale reminders.
	staleRepeat = 24 * time.Hour
	// RetryAfter is how long an undelivered alert waits before it is handed
	// to the sender again.
	RetryAfter = time.Hour
	// StaleGrace is how long the engine must have been running before a stale
	// alert may be raised: after the machine was off, the first rounds come soon.
	StaleGrace = time.Hour

	// recordUnhealthy marks an episode that has been announced; `recovered`
	// ends it. It is not an event kind.
	recordUnhealthy = "unhealthy"
)

// Input is what Evaluate looks at.
type Input struct {
	Now      time.Time
	Start    time.Time // when this engine process started
	Config   config.Notify
	Accounts []config.Account
	State    *store.State
}

// Evaluate compares the account states with what has been announced already
// and returns the alerts to hand to the sender now. Announcements are
// recorded in State as pending; the caller marks them delivered with
// MarkDelivered once the sender confirms, and a pending alert is handed out
// again after RetryAfter. The caller saves State when the returned flag says
// it changed. Kinds not listed in Config.Events are recorded as delivered and
// not returned.
func Evaluate(in Input) (events []Event, changed bool) {
	allowed := func(kind string) bool {
		return len(in.Config.Events) == 0 || slices.Contains(in.Config.Events, kind)
	}
	staleAfter := in.Config.StaleDuration()
	inGrace := in.Now.Sub(in.Start) <= StaleGrace

	for _, acct := range in.Accounts {
		as := in.State.Accounts[acct.Name]
		if as == nil {
			continue
		}
		get := func(kind string) (store.Notice, bool) { n, ok := as.Notified[kind]; return n, ok }
		put := func(kind string, n store.Notice) {
			if as.Notified == nil {
				as.Notified = map[string]store.Notice{}
			}
			as.Notified[kind] = n
			changed = true
		}
		del := func(kind string) {
			if _, ok := as.Notified[kind]; ok {
				delete(as.Notified, kind)
				changed = true
			}
		}
		// announce records an alert and hands it out.
		announce := func(kind, key, msg string) {
			n := store.Notice{At: in.Now, Key: key}
			if kind == Recovered && !allowed(kind) {
				return // one-shot and filtered out: nothing to remember
			}
			if allowed(kind) {
				n.Pending, n.Tried, n.Msg = true, in.Now, msg
				events = append(events, Event{Event: kind, Account: acct.Name, Message: msg, Time: in.Now})
			}
			put(kind, n)
		}
		raise := func(kind, key, msg string) { // an announced problem starts or continues an episode
			del(Recovered)
			announce(kind, key, msg)
			if _, ok := get(recordUnhealthy); !ok {
				put(recordUnhealthy, store.Notice{At: in.Now, Key: kind})
			}
		}

		// A condition that holds is announced once; one that ended is forgotten
		// so the next occurrence is announced again.
		if as.Status == store.StatusNeedsAttention {
			if _, ok := get(NeedsAttention); !ok {
				raise(NeedsAttention, as.Reason, "needs attention: "+orDefault(as.Reason, "see: e5renew status"))
			}
		} else {
			del(NeedsAttention)
		}
		if as.Status == store.StatusPaused {
			if _, ok := get(AutoPaused); !ok {
				raise(AutoPaused, "", "paused automatically: "+orDefault(as.Reason, "repeated failures"))
			}
		} else {
			del(AutoPaused)
		}

		// Stale is judged from LastSuccess alone. Right after a start it is not
		// raised (a round is about to run), but what was announced before the
		// restart stays as it is until a success newer than the threshold clears it.
		stale := !acct.Paused && as.Status != store.StatusPeriodEnded && !as.LastSuccess.IsZero() &&
			in.Now.Sub(as.LastSuccess) > staleAfter
		if stale {
			if !inGrace {
				n, ok := get(Stale)
				if !ok || in.Now.Sub(n.At) >= staleRepeat {
					raise(Stale, "", fmt.Sprintf("no successful call for %s (last success %s)",
						humanDuration(in.Now.Sub(as.LastSuccess)), as.LastSuccess.In(in.Now.Location()).Format("2006-01-02 15:04 MST")))
				}
			}
		} else {
			del(Stale)
		}

		if as.Status == store.StatusOK && !stale && !acct.Paused {
			if _, ok := get(recordUnhealthy); ok {
				del(recordUnhealthy)
				announce(Recovered, "", "is working again")
			}
		}

		if as.Status == store.StatusPeriodEnded {
			if _, ok := get(PeriodEnded); !ok {
				announce(PeriodEnded, "", "the run period has ended; no more rounds will run")
			}
		} else {
			del(PeriodEnded)
		}

		if acct.NeedsSecret() && acct.SecretExpires != "" {
			if days, ok := daysLeft(in.Now, acct.SecretExpires); ok {
				level := secretLevel(days)
				prevLevel, prevDate, had := 1<<30, "", false
				if n, ok := get(SecretExpiring); ok {
					had = true
					lv, date, _ := strings.Cut(n.Key, "@")
					prevLevel, prevDate = atoiOr(lv, 1<<30), date
				}
				switch {
				case level < 0:
					del(SecretExpiring)
				case had && prevDate != acct.SecretExpires:
					// The expiry date changed: the secret was renewed. Start over.
					del(SecretExpiring)
					announce(SecretExpiring, strconv.Itoa(level)+"@"+acct.SecretExpires, secretMessage(days, acct.SecretExpires))
				case !had || level < prevLevel:
					announce(SecretExpiring, strconv.Itoa(level)+"@"+acct.SecretExpires, secretMessage(days, acct.SecretExpires))
				}
			}
		}

		// Alerts that were handed out but not confirmed are tried again, at
		// most once per RetryAfter each.
		for _, kind := range sortedKinds(as.Notified) {
			n := as.Notified[kind]
			if !n.Pending || in.Now.Sub(n.Tried) < RetryAfter || !allowed(kind) {
				continue
			}
			if slices.ContainsFunc(events, func(e Event) bool { return e.Account == acct.Name && e.Event == kind }) {
				continue
			}
			n.Tried = in.Now
			put(kind, n)
			events = append(events, Event{Event: kind, Account: acct.Name, Message: n.Msg, Time: in.Now})
		}
	}
	return events, changed
}

func sortedKinds(m map[string]store.Notice) []string {
	k := make([]string, 0, len(m))
	for kind := range m {
		k = append(k, kind)
	}
	slices.Sort(k)
	return k
}

// MarkDelivered records that the alert of the given kind for the account has
// reached the destination for the attempt made at tried (the event Time).
// It reports whether the state changed.
func MarkDelivered(st *store.State, account, kind string, tried time.Time) bool {
	as := st.Accounts[account]
	if as == nil {
		return false
	}
	n, ok := as.Notified[kind]
	// A late callback from an earlier attempt must not settle a newer one.
	if !ok || !n.Tried.Equal(tried) {
		return false
	}
	if kind == Recovered {
		delete(as.Notified, kind) // a one-shot message: nothing left to remember
		return true
	}
	if !n.Pending {
		return false
	}
	n.Pending, n.Msg = false, ""
	as.Notified[kind] = n
	return true
}

// secretLevel maps the days left to the threshold reached: 30, 7, 1 or 0
// (expiry day or later); -1 when none is reached yet.
func secretLevel(days int) int {
	switch {
	case days <= 0:
		return 0
	case days <= 1:
		return 1
	case days <= 7:
		return 7
	case days <= 30:
		return 30
	}
	return -1
}

func secretMessage(days int, date string) string {
	switch {
	case days < 0:
		return fmt.Sprintf("client secret expired on %s; create a new one and run: e5renew account set-secret", date)
	case days == 0:
		return fmt.Sprintf("client secret expires today (%s); create a new one and run: e5renew account set-secret", date)
	case days == 1:
		return fmt.Sprintf("client secret expires tomorrow (%s); create a new one and run: e5renew account set-secret", date)
	}
	return fmt.Sprintf("client secret expires in %d days (%s); create a new one and run: e5renew account set-secret", days, date)
}

// daysLeft counts calendar days from now's local date to the YYYY-MM-DD date.
func daysLeft(now time.Time, date string) (int, bool) {
	exp, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0, false
	}
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return int(exp.Sub(today) / (24 * time.Hour)), true
}

// DaysLeft is the exported form of daysLeft for the health check.
func DaysLeft(now time.Time, date string) (int, bool) { return daysLeft(now, date) }

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func humanDuration(d time.Duration) string {
	d = d.Round(time.Hour)
	if d < 48*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}
