//go:build !windows

package update

import (
	"os/exec"
	"syscall"
)

// StartDetached starts a program that keeps running after this process ends.
func StartDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
