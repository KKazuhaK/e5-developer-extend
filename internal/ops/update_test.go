package ops

import (
	"strings"
	"testing"
)

func TestOnlyOneUpgradeRunsAtATime(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockUpdate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockUpdate(dir); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("a second upgrade got the lock: %v", err)
	}
	unlock()
	unlock2, err := lockUpdate(dir)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	unlock2()
}
