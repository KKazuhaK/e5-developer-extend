package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func TestCooldownSuppressesFlapping(t *testing.T) {
	r := newRig()
	r.cfg.Cooldown = "6h"
	fail := func() { r.as.Status, r.as.Reason = store.StatusNeedsAttention, "login required" }
	fail()
	if kinds(r.eval()) != "needs_attention" {
		t.Fatal("first episode must be announced")
	}
	r.as.Status = store.StatusOK
	if kinds(r.eval()) != "recovered" {
		t.Fatal("recovery")
	}
	// Critical: the cooldown is half, 3h. Flapping again after 1h is swallowed.
	r.now = r.now.Add(time.Hour)
	fail()
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("within the cooldown: %q", k)
	}
	// A swallowed episode is not ended with a "recovered" nobody expects.
	r.as.Status = store.StatusOK
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("recovery of a swallowed episode: %q", k)
	}
	// After half the cooldown a critical one goes out again.
	r.now = r.now.Add(3 * time.Hour)
	fail()
	if k := kinds(r.eval()); k != "needs_attention" {
		t.Fatalf("after cooldown/2: %q", k)
	}
}

func TestCooldownIsLongerForWarnings(t *testing.T) {
	r := newSpikeRig()
	r.cfg.Cooldown = "6h"
	bad := callRounds("main", "user.me", "server_error", 500, 3)
	good := callRounds("main", "user.me", "ok", 200, 3)
	r.calls["main"] = bad
	if kinds(r.eval()) != "failure_spike" {
		t.Fatal("first spike")
	}
	r.calls["main"] = good
	r.now = r.now.Add(time.Hour)
	if kinds(r.eval()) != "recovered" {
		t.Fatal("recovery")
	}
	// 4h later: past cooldown/2 but inside the warning's 6h.
	r.calls["main"] = bad
	r.now = r.now.Add(4 * time.Hour)
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("warning inside the cooldown: %q", k)
	}
	r.calls["main"] = good
	r.eval()
	r.calls["main"] = bad
	r.now = r.now.Add(3 * time.Hour)
	if k := kinds(r.eval()); k != "failure_spike" {
		t.Fatalf("warning after the cooldown: %q", k)
	}
}

func TestCooldownIsPerAccountAndOffWhenZero(t *testing.T) {
	r := newRig()
	r.cfg.Cooldown = "6h"
	other := &store.AccountState{Status: store.StatusNeedsAttention, Reason: "x"}
	r.st.Accounts["other"] = other
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "y"
	accts := []config.Account{r.acct, {Name: "other", Auth: config.AuthClientSecret}}
	evs, _ := Evaluate(Input{Now: r.now, Start: r.start, Config: r.cfg, Accounts: accts, State: r.st})
	n := 0
	for _, e := range evs {
		if e.Event == NeedsAttention {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("each account has its own cooldown: %v", kinds(evs))
	}
}

func TestQuietHoursOnlyCriticalThenDigest(t *testing.T) {
	r := newRig()
	r.cfg.QuietHours = "23:00-08:00"
	r.now = time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC)
	r.as.LastSuccess = r.now.Add(-100 * time.Hour) // stale: a warning
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "login required"
	// needs_attention is critical and goes out; stale is held.
	if k := kinds(r.eval()); k != "needs_attention" {
		t.Fatalf("at night: %q", k)
	}
	if !r.as.Notified[Stale].Held || !r.as.Notified[Stale].Pending {
		t.Fatalf("stale not held: %+v", r.as.Notified[Stale])
	}
	// Still night, across midnight: nothing new, nothing released.
	r.now = time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC)
	r.as.LastSuccess = r.now.Add(-100 * time.Hour)
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("still quiet: %q", k)
	}
	// The account recovers during the night: "recovered" (info) is held too.
	r.as.Status, r.as.LastSuccess = store.StatusOK, r.now
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("recovered at night: %q", k)
	}
	// Morning: what is still held leaves as one message.
	r.now = time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	r.as.LastSuccess = r.now.Add(-time.Hour)
	evs := r.evalNoAck()
	if len(evs) != 1 {
		t.Fatalf("morning: %v", kinds(evs))
	}
	// stale ended during the night, so only "recovered" was left to send
	if evs[0].Event != Recovered {
		t.Fatalf("got %+v", evs[0])
	}
}

func TestDigestCollectsSeveralAndSettlesEach(t *testing.T) {
	r := newRig()
	r.cfg.QuietHours = "23:00-08:00"
	r.now = time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC)
	r.acct.SecretExpires = "2026-11-20"
	r.as.LastSuccess = r.now.Add(-100 * time.Hour)
	if evs := r.eval(); len(evs) != 0 { // stale and secret_expiring, both warnings
		t.Fatalf("night: %v", kinds(evs))
	}
	r.now = time.Date(2026, 11, 18, 8, 5, 0, 0, time.UTC)
	r.as.LastSuccess = r.now.Add(-100 * time.Hour)
	evs := r.evalNoAck()
	if len(evs) != 1 || evs[0].Event != Digest || len(evs[0].Items) != 2 || evs[0].Level() != config.LevelWarning {
		t.Fatalf("morning: %+v", evs)
	}
	if !strings.Contains(evs[0].Message, "2 alerts") {
		t.Errorf("message: %q", evs[0].Message)
	}
	for _, it := range evs[0].Items {
		if it.Account != "main" || it.Message == "" {
			t.Errorf("item: %+v", it)
		}
	}
	if !MarkEvent(r.st, evs[0]) {
		t.Fatal("digest not settled")
	}
	for k, n := range r.as.Notified {
		if n.Pending || n.Held {
			t.Errorf("%s still pending: %+v", k, n)
		}
	}
	r.now = r.now.Add(2 * RetryAfter)
	if k := kinds(r.eval()); k != "" {
		t.Errorf("sent again: %q", k)
	}
}

func TestSingleHeldAlertIsSentAsItself(t *testing.T) {
	r := newRig()
	r.cfg.QuietHours = "23:00-08:00"
	r.now = time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC)
	r.as.LastSuccess = r.now.Add(-100 * time.Hour)
	r.eval()
	r.now = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	r.as.LastSuccess = r.now.Add(-100 * time.Hour)
	evs := r.eval()
	if len(evs) != 1 || evs[0].Event != Stale || len(evs[0].Items) != 0 {
		t.Fatalf("got %+v", evs)
	}
}

func TestQuietHoursSameDayWindowAndOff(t *testing.T) {
	r := newRig()
	r.cfg.QuietHours = "12:00-14:00"
	r.now = time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC)
	r.as.LastSuccess = r.now.Add(-100 * time.Hour)
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("inside: %v", kinds(evs))
	}
	r2 := newRig()
	r2.as.LastSuccess = r2.now.Add(-100 * time.Hour)
	if k := kinds(r2.eval()); k != "stale" {
		t.Fatalf("quiet hours off: %q", k)
	}
}

func TestHeldAlertDroppedWhenConditionEnds(t *testing.T) {
	r := newRig()
	r.cfg.QuietHours = "23:00-08:00"
	r.now = time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC)
	r.as.LastSuccess = r.now.Add(-100 * time.Hour)
	r.eval()
	r.as.LastSuccess = r.now
	r.eval()
	if _, ok := r.as.Notified[Stale]; ok {
		t.Error("held alert kept after its condition ended")
	}
}

func TestCooledProblemIsAnnouncedOnceTheCooldownEnds(t *testing.T) {
	r := newRig()
	r.cfg.Cooldown = "6h"
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "login required"
	if kinds(r.eval()) != "needs_attention" {
		t.Fatal("first")
	}
	r.as.Status = store.StatusOK
	r.as.LastSuccess = r.now
	if kinds(r.eval()) != "recovered" {
		t.Fatal("recovery")
	}
	// Flaps back within the cooldown and then stays broken for 72 hours.
	r.now = r.now.Add(time.Hour)
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "login required"
	total := 0
	var times []time.Time
	for h := 0; h < 72; h++ {
		for _, e := range r.eval() {
			if e.Event == NeedsAttention {
				total++
				times = append(times, e.Time)
			}
		}
		r.as.LastSuccess = r.now // keep stale out of it
		r.now = r.now.Add(time.Hour)
	}
	if total != 1 {
		t.Fatalf("needs_attention sent %d times, want exactly one after the cooldown", total)
	}
	// It went out when half the 6h cooldown (3h since the first) had passed, with the time it was detected.
	if !times[0].Equal(t0.Add(time.Hour)) {
		t.Errorf("event time %v, want the detection time", times[0])
	}
	// Its recovery is announced normally.
	r.as.Status = store.StatusOK
	if k := kinds(r.eval()); k != "recovered" {
		t.Fatalf("recovery of the released problem: %q", k)
	}
}

func TestHeldProblemThatClearsLeavesNoRecovery(t *testing.T) {
	r := newRig()
	r.cfg.QuietHours = "23:00-08:00"
	r.now = time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC)
	r.as.LastSuccess = r.now.Add(-100 * time.Hour) // stale: a warning, held
	r.eval()
	r.as.LastSuccess = r.now // clears during the night
	r.eval()
	r.now = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	r.as.LastSuccess = r.now
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("a problem nobody heard of produced %v", kinds(evs))
	}
	if _, ok := r.as.Notified[recordUnhealthy]; ok {
		t.Error("unhealthy episode kept")
	}
}

func TestRetriedAndHeldAlertsKeepTheirDetectionTime(t *testing.T) {
	r := newRig()
	r.cfg.QuietHours = "23:00-08:00"
	r.now = time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC)
	detected := r.now
	r.acct.SecretExpires = "2026-11-20"
	r.as.LastSuccess = r.now.Add(-100 * time.Hour)
	r.eval()
	r.now = time.Date(2026, 11, 18, 8, 5, 0, 0, time.UTC)
	r.as.LastSuccess = r.now.Add(-100 * time.Hour)
	evs := r.evalNoAck()
	if len(evs) != 1 || evs[0].Event != Digest {
		t.Fatalf("got %+v", evs)
	}
	for _, it := range evs[0].Items {
		if !it.Time.Equal(detected) || it.Attempt.IsZero() || it.Attempt.Equal(detected) {
			t.Errorf("item %s: time %v attempt %v", it.Event, it.Time, it.Attempt)
		}
	}
	if !MarkEvent(r.st, evs[0]) {
		t.Error("digest not settled by its attempts")
	}
	// A plain retry keeps the time too.
	r2 := newRig()
	r2.as.Status, r2.as.Reason = store.StatusNeedsAttention, "x"
	first := r2.evalNoAck()
	r2.now = r2.now.Add(2 * time.Hour)
	again := r2.evalNoAck()
	if len(again) != 1 || !again[0].Time.Equal(first[0].Time) || again[0].Attempt.Equal(first[0].Attempt) {
		t.Errorf("retry: %+v vs %+v", again, first)
	}
	if !MarkEvent(r2.st, again[0]) || r2.as.Notified[NeedsAttention].Pending {
		t.Error("retry not settled")
	}
}
