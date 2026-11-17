//go:build !windows

package ops

import (
	"os"
	"syscall"
)

// pidAlive reports whether a process of this user with the id exists. A
// process of another user (EPERM) is not ours: the data directory is per user.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
