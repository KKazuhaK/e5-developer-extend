package engine

import "time"

// Clock is the engine's source of time; tests replace it.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// SystemClock uses the real time.
type SystemClock struct{}

// Now returns the wall time in the local zone. The monotonic reading is
// stripped on purpose: it stands still while the machine sleeps, which would
// hide the jump the engine wants to see after a wake-up.
func (SystemClock) Now() time.Time { return time.Now().Round(0) }

// After waits for d.
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
