package ops

import (
	"errors"
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/service"
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

type fakeManager struct {
	service.Manager
	st        service.Status
	statusErr error
	restarts  int
}

func (f *fakeManager) Status() (service.Status, error) { return f.st, f.statusErr }
func (f *fakeManager) Restart() (service.Result, error) {
	f.restarts++
	return service.Result{}, nil
}

func TestRestartManaged(t *testing.T) {
	cases := []struct {
		name     string
		m        fakeManager
		state    string
		restarts int
	}{
		{"running", fakeManager{st: service.Status{Installed: true, Running: service.Yes}}, "restarted", 1},
		{"stopped", fakeManager{st: service.Status{Installed: true, Running: service.No}}, "not_running", 0},
		{"status error", fakeManager{statusErr: errors.New("boom")}, "failed", 0},
	}
	for _, c := range cases {
		m := c.m
		state, note := restartManaged(&m)
		if state != c.state || m.restarts != c.restarts {
			t.Errorf("%s: state %q restarts %d", c.name, state, m.restarts)
		}
		if c.state == "failed" && !strings.Contains(note, "restart it yourself") {
			t.Errorf("%s: note %q", c.name, note)
		}
	}
}
