//go:build !windows

package service

// IsElevated is only meaningful on Windows; elsewhere it reports false.
func IsElevated() bool { return false }
