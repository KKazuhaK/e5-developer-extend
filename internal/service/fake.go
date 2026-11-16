package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// FakeRunner records commands instead of running them; tests use it so the
// real systemctl, launchctl, schtasks and loginctl are never started.
type FakeRunner struct {
	mu    sync.Mutex
	Calls []string
	// Replies maps a command line (or its prefix) to the Output to return;
	// a non-zero ExitCode makes the call fail.
	Replies map[string]Output
	// FailOn makes any command line starting with one of these fail.
	FailOn []string
	// FailOnce makes the first command starting with each prefix fail, once.
	FailOnce []string
}

// Run implements Runner.
func (f *FakeRunner) Run(_ context.Context, name string, args ...string) (Output, error) {
	line := commandLine(name, args)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, line)
	for _, p := range f.FailOn {
		if strings.HasPrefix(line, p) {
			return Output{ExitCode: 1, Stderr: "fake failure"}, fmt.Errorf("%s: exit status 1", line)
		}
	}
	for i, p := range f.FailOnce {
		if strings.HasPrefix(line, p) {
			f.FailOnce = append(f.FailOnce[:i:i], f.FailOnce[i+1:]...)
			return Output{ExitCode: 1, Stderr: "fake failure"}, fmt.Errorf("%s: exit status 1", line)
		}
	}
	// Longest matching prefix wins.
	best := ""
	for k := range f.Replies {
		if strings.HasPrefix(line, k) && len(k) > len(best) {
			best = k
		}
	}
	if best != "" {
		out := f.Replies[best]
		if out.ExitCode != 0 {
			return out, fmt.Errorf("%s: exit status %d", line, out.ExitCode)
		}
		return out, nil
	}
	return Output{}, nil
}

// Commands returns a copy of the recorded command lines.
func (f *FakeRunner) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Calls...)
}
