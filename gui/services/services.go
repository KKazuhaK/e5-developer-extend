// Package services holds the Wails services of the desktop app. Each one is a
// thin wrapper around internal/ops; the exported methods and types here are
// the contract with the frontend (docs/design/gui.md, section 3).
//
// Nothing in this package returns or emits a secret, a token, the sign-in
// name a token was issued to, or the webhook address. AccountService.Get does
// return the configured user name of an account, because the edit form needs
// it, and the proxy address with the password of its user info hidden.
package services

import (
	"context"
	"sync"
	"time"

	"github.com/KKazuhaK/e5-renew/internal/ops"
)

// Emitter sends an event to the frontend.
type Emitter func(name string, data any)

// Autostart controls "start at login" of the app.
type Autostart interface {
	Enabled() (bool, error)
	Set(on bool) error
}

// Backend is what every service shares.
type Backend struct {
	Ops *ops.Ops
	// ConfigDir holds gui.json; it is the e5renew config directory.
	ConfigDir string
	Emit      Emitter
	Autostart Autostart
	// OpenURL opens a web address in the default browser.
	OpenURL func(url string) error
	// PickSaveFile shows the system "save as" dialog and returns the chosen
	// path, or "" when the user cancels.
	PickSaveFile func(title, defaultName string) (string, error)
	// PickOpenFile shows the system "open" dialog and returns the chosen path,
	// or "" when the user cancels.
	PickOpenFile func(title string) (string, error)
	// RequestHandover asks the process that runs the engine inside the tray to
	// give it up (needed before installing the background service) and
	// waits until the engine lock is free or the timeout passes.
	RequestHandover func(ctx context.Context) error
	// CancelHandover withdraws a handover request and tells the tray to take the engine back.
	CancelHandover func()

	mu       sync.Mutex
	logins   map[string]context.CancelFunc
	runBusy  bool
	busy     int // install, uninstall and similar jobs in progress
	bgCtx    context.Context
	bgCancel context.CancelFunc
}

// NewBackend fills in the pieces of b that are left nil and returns it.
func NewBackend(b *Backend) *Backend {
	if b.Ops == nil {
		b.Ops = ops.New()
	}
	if b.Emit == nil {
		b.Emit = func(string, any) {}
	}
	if b.OpenURL == nil {
		b.OpenURL = func(string) error { return nil }
	}
	b.logins = map[string]context.CancelFunc{}
	b.bgCtx, b.bgCancel = context.WithCancel(context.Background())
	return b
}

// begin marks a job that must not be cut short by the process ending; the
// returned function ends it.
func (b *Backend) begin() func() {
	b.mu.Lock()
	b.busy++
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		b.busy--
		b.mu.Unlock()
	}
}

// Idle reports whether no install, uninstall or local round is in progress.
func (b *Backend) Idle() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.busy == 0 && !b.runBusy
}

// WaitIdle waits until Idle, at most timeout. It reports whether it is idle.
func (b *Backend) WaitIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for !b.Idle() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
	return true
}

// Close cancels background work (sign-ins in progress).
func (b *Backend) Close() { b.bgCancel() }
