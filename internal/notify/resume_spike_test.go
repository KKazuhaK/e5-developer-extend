package notify

import (
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func TestSpikeAnnouncedBeforeAPauseEndsOnceTheAccountSucceeds(t *testing.T) {
	r := newSpikeRig()
	r.calls["main"] = callRounds("main", "user.me", "server_error", 500, 4)
	if kinds(r.eval()) != "failure_spike" {
		t.Fatal("no spike")
	}
	// The account was paused and resumed; the log still holds only old failures.
	resumed := r.now.Add(time.Hour)
	r.as.ResumedAt = resumed
	r.now = resumed.Add(time.Minute)
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("ended before any success: %v", kinds(evs))
	}
	// One good round later (less than a full window) the spike is over.
	r.as.LastSuccess = r.now.Add(time.Minute)
	r.now = r.now.Add(5 * time.Minute)
	r.calls["main"] = append(r.calls["main"], store.Entry{TS: r.now, Account: "main", Round: "new", API: "user.me", Class: "ok", Status: 200})
	evs := r.eval()
	if kinds(evs) != "recovered" || evs[0].Origin != FailureSpike {
		t.Fatalf("got %v", kinds(evs))
	}
}
