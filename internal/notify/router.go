package notify

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

// Router is the alert sender of the app: it hands each event to every channel
// whose filters accept it, all at once, so a slow or failing channel does not
// hold up the others. It satisfies Sender.
type Router struct {
	// Targets returns the channels as they are now, with their secrets. It is
	// asked on every send so a change made while the engine runs takes effect.
	Targets func() ([]Target, error)
	// Configured answers Enabled cheaply, without decrypting anything.
	Configured func() bool
	Deliverer  *Deliverer
	// Record, when set, is told how each delivery went (err is nil on
	// success), so the app can show the last result of every channel.
	Record func(channelID string, err error)
}

// Enabled reports whether at least one channel exists.
func (r *Router) Enabled() bool { return r.Configured != nil && r.Configured() }

// Accepts reports whether the channel's level and event filters let ev through.
func Accepts(c config.Channel, ev Event) bool {
	if ev.Event == Test {
		return true
	}
	if config.LevelRank(ev.Level()) < config.LevelRank(c.MinLevel) {
		return false
	}
	return len(c.Events) == 0 || slices.Contains(c.Events, ev.Event)
}

// forChannel narrows ev to what the channel accepts: a digest keeps only the
// items the channel wants, and is sent as the plain event when one is left.
func forChannel(c config.Channel, ev Event) (Event, bool) {
	if ev.Event != Digest {
		return ev, Accepts(c, ev)
	}
	var items []Event
	for _, it := range ev.Items {
		if Accepts(c, it) {
			items = append(items, it)
		}
	}
	switch len(items) {
	case 0:
		return ev, false
	case 1:
		return items[0], true
	}
	ev.Items = items
	ev.Message = fmt.Sprintf("%d alerts were held during the quiet hours", len(items))
	return ev, true
}

// PartialError is returned by Send when some alerts of an event were delivered
// and others were not (a long digest split into parts, one of which failed).
// Delivered lists the alerts that need no further attempt; the rest are tried again.
type PartialError struct {
	Delivered []Event
	Err       error
}

func (e *PartialError) Error() string { return e.Err.Error() }
func (e *PartialError) Unwrap() error { return e.Err }

// Send delivers ev. It succeeds when at least one channel took the event (or
// none wanted it); it fails, naming each channel and the reason, when every
// channel that wanted it failed. A channel that failed is not tried again
// when another one succeeded: the retries inside a delivery are its chance.
// For a digest this is judged per alert: one that no channel received is
// reported in a *PartialError together with the ones that were received.
func (r *Router) Send(ctx context.Context, ev Event) error {
	targets, err := r.Targets()
	if err != nil {
		return fmt.Errorf("notification channels unavailable: %w", err)
	}
	type outcome struct {
		id     string
		wanted []Event
		sent   []Event
		err    error
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var outs []outcome
	for _, t := range targets {
		e, ok := forChannel(t.Channel, ev)
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sent, err := r.Deliverer.DeliverParts(ctx, t, e)
			if r.Record != nil {
				r.Record(t.Channel.ID, err)
			}
			mu.Lock()
			outs = append(outs, outcome{t.Channel.ID, leaves(e), sent, err})
			mu.Unlock()
		}()
	}
	wg.Wait()
	slices.SortFunc(outs, func(a, b outcome) int { return strings.Compare(a.id, b.id) })
	var failed []error
	delivered := 0
	got := map[string]bool{}
	key := func(e Event) string {
		return e.Event + "\x00" + e.Account + "\x00" + e.Attempt.String() + "\x00" + e.Time.String()
	}
	var wanted []Event
	for _, o := range outs {
		wanted = append(wanted, o.wanted...)
		for _, e := range o.sent {
			got[key(e)] = true
		}
		if o.err == nil {
			delivered++
			continue
		}
		failed = append(failed, fmt.Errorf("%s: %w", o.id, o.err))
		if r.Deliverer.Log != nil {
			r.Deliverer.Log.Warn("channel did not deliver", "channel", o.id, "event", ev.Event, "err", o.err.Error())
		}
	}
	if len(failed) == 0 {
		return nil
	}
	joined := errors.Join(failed...)
	if ev.Event == Digest {
		missing := false
		for _, w := range wanted {
			missing = missing || !got[key(w)]
		}
		if !missing {
			return nil
		}
		// Settle what some channel received, and every alert no channel wanted.
		var done []Event
		for _, it := range ev.Items {
			if got[key(it)] || !slices.ContainsFunc(wanted, func(w Event) bool { return key(w) == key(it) }) {
				done = append(done, it)
			}
		}
		return &PartialError{Delivered: done, Err: joined}
	}
	if delivered == 0 {
		return joined
	}
	return nil
}

// SendTo delivers ev to one channel, ignoring its filters: this is what
// `notify test` uses.
func (r *Router) SendTo(ctx context.Context, id string, ev Event) error {
	targets, err := r.Targets()
	if err != nil {
		return err
	}
	for _, t := range targets {
		if t.Channel.ID == id {
			err := r.Deliverer.Deliver(ctx, t, ev)
			if r.Record != nil {
				r.Record(id, err)
			}
			return err
		}
	}
	return fmt.Errorf("no channel named %q", id)
}

var _ Sender = (*Router)(nil)
