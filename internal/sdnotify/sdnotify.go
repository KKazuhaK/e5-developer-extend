// Package sdnotify sends readiness and watchdog messages to systemd
// (sd_notify). Outside Linux, or when NOTIFY_SOCKET is not set, every call
// does nothing.
package sdnotify

// Messages understood by systemd.
const (
	MsgReady    = "READY=1"
	MsgWatchdog = "WATCHDOG=1"
	MsgStopping = "STOPPING=1"
)

// Ready tells systemd the service finished starting.
func Ready() error { return Notify(MsgReady) }

// Watchdog resets the service's watchdog timer.
func Watchdog() error { return Notify(MsgWatchdog) }

// Stopping tells systemd the service has begun shutting down.
func Stopping() error { return Notify(MsgStopping) }
