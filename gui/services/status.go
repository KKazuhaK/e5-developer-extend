package services

import (
	"context"
	"errors"

	"github.com/KKazuhaK/e5-renew/internal/ops"
	"github.com/KKazuhaK/e5-renew/internal/store"
)

// StatusService reports the engine and account state and starts rounds.
type StatusService struct{ b *Backend }

// NewStatusService returns the service.
func NewStatusService(b *Backend) *StatusService { return &StatusService{b} }

// Snapshot returns the state of the engine and of every account.
func (s *StatusService) Snapshot() (Snapshot, error) { return s.b.Ops.Snapshot() }

// RunDoneEvent is emitted when a round started by RunNow in this process has ended.
const RunDoneEvent = "run:done"

// RunDone is the payload of RunDoneEvent.
type RunDone struct {
	// Error is empty when the round went through.
	Error string `json:"error"`
}

// RunNow runs a round for the named accounts (all when empty). A running
// engine is asked to do it and picks it up within about 30 seconds. With no
// engine running the round runs in this process; RunNow then returns at once
// and the event "run:done" reports the end.
func (s *StatusService) RunNow(names []string) error {
	b := s.b
	running, err := b.Ops.EngineRunning()
	if err != nil {
		return err
	}
	if running {
		_, err := b.Ops.RunNow(context.Background(), names, ops.RunNowOptions{})
		return err
	}
	b.mu.Lock()
	if b.runBusy {
		b.mu.Unlock()
		return errors.New("a round is already running")
	}
	b.runBusy = true
	b.mu.Unlock()
	go func() {
		_, err := b.Ops.RunNow(b.bgCtx, names, ops.RunNowOptions{OnEntry: func(store.Entry) {}})
		b.mu.Lock()
		b.runBusy = false
		b.mu.Unlock()
		done := RunDone{}
		if err != nil {
			done.Error = err.Error()
		}
		b.Emit(RunDoneEvent, done)
	}()
	return nil
}
