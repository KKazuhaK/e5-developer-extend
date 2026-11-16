//go:build !linux

package sdnotify

// Notify does nothing outside Linux.
func Notify(string) error { return nil }
