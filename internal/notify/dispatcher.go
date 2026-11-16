package notify

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Sender delivers one event.
type Sender interface {
	Enabled() bool
	Send(ctx context.Context, ev Event) error
}

type job struct {
	ev   Event
	done func(error)
}

// Dispatcher sends events from a background goroutine so the engine's tick
// never waits for the network, the vault or the keyring. A failed delivery is
// logged and reported through the callback given to Send.
type Dispatcher struct {
	// CloseWait is how long Close lets queued events finish.
	CloseWait time.Duration
	// EnabledTTL is how often the sender is asked whether it has a
	// destination. The question runs on its own goroutine.
	EnabledTTL time.Duration

	s       Sender
	log     *slog.Logger
	q       chan job
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	enabled atomic.Bool

	mu     sync.Mutex
	closed bool
}

// NewDispatcher starts the worker. log may be nil.
func NewDispatcher(s Sender, log *slog.Logger) *Dispatcher {
	return NewDispatcherTTL(s, log, 5*time.Minute)
}

// NewDispatcherTTL is NewDispatcher with an explicit Enabled refresh interval.
func NewDispatcherTTL(s Sender, log *slog.Logger, ttl time.Duration) *Dispatcher {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{CloseWait: 5 * time.Second, EnabledTTL: ttl, s: s, log: log,
		q: make(chan job, 64), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go d.work()
	go d.refresh()
	return d
}

// Enabled reports, without blocking, whether the sender had a destination the
// last time it was asked. Until the first answer arrives it is false.
func (d *Dispatcher) Enabled() bool { return d.enabled.Load() }

func (d *Dispatcher) refresh() {
	for {
		// The sender may touch the vault or the keyring and block; nothing
		// else waits for this goroutine.
		d.enabled.Store(d.s.Enabled())
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(d.EnabledTTL):
		}
	}
}

// Send queues ev and returns at once. done, if not nil, is called from the
// worker goroutine with the delivery result. When the queue is full or the
// dispatcher is closed the event is dropped and done is not called.
func (d *Dispatcher) Send(ev Event, done func(error)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	select {
	case d.q <- job{ev, done}:
	default:
		d.log.Warn("alert dropped: queue is full", "event", ev.Event, "account", ev.Account)
	}
}

// Close stops accepting events, gives the queued ones up to CloseWait to be
// delivered, then abandons the rest.
func (d *Dispatcher) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	close(d.q)
	d.mu.Unlock()
	select {
	case <-d.done:
	case <-time.After(d.CloseWait):
		d.cancel()
		<-d.done
	}
	d.cancel()
}

func (d *Dispatcher) work() {
	defer close(d.done)
	for j := range d.q {
		if d.ctx.Err() != nil {
			d.log.Warn("alert dropped at shutdown", "event", j.ev.Event, "account", j.ev.Account)
			continue
		}
		err := d.s.Send(d.ctx, j.ev)
		if err != nil {
			d.log.Warn("alert not delivered", "event", j.ev.Event, "account", j.ev.Account, "err", err.Error())
		} else {
			d.log.Info("alert delivered", "event", j.ev.Event, "account", j.ev.Account)
		}
		if j.done != nil {
			j.done(err)
		}
	}
}
