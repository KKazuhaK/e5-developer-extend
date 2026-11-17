//go:build windows

package service

import "golang.org/x/sys/windows"

// IsElevated reports whether this process runs with administrator rights
// (an elevated token).
func IsElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }
