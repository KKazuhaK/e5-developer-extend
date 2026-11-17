package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

func (r *rig) evalUpdate(offer *UpdateOffer, ack bool) []Event {
	evs, _ := Evaluate(Input{Now: r.now, Start: r.start, Config: r.cfg, Accounts: []config.Account{r.acct}, State: r.st, Update: offer})
	if ack {
		for _, e := range evs {
			MarkEvent(r.st, e)
		}
	}
	return evs
}

func TestUpdateAvailableOncePerVersion(t *testing.T) {
	r := newRig()
	offer := &UpdateOffer{Version: "0.3.0", Current: "0.2.0", URL: "https://example.test/v0.3.0"}
	evs := r.evalUpdate(offer, true)
	if kinds(evs) != "update_available" {
		t.Fatalf("first: %q", kinds(evs))
	}
	e := evs[0]
	if e.Level() != config.LevelInfo || e.Account != "" || !strings.Contains(e.Message, "0.3.0") || !strings.Contains(e.Message, "0.2.0") || !strings.Contains(e.Message, offer.URL) {
		t.Fatalf("event: %+v (level %s)", e, e.Level())
	}
	for i := 0; i < 3; i++ {
		r.now = r.now.Add(2 * time.Hour)
		if k := kinds(r.evalUpdate(offer, true)); k != "" {
			t.Fatalf("announced again: %q", k)
		}
	}
	// "Nothing newer" (the program was upgraded) forgets it; the next version is news again.
	if k := kinds(r.evalUpdate(&UpdateOffer{}, true)); k != "" {
		t.Fatalf("nothing newer: %q", k)
	}
	if _, ok := r.st.Notified[UpdateAvailable]; ok {
		t.Fatal("the announcement was not forgotten")
	}
	if k := kinds(r.evalUpdate(&UpdateOffer{Version: "0.4.0", Current: "0.3.0"}, true)); k != "update_available" {
		t.Fatalf("next version: %q", k)
	}
}

func TestUpdateAvailableUnknownLeavesAnnouncementAlone(t *testing.T) {
	r := newRig()
	r.evalUpdate(&UpdateOffer{Version: "0.3.0", Current: "0.2.0"}, true)
	// The check is off, or has not run yet: nothing is forgotten, so switching it on
	// again does not announce the same version a second time.
	if k := kinds(r.evalUpdate(nil, true)); k != "" {
		t.Fatalf("%q", k)
	}
	if _, ok := r.st.Notified[UpdateAvailable]; !ok {
		t.Fatal("the announcement was dropped while the check was unknown")
	}
	if k := kinds(r.evalUpdate(&UpdateOffer{Version: "0.3.0", Current: "0.2.0"}, true)); k != "" {
		t.Fatalf("announced again: %q", k)
	}
}

func TestUpdateAvailableRetriedUntilDelivered(t *testing.T) {
	r := newRig()
	offer := &UpdateOffer{Version: "0.3.0", Current: "0.2.0"}
	if k := kinds(r.evalUpdate(offer, false)); k != "update_available" {
		t.Fatalf("%q", k)
	}
	r.now = r.now.Add(30 * time.Minute)
	if k := kinds(r.evalUpdate(offer, false)); k != "" {
		t.Fatalf("retried too soon: %q", k)
	}
	r.now = r.now.Add(RetryAfter)
	if k := kinds(r.evalUpdate(offer, true)); k != "update_available" {
		t.Fatalf("not retried: %q", k)
	}
	r.now = r.now.Add(RetryAfter * 3)
	if k := kinds(r.evalUpdate(offer, true)); k != "" {
		t.Fatalf("repeated after delivery: %q", k)
	}
}

func TestUpdateAvailableHeldDuringQuietHours(t *testing.T) {
	r := newRig()
	r.cfg.QuietHours = "09:00-11:00" // t0 is 10:00 UTC; the zone is the evaluation time's
	offer := &UpdateOffer{Version: "0.3.0", Current: "0.2.0"}
	if k := kinds(r.evalUpdate(offer, true)); k != "" {
		t.Fatalf("an info alert went out during the quiet hours: %q", k)
	}
	r.now = r.now.Add(2 * time.Hour)
	if k := kinds(r.evalUpdate(offer, true)); k != "update_available" {
		t.Fatalf("held alert not released: %q", k)
	}
}

func TestUpdateAvailableFilteredByEvents(t *testing.T) {
	r := newRig()
	r.cfg.Events = []string{"recovered"}
	offer := &UpdateOffer{Version: "0.3.0", Current: "0.2.0"}
	if k := kinds(r.evalUpdate(offer, true)); k != "" {
		t.Fatalf("filtered event sent: %q", k)
	}
	r.cfg.Events = nil
	if k := kinds(r.evalUpdate(offer, true)); k != "" {
		t.Fatalf("a version that was already seen while filtered out is announced late: %q", k)
	}
}

func TestUpdateAvailableRenders(t *testing.T) {
	ev := SampleEvent(UpdateAvailable, time.Now())
	for _, lang := range []string{"zh", "en"} {
		d := NewData(ev, lang, "host", time.UTC)
		if d.EventLabel == UpdateAvailable || d.Hint == "" {
			t.Errorf("%s: label %q hint %q", lang, d.EventLabel, d.Hint)
		}
		r, err := Render("telegram", lang, "", d)
		if err != nil || !strings.Contains(r.Body, "0.3.0") {
			t.Errorf("%s: %q %v", lang, r.Body, err)
		}
	}
}
