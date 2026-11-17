package engine

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// A round that asks for more APIs than the account can use takes all of them.
func TestRoundTakesAllAvailableWhenAskingForMore(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.cfg.Schedule.APIsPerRoundMin, f.cfg.Schedule.APIsPerRoundMax = 25, 30
	f.start()
	f.round("main")

	apis, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := len(catalog.Available(apis, catalog.Delegated, delegatedScopes, nil))
	if want == 0 || want >= 25 {
		t.Fatalf("test needs fewer than 25 usable APIs, have %d", want)
	}
	if got := f.h.total(); got != want {
		t.Errorf("round made %d calls, want all %d usable APIs", got, want)
	}
	if got := f.acct("main").Totals.Calls; got != want {
		t.Errorf("totals.calls = %d, want %d", got, want)
	}
}

func TestDefaultScheduleRunsAllDay(t *testing.T) {
	c := config.Default().Schedule
	if c.WorkStart != c.WorkEnd {
		t.Fatalf("default window %s-%s is not all day", c.WorkStart, c.WorkEnd)
	}
	s, err := newSchedule(c)
	if err != nil {
		t.Fatal(err)
	}
	ny := nyLoc(t)
	// Sunday 03:00 and Tuesday 23:59 are inside the window.
	for _, at := range []time.Time{time.Date(2026, 11, 22, 3, 0, 0, 0, ny), time.Date(2026, 11, 17, 23, 59, 0, 0, ny)} {
		if !s.within(at) {
			t.Errorf("%s is outside the default window", at)
		}
	}
	got, _ := s.next(time.Date(2026, 11, 21, 23, 50, 0, 0, ny), lo)
	if got.Sub(time.Date(2026, 11, 21, 23, 50, 0, 0, ny)) > 2*time.Hour {
		t.Errorf("next round after Saturday night is %s", got)
	}
}

func TestAPIStatsAreCounted(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.cfg.Schedule.APIsPerRoundMin, f.cfg.Schedule.APIsPerRoundMax = 30, 30
	f.h.setStatus(func(p string) (int, map[string]string) {
		switch p {
		case "/me/contacts":
			return http.StatusInternalServerError, nil
		}
		return 200, nil
	})
	f.start()
	f.round("main")
	f.round("main")

	st := f.acct("main").APIStats
	if len(st) == 0 {
		t.Fatal("no statistics recorded")
	}
	var ok, failed int
	for id, s := range st {
		ok += s.OK
		failed += s.Failed
		if s.LastAt.IsZero() || s.LastClass == "" {
			t.Errorf("%s: last result missing: %+v", id, s)
		}
	}
	tot := f.acct("main").Totals
	if ok != tot.OK || failed != tot.Failed {
		t.Errorf("per-API %d/%d, totals %d/%d", ok, failed, tot.OK, tot.Failed)
	}
	c := st["contacts.list"]
	if c == nil || c.Failed != 2 || c.OK != 0 || c.LastStatus != 500 {
		t.Errorf("contacts.list = %+v", c)
	}

	// An API that left the catalog is dropped on the next round, and the
	// counters survive a restart.
	f.e.updateAccount(context.Background(), "main", func(as *store.AccountState) { as.APIStats["gone.api"] = &store.APIStat{OK: 1} })
	f.round("main")
	if _, ok := f.acct("main").APIStats["gone.api"]; ok {
		t.Error("statistics of an API outside the catalog were kept")
	}
	before := f.acct("main").APIStats["contacts.list"].Failed
	f2 := *f
	f2.start()
	if got := f2.acct("main").APIStats["contacts.list"]; got == nil || got.Failed != before {
		t.Errorf("counters lost over a restart: %+v, want %d failures", got, before)
	}
}

// A throttled call is not a failure: the per-API counters agree with Totals.
func TestAPIStatsDoNotCountThrottling(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.cfg.Schedule.APIsPerRoundMin, f.cfg.Schedule.APIsPerRoundMax = 30, 30
	f.auths["main"].tok.Scopes = []string{"Contacts.Read", "Tasks.Read"} // contacts.list, todo.lists, planner.tasks
	f.h.setStatus(func(p string) (int, map[string]string) {
		if strings.Contains(p, "contacts") {
			return http.StatusTooManyRequests, map[string]string{"Retry-After": "1"}
		}
		return 200, nil
	})
	f.start()
	f.round("main")
	f.round("main")

	as := f.acct("main")
	var ok, failed int
	for _, s := range as.APIStats {
		ok += s.OK
		failed += s.Failed
	}
	if ok != as.Totals.OK || failed != as.Totals.Failed || failed != 0 {
		t.Errorf("per-API %d/%d, totals %d/%d", ok, failed, as.Totals.OK, as.Totals.Failed)
	}
	if c := as.APIStats["contacts.list"]; c != nil && (c.Failed != 0 || c.LastClass != "throttled") {
		t.Errorf("contacts.list = %+v", c)
	}
}
