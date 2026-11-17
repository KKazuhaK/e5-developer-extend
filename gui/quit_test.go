package main

import (
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
