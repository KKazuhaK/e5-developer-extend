package notify

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/callstat"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
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
	// Calls holds the recent call-log entries per account, oldest first, for
	// failure_spike. Nil means the log was not read: failure_spike is then
	// neither raised nor ended.
	Calls map[string][]store.Entry
	APIs  []catalog.API
}

// evaluator is one run of Evaluate.
type evaluator struct {
	in       Input
	events   []Event
	held     []Event // alerts whose quiet hours ended, to be sent as one summary
	changed  bool
	quiet    bool
	cooldown time.Duration
}

func (ev *evaluator) allowed(kind string) bool {
	return len(ev.in.Config.Events) == 0 || slices.Contains(ev.in.Config.Events, kind)
}

// cooldownApplies says which kinds the cooldown covers: those that can flap.
// The others have their own repetition rule (stale, secret_expiring) or are
// one-shot (recovered, period_ended).
func cooldownApplies(kind string) bool {
	switch kind {
	case NeedsAttention, AutoPaused, FailureSpike, AllFailing:
		return true
	}
	return false
}

// cooldownFor is the cooldown of a kind: critical ones repeat twice as soon.
func (ev *evaluator) cooldownFor(kind string) time.Duration {
	if LevelOf(kind) == config.LevelCritical {
		return ev.cooldown / 2
	}
	return ev.cooldown
}

// scope is where the notices of one account, or of the global alerts, live.
type scope struct {
	ev       *evaluator
	account  string // empty for global alerts
	tenant   string
	notified *map[string]store.Notice
	sent     *map[string]time.Time
}

func (s scope) get(kind string) (store.Notice, bool) { n, ok := (*s.notified)[kind]; return n, ok }
func (s scope) put(kind string, n store.Notice) {
	if *s.notified == nil {
		*s.notified = map[string]store.Notice{}
	}
	(*s.notified)[kind] = n
	s.ev.changed = true
}
func (s scope) del(kind string) {
	if _, ok := (*s.notified)[kind]; ok {
		delete(*s.notified, kind)
		s.ev.changed = true
	}
}

// event rebuilds the event of a notice. key carries what the decision was
// based on: the numbers of a failure_spike, the origin of a recovery.
func (s scope) event(kind, key, msg string, at time.Time) Event {
	e := Event{Event: kind, Account: s.account, Tenant: s.tenant, Message: msg, Time: at}
	switch kind {
	case FailureSpike:
		parts := strings.Split(key, "/")
		if len(parts) == 3 {
			e.Failures, e.Rounds = atoiOr(parts[0], 0), atoiOr(parts[1], 0)
			e.FailRate, _ = strconv.ParseFloat(parts[2], 64)
		}
	case Recovered:
		e.Origin = key
	}
	return e
}

// announce records an alert and hands it out. cooled is true when the cooldown
// swallowed it: it is then remembered as announced without ever being sent.
func (s scope) announce(kind, key, msg string) (cooled bool) {
	ev, now := s.ev, s.ev.in.Now
	n := store.Notice{At: now, Key: key}
	if kind == Recovered && !ev.allowed(kind) {
		return false // one-shot and filtered out: nothing to remember
	}
	if ev.allowed(kind) {
		if last, ok := (*s.sent)[kind]; ok && cooldownApplies(kind) && ev.cooldown > 0 && now.Sub(last) < ev.cooldownFor(kind) {
			s.put(kind, n)
			return true
		}
		n.Pending, n.Msg = true, msg
		if ev.quiet && LevelOf(kind) != config.LevelCritical {
			n.Held = true
		} else {
			n.Tried = now
			ev.events = append(ev.events, s.event(kind, key, msg, now))
		}
		if *s.sent == nil {
			*s.sent = map[string]time.Time{}
		}
		(*s.sent)[kind] = now
	}
	s.put(kind, n)
	return false
}

// retry hands out again what was not confirmed, and releases what was held.
// Each alert is handed out at most once per RetryAfter.
func (s scope) retry() {
	ev, now := s.ev, s.ev.in.Now
	for _, kind := range sortedKinds(*s.notified) {
		n := (*s.notified)[kind]
		if !n.Pending || !ev.allowed(kind) {
			continue
		}
		heldBack := ev.quiet && LevelOf(kind) != config.LevelCritical
		if n.Held {
			if heldBack {
				continue
			}
			n.Held, n.Tried = false, now
			s.put(kind, n)
			ev.held = append(ev.held, s.event(kind, n.Key, n.Msg, now))
			continue
		}
		if now.Sub(n.Tried) < RetryAfter {
			continue
		}
		if slices.ContainsFunc(ev.events, func(e Event) bool { return e.Account == s.account && e.Event == kind }) {
			continue
		}
		if heldBack {
			n.Held = true
			s.put(kind, n)
			continue
		}
		n.Tried = now
		s.put(kind, n)
		ev.events = append(ev.events, s.event(kind, n.Key, n.Msg, now))
	}
}

// Evaluate compares the account states with what has been announced already
// and returns the alerts to hand to the sender now. Announcements are
// recorded in State as pending; the caller marks them delivered with
// MarkDelivered once the sender confirms, and a pending alert is handed out
// again after RetryAfter. The caller saves State when the returned flag says
// it changed. Kinds not listed in Config.Events are recorded as delivered and
// not returned.
//
// The same alert for the same account is not sent again within the cooldown.
// During the quiet hours only critical alerts go out; the others are held and,
// once the hours end, sent as one Digest event (or as themselves when only
// one was held).
func Evaluate(in Input) (events []Event, changed bool) {
	ev := &evaluator{in: in, cooldown: in.Config.CooldownDuration(), quiet: in.Config.InQuietHours(in.Now)}
	staleAfter := in.Config.StaleDuration()
	inGrace := in.Now.Sub(in.Start) <= StaleGrace
	spikeRounds, spikeRate, spikeMin := in.Config.SpikeSettings()

	if in.State.Accounts == nil {
		in.State.Accounts = map[string]*store.AccountState{}
	}
	var running, failing int // unpaused accounts, and those of them that are failing
	for _, acct := range in.Accounts {
		as := in.State.Accounts[acct.Name]
		if as == nil {
			continue
		}
		s := scope{ev: ev, account: acct.Name, tenant: acct.Tenant, notified: &as.Notified, sent: &as.LastSent}
		get, put, del := s.get, s.put, s.del
		raise := func(kind, key, msg string) { // an announced problem starts or continues an episode
			del(Recovered)
			cooled := s.announce(kind, key, msg)
			if _, ok := get(recordUnhealthy); !ok && !cooled {
				put(recordUnhealthy, store.Notice{At: in.Now, Key: kind})
			}
		}

		// A condition that holds is announced once; one that ended is forgotten
		// so the next occurrence is announced again.
		if as.Status == store.StatusNeedsAttention {
			if _, ok := get(NeedsAttention); !ok {
				raise(NeedsAttention, as.Reason, "needs attention: "+orDefault(as.Reason, "see: e5extend status"))
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

		// A failure spike: many real failures over the last rounds. Calls that
		// found nothing do not count (see callstat). When the log holds too few
		// rounds to tell, what was announced stays as it is.
		spiking := false
		if in.Calls != nil && as.Status == store.StatusOK && !acct.Paused {
			sp := callstat.SpikeOf(in.Calls[acct.Name], in.APIs, acct.Categories, spikeRounds, spikeRate, spikeMin)
			_, had := get(FailureSpike)
			switch {
			case sp.Active:
				spiking = true
				if !had {
					raise(FailureSpike, fmt.Sprintf("%d/%d/%.4f", sp.Failures, sp.Rounds, sp.Rate), fmt.Sprintf(
						"%d of the last %d calls failed (%.0f%%) over the last %d rounds",
						sp.Failures, sp.Calls, sp.Rate*100, sp.Rounds))
				}
			case !sp.Enough:
				spiking = had
			default:
				del(FailureSpike)
			}
		} else if _, had := get(FailureSpike); had && as.Status == store.StatusOK && !acct.Paused {
			spiking = true // the log was not read: keep what is announced
		} else if had && (as.Status != store.StatusOK || acct.Paused) {
			del(FailureSpike) // another condition takes over
		}

		if !acct.Paused && as.Status != store.StatusPaused && as.Status != store.StatusPeriodEnded {
			running++
			if as.Status == store.StatusNeedsAttention || spiking {
				failing++
			}
		}

		if as.Status == store.StatusOK && !stale && !acct.Paused && !spiking {
			if n, ok := get(recordUnhealthy); ok {
				del(recordUnhealthy)
				s.announce(Recovered, n.Key, "is working again")
			}
		}

		if as.Status == store.StatusPeriodEnded {
			if _, ok := get(PeriodEnded); !ok {
				s.announce(PeriodEnded, "", "the run period has ended; no more rounds will run")
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
					s.announce(SecretExpiring, strconv.Itoa(level)+"@"+acct.SecretExpires, secretMessage(days, acct.SecretExpires))
				case !had || level < prevLevel:
					s.announce(SecretExpiring, strconv.Itoa(level)+"@"+acct.SecretExpires, secretMessage(days, acct.SecretExpires))
				}
			}
		}

		s.retry()
	}

	// Every account that is not paused fails at once: probably not the
	// accounts' own fault. One account alone is already covered by its own alerts.
	g := scope{ev: ev, notified: &in.State.Notified, sent: &in.State.LastSent}
	if running >= 2 && failing == running {
		if _, ok := g.get(AllFailing); !ok {
			g.announce(AllFailing, "", fmt.Sprintf("all %d accounts that are running are failing; check the network, the proxy and the Microsoft 365 service status first", running))
		}
	} else {
		g.del(AllFailing)
	}
	g.retry()

	// What was held during the quiet hours goes out as one message.
	switch len(ev.held) {
	case 0:
	case 1:
		ev.events = append(ev.events, ev.held[0])
	default:
		ev.events = append(ev.events, Event{Event: Digest, Time: in.Now, Items: ev.held,
			Message: fmt.Sprintf("%d alerts were held during the quiet hours", len(ev.held))})
	}
	return ev.events, ev.changed
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
// An empty account means a global alert. It reports whether the state changed.
func MarkDelivered(st *store.State, account, kind string, tried time.Time) bool {
	notified := st.Notified
	if account != "" {
		as := st.Accounts[account]
		if as == nil {
			return false
		}
		notified = as.Notified
	}
	n, ok := notified[kind]
	// A late callback from an earlier attempt must not settle a newer one.
	if !ok || !n.Tried.Equal(tried) {
		return false
	}
	if kind == Recovered {
		delete(notified, kind) // a one-shot message: nothing left to remember
		return true
	}
	if !n.Pending {
		return false
	}
	n.Pending, n.Msg = false, ""
	notified[kind] = n
	return true
}

// MarkEvent is MarkDelivered for a whole event: a digest settles every
// alert it carried.
func MarkEvent(st *store.State, ev Event) bool {
	if ev.Event != Digest {
		return MarkDelivered(st, ev.Account, ev.Event, ev.Time)
	}
	changed := false
	for _, it := range ev.Items {
		if MarkDelivered(st, it.Account, it.Event, it.Time) {
			changed = true
		}
	}
	return changed
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
		return fmt.Sprintf("client secret expired on %s; create a new one and run: e5extend account set-secret", date)
	case days == 0:
		return fmt.Sprintf("client secret expires today (%s); create a new one and run: e5extend account set-secret", date)
	case days == 1:
		return fmt.Sprintf("client secret expires tomorrow (%s); create a new one and run: e5extend account set-secret", date)
	}
	return fmt.Sprintf("client secret expires in %d days (%s); create a new one and run: e5extend account set-secret", days, date)
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
