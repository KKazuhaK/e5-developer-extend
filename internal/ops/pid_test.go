package ops

import (
	"os"
	"runtime"
	"testing"
)

func TestPidAlive(t *testing.T) {
	if !PidAlive(os.Getpid()) {
		t.Error("own process reported dead")
	}
	if PidAlive(0) || PidAlive(-1) {
		t.Error("invalid pid reported alive")
	}
}

func TestPidMatchesStartTime(t *testing.T) {
	pid := os.Getpid()
	start := ProcStart(pid)
	if runtime.GOOS == "linux" && start == "" {
		t.Fatal("no start time of the own process")
	}
	if !PidMatches(pid, start) {
		t.Error("own process does not match its own start time")
	}
	if !PidMatches(pid, "") {
		t.Error("an unknown start time must fall back to liveness")
	}
	if start != "" && PidMatches(pid, start+"1") {
		t.Error("a reused pid (other start time) matched")
	}
	if PidMatches(0, start) {
		t.Error("pid 0 matched")
	}
}
