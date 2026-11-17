package ops

import (
	"os"
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
