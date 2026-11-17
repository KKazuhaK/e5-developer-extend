package main

import (
	"os"
	"testing"
	"time"
)

func TestShouldQuit(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	before, after := start.Add(-time.Minute), start.Add(time.Minute)
	stamp := func(x time.Time) []byte { return []byte(x.Format(time.RFC3339Nano) + "\n") }
	tests := []struct {
		name    string
		exists  bool
		content []byte
		mtime   time.Time
		want    bool
	}{
		{"no file", false, nil, time.Time{}, false},
		{"stale file", true, stamp(before), after, false},
		{"newer file", true, stamp(after), before, true},
		{"garbage, old mtime", true, []byte("???"), before, false},
		{"garbage, new mtime", true, []byte("???"), after, true},
		{"empty, new mtime", true, nil, after, true},
	}
	for _, tt := range tests {
		if got := shouldQuit(start, tt.content, tt.mtime, tt.exists); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestQuitRequestedReadsTheQuitFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("E5EXTEND_DATA_DIR", dir)
	start := time.Now()
	if quitRequested(start) {
		t.Fatal("a quit request without a file")
	}
	f, err := quitFile()
	if err != nil {
		t.Fatal(err)
	}
	// Left from an earlier run (before this process started): ignored.
	if err := os.WriteFile(f, []byte(start.Add(-time.Hour).Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if quitRequested(start) {
		t.Fatal("a request from before the start was obeyed")
	}
	// A restart after an upgrade writes the time of the request: the tray quits.
	if err := os.WriteFile(f, []byte(start.Add(time.Second).Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !quitRequested(start) {
		t.Fatal("a request after the start was ignored")
	}
}
