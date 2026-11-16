//go:build windows

package service

import (
	"context"
	"testing"
)

func TestExecRunnerHidesTheConsoleWindow(t *testing.T) {
	cmd := buildCmd(context.Background(), "schtasks.exe", "/Query")
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("SysProcAttr = %+v; every command must run without a console window", cmd.SysProcAttr)
	}
}
