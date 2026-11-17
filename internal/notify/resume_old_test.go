package notify

import (
	"testing"
	"time"
)

// A resume that happened before a spike was announced says nothing about that spike.
func TestOldResumeDoesNotEndALaterSpike(t *testing.T) {
	r := newSpikeRig()
	r.as.ResumedAt = r.now.Add(-5 * time.Hour)
	r.calls["main"] = callRounds("main", "user.me", "server_error", 500, 4)
	for i := range r.calls["main"] {
		r.calls["main"][i].TS = r.now.Add(-time.Hour)
	}
	if kinds(r.eval()) != "failure_spike" {
		t.Fatal("no spike")
	}
	r.now = r.now.Add(time.Hour)
	r.as.LastSuccess = r.now
	r.calls["main"] = r.calls["main"][:2] // too few rounds to judge
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("spike ended by an older resume: %v", kinds(evs))
	}
}
