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
	// Subscription says which subscriptions are watched.
	Subscription config.Subscription
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
	case NeedsAttention, AutoPaused, FailureSpike, AllFailing, SubscriptionStatus:
		return true
	}
	return false
}

// cooldownFor is the cooldown of a kind: critical ones repeat twice as soon.
func (ev *evaluator) cooldownFor(kind, key string) time.Duration {
	if LevelOfKey(kind, key) == config.LevelCritical {
		return ev.cooldown / 2
	}
	return ev.cooldown
}

// scope is where the notices of one account, or of the global alerts, live.
type scope struct {
	ev      *evaluator
	account string // display name; empty for global alerts
	key     string // state key of the account
	tenant  string
	// subscription is the subscription ID of a tenant-level alert.
	subscription string
	// sku, status and end describe that subscription (end is YYYY-MM-DD); for
	// the date entered by hand only end is set.
	sku, status, end string
	notified         *map[string]store.Notice
	sent             *map[string]time.Time
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
	if n, ok := (*s.notified)[kind]; ok {
		delete(*s.notified, kind)
		// A problem that was only ever held (never sent) and has cleared leaves
		// nothing to recover from: no "recovered" for something nobody heard of.
		// The record says (Held) that nothing of this episode was announced yet;
		// a held problem that is still waiting will start the episode again when it is sent.
		if u, has := (*s.notified)[recordUnhealthy]; has && n.Held && u.Held {
			delete(*s.notified, recordUnhealthy)
		}
		s.ev.changed = true
	}
}

// event rebuilds the event of a notice. key carries what the decision was
// based on: the numbers of a failure_spike, the origin of a recovery.
func (s scope) event(kind, key, msg string, at, attempt time.Time) Event {
	e := Event{Event: kind, Account: s.account, Key: s.key, Tenant: s.tenant, Message: msg, Time: at, Attempt: attempt, Subscription: s.subscription,
		SKU: s.sku, SubStatus: s.status, EndDate: s.end}
	if sev := LevelOfKey(kind, key); sev != LevelOf(kind) {
		e.Severity = sev
	}
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

// announce records an alert and hands it out. cooled is true when the
// cooldown kept it back: it stays in the state, not announced, and goes out
// once the cooldown ends if its condition still holds. held is true when the
// quiet hours keep it back until they end.
func (s scope) announce(kind, key, msg string) (cooled, held bool) {
	ev, now := s.ev, s.ev.in.Now
	n := store.Notice{At: now, Key: key}
	if kind == Recovered && !ev.allowed(kind) {
		return false, false // one-shot and filtered out: nothing to remember
	}
	if ev.allowed(kind) {
		n.Msg = msg
		if last, ok := (*s.sent)[kind]; ok && cooldownApplies(kind) && ev.cooldown > 0 && now.Sub(last) < ev.cooldownFor(kind, key) {
			n.Cooled = true
			s.put(kind, n)
			return true, false
		}
		s.handOut(kind, &n)
	}
	s.put(kind, n)
	return false, n.Held
}

// handOut marks n as pending and either sends it now or holds it for the end
// of the quiet hours.
func (s scope) handOut(kind string, n *store.Notice) {
	ev, now := s.ev, s.ev.in.Now
	n.Cooled, n.Pending = false, true
	if ev.quiet && LevelOfKey(kind, n.Key) != config.LevelCritical {
		n.Held = true
	} else {
		n.Tried = now
		ev.events = append(ev.events, s.event(kind, n.Key, n.Msg, n.At, now))
	}
	if *s.sent == nil {
		*s.sent = map[string]time.Time{}
	}
	(*s.sent)[kind] = now
}

// beginEpisode records a problem so that its end is announced too. held says
// the problem is only waiting for the quiet hours to end; the record keeps
// Held until a problem of the episode is really announced.
func (s scope) beginEpisode(kind string, held bool) {
	u, ok := (*s.notified)[recordUnhealthy]
	switch {
	case !ok:
		s.put(recordUnhealthy, store.Notice{At: s.ev.in.Now, Key: kind, Held: held})
	case u.Held && !held:
		u.Held = false
		s.put(recordUnhealthy, u)
	}
	s.dropObsoleteRecovery()
}

// dropObsoleteRecovery forgets a recovery that was already delivered or is a
// leftover; one that is still waiting (held for the quiet hours, or sent and not
// yet confirmed) belongs to an earlier episode that was announced, and stays.
func (s scope) dropObsoleteRecovery() {
	if n, ok := (*s.notified)[Recovered]; ok && !n.Pending {
		s.del(Recovered)
	}
}

// retry hands out again what was not confirmed, releases what the cooldown
// or the quiet hours held back. Each alert is handed out at most once per RetryAfter.
func (s scope) retry() {
	ev, now := s.ev, s.ev.in.Now
	for _, kind := range sortedKinds(*s.notified) {
		n := (*s.notified)[kind]
		if !ev.allowed(kind) {
			continue
		}
		if n.Cooled {
			if last, ok := (*s.sent)[kind]; ok && ev.cooldown > 0 && now.Sub(last) < ev.cooldownFor(kind, n.Key) {
				continue
			}
			// The cooldown is over and the condition still holds: announce it now.
			s.handOut(kind, &n)
			s.put(kind, n)
			if s.account != "" {
				s.beginEpisode(kind, n.Held)
			}
			continue
		}
		if !n.Pending {
			continue
		}
		heldBack := ev.quiet && LevelOfKey(kind, n.Key) != config.LevelCritical
		if n.Held {
			if heldBack {
				continue
			}
			n.Held, n.Tried = false, now
			s.put(kind, n)
			if slices.Contains(problemKinds, kind) {
				s.beginEpisode(kind, false) // it may have been dropped while this was the only held problem
			}
			ev.held = append(ev.held, s.event(kind, n.Key, n.Msg, n.At, now))
			continue
		}
		if now.Sub(n.Tried) < RetryAfter {
			continue
		}
		if slices.ContainsFunc(ev.events, func(e Event) bool {
			return e.Key == s.key && e.Account == s.account && e.Event == kind && e.Tenant == s.tenant && e.Subscription == s.subscription
		}) {
			continue
		}
		if heldBack {
			n.Held = true
			s.put(kind, n)
			continue
		}
		n.Tried = now
		s.put(kind, n)
		ev.events = append(ev.events, s.event(kind, n.Key, n.Msg, n.At, now))
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
		as := in.State.Accounts[acct.Key()]
		if as == nil {
			continue
		}
		s := scope{ev: ev, account: acct.Name, key: acct.Key(), tenant: acct.Tenant, notified: &as.Notified, sent: &as.LastSent}
		get, del := s.get, s.del
		raise := func(kind, key, msg string) { // an announced problem starts or continues an episode
			s.dropObsoleteRecovery()
			cooled, held := s.announce(kind, key, msg)
			if !cooled {
				s.beginEpisode(kind, held)
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
			sp := callstat.SpikeOf(in.Calls[acct.Key()], in.APIs, acct.Categories, spikeRounds, spikeRate, spikeMin)
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
				// An earlier recovery still waiting to go out already says it.
				if r, pending := get(Recovered); !pending || !r.Pending {
					_, _ = s.announce(Recovered, n.Key, "is working again")
				}
			}
		}

		if as.Status == store.StatusPeriodEnded {
			if _, ok := get(PeriodEnded); !ok {
				_, _ = s.announce(PeriodEnded, "", "the run period has ended; no more rounds will run")
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
					_, _ = s.announce(SecretExpiring, strconv.Itoa(level)+"@"+acct.SecretExpires, secretMessage(days, acct.SecretExpires))
				case !had || level < prevLevel:
					_, _ = s.announce(SecretExpiring, strconv.Itoa(level)+"@"+acct.SecretExpires, secretMessage(days, acct.SecretExpires))
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
			_, _ = g.announce(AllFailing, "", fmt.Sprintf("all %d accounts that are running are failing; check the network, the proxy and the Microsoft 365 service status first", running))
		}
	} else {
		g.del(AllFailing)
	}
	g.retry()

	ev.evalSubscriptions()

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

// problemKinds are the events that start an episode ended by "recovered".
var problemKinds = []string{NeedsAttention, AutoPaused, Stale, FailureSpike}

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
// account is the account key; empty means a global alert. It reports whether the state changed.
func MarkDelivered(st *store.State, account, kind string, tried time.Time) bool {
	notified := st.Notified
	if account != "" {
		as := st.Accounts[account]
		if as == nil {
			return false
		}
		notified = as.Notified
	}
	return markNotified(notified, kind, tried)
}

// markSubscription is MarkDelivered for a subscription_* alert of a tenant.
func markSubscription(st *store.State, tenant, subscription, kind string, tried time.Time) bool {
	ts := st.Tenants[tenant]
	if ts == nil {
		return false
	}
	if subscription == manualID {
		return markNotified(ts.Notified, kind, tried)
	}
	sub := ts.Subscriptions[subscription]
	if sub == nil {
		return false
	}
	return markNotified(sub.Notified, kind, tried)
}

func markNotified(notified map[string]store.Notice, kind string, tried time.Time) bool {
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
	attempt := func(e Event) time.Time {
		if e.Attempt.IsZero() {
			return e.Time
		}
		return e.Attempt
	}
	mark := func(e Event) bool {
		if e.Subscription != "" {
			return markSubscription(st, e.Tenant, e.Subscription, e.Event, attempt(e))
		}
		key := e.Key
		if key == "" {
			key = e.Account
		}
		return MarkDelivered(st, key, e.Event, attempt(e))
	}
	if ev.Event != Digest {
		return mark(ev)
	}
	changed := false
	for _, it := range ev.Items {
		if mark(it) {
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
