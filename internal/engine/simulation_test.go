package engine

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// sim drives two accounts with the default schedule on a fake clock.
type sim struct {
	t  *testing.T
	f  *fixture
	ny *time.Location

	mu      sync.Mutex
	entries []store.Entry
}

func newSim(t *testing.T, start time.Time, mod func(*config.Config), opts ...func(*Options)) *sim {
	t.Helper()
	// Thousands of state saves: skip fsync so slow disks do not dominate.
	paths.SyncWrites = false
	t.Cleanup(func() { paths.SyncWrites = true })

	s := &sim{t: t, ny: nyLoc(t)}
	s.f = newFixture(t,
		delegatedAcct("alpha"),
		config.Account{Name: "beta", Auth: config.AuthClientSecret, Tenant: "example.org", ClientID: testGUID},
	)
	f := s.f
	f.clock.Set(start.In(s.ny))
	f.auths["beta"].tok.Scopes = []string{"User.Read.All", "Mail.Read", "Calendars.Read", "Contacts.Read", "Tasks.Read.All"}
	// One API is forbidden all year; it must be skipped, not pile up state.
	f.h.setStatus(func(p string) (int, map[string]string) {
		if strings.Contains(p, "contacts") {
			return 403, nil
		}
		return 200, nil
	})
	// Requests go straight to the fake handler: tens of thousands of real sockets only add noise.
	f.opts.Graph = &graph.Client{HTTP: &http.Client{Transport: handlerRT{f.h}}, Base: "http://graph.test"}
	f.opts.Tick = time.Hour
	f.opts.OnEntry = func(en store.Entry) {
		s.mu.Lock()
		s.entries = append(s.entries, en)
		s.mu.Unlock()
	}
	if mod != nil {
		mod(f.cfg)
	}
	for _, o := range opts {
		o(&f.opts)
	}
	f.start()
	return s
}

// runUntil steps to the earliest due time (ticking at least every Tick).
func (s *sim) runUntil(end time.Time, daily func(day int)) {
	f, e := s.f, s.f.e
	startDay := f.clock.Now()
	nextDay := startDay.AddDate(0, 0, 1)
	for f.clock.Now().Before(end) {
		next := f.clock.Now().Add(f.opts.Tick)
		e.mu.Lock()
		for _, as := range e.state.Accounts {
			if !as.NextRun.IsZero() && as.NextRun.After(f.clock.Now()) && as.NextRun.Before(next) {
				next = as.NextRun
			}
		}
		e.mu.Unlock()
		f.clock.Set(next)
		f.tickWait()
		if daily != nil && !f.clock.Now().Before(nextDay) {
			nextDay = nextDay.AddDate(0, 0, 1)
			daily(int(f.clock.Now().Sub(startDay).Hours() / 24))
		}
	}
}

func (s *sim) snapshot() []store.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.Entry(nil), s.entries...)
}

func isWeekday(t time.Time) bool { return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday }

// checkRounds applies the continuity rules to the round log of the given span.
func (s *sim) checkRounds(entries []store.Entry, firstDay, lastDay time.Time, minGap time.Duration) {
	t := s.t
	t.Helper()
	okDays := map[string]map[string]bool{"alpha": {}, "beta": {}}
	ends := map[string][]time.Time{}
	current := map[string]string{} // account -> round id being written
	finished := map[string]bool{}  // round ids that already ended
	for _, en := range entries {
		if en.Account == "" || en.Round == "" {
			continue
		}
		if en.Round != current[en.Account] {
			if finished[en.Round] || (current[en.Account] != "" && !finished[current[en.Account]]) {
				t.Fatalf("%s: rounds overlap or interleave at %s (round %s after %s)", en.Account, en.TS, en.Round, current[en.Account])
			}
			current[en.Account] = en.Round
		}
		if en.Event == "round_end" {
			finished[en.Round] = true
			ends[en.Account] = append(ends[en.Account], en.TS)
			if en.OK > 0 {
				okDays[en.Account][en.TS.In(s.ny).Format("2006-01-02")] = true
			}
		}
	}
	for acct, days := range okDays {
		for d := firstDay; !d.After(lastDay); d = d.AddDate(0, 0, 1) {
			if isWeekday(d) && !days[d.Format("2006-01-02")] {
				t.Errorf("%s: no successful round on work day %s", acct, d.Format("2006-01-02 Mon"))
			}
		}
		ts := ends[acct]
		sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
		for i := 1; i < len(ts); i++ {
			if gap := ts[i].Sub(ts[i-1]); gap < minGap {
				t.Errorf("%s: rounds only %s apart at %s", acct, gap, ts[i])
				break
			}
		}
	}
}

// TestOneYearSimulation runs a whole simulated year.
func TestOneYearSimulation(t *testing.T) {
	ny := nyLoc(t)
	s := newSim(t, time.Date(2026, 1, 1, 0, 0, 0, 0, ny), nil)
	f := s.f
	start := f.clock.Now()
	end := start.AddDate(0, 0, 365)

	maxFiles := 0
	var stateSizeAt30, stateSizeEnd int64
	began := time.Now()
	s.runUntil(end, func(day int) {
		files, _ := filepath.Glob(filepath.Join(f.dir, "logs", "calls-*.jsonl"))
		maxFiles = max(maxFiles, len(files))
		if fi, err := os.Stat(filepath.Join(f.dir, "state.json")); err == nil {
			if day == 30 {
				stateSizeAt30 = fi.Size()
			}
			stateSizeEnd = fi.Size()
		}
	})
	elapsed := time.Since(began)
	entries := s.snapshot()
	t.Logf("simulated 365 days in %s, %d log entries", elapsed.Round(time.Millisecond), len(entries))

	type dayKey struct{ acct, day string }
	roundsPerDay := map[dayKey]int{}
	firstOfDay := map[dayKey]time.Time{}
	callsByHour := map[int]int{}
	totalCalls := 0
	for _, en := range entries {
		ts := en.TS.In(ny)
		k := dayKey{en.Account, ts.Format("2006-01-02")}
		if en.Event == "round_end" {
			roundsPerDay[k]++
			if first, ok := firstOfDay[k]; !ok || ts.Before(first) {
				firstOfDay[k] = ts
			}
		}
		if en.API == "" {
			continue
		}
		totalCalls++
		callsByHour[ts.Hour()]++
		if !isWeekday(ts) {
			t.Fatalf("call on a weekend: %s", ts)
		}
		if ts.Hour() < 8 || ts.Hour() >= 22 {
			t.Fatalf("call outside work hours: %s", ts)
		}
	}
	if totalCalls < 20000 {
		t.Fatalf("only %d calls in a year", totalCalls)
	}
	if len(roundsPerDay) < 2*250 {
		t.Errorf("rounds happened on only %d account-days", len(roundsPerDay))
	}
	for k, n := range roundsPerDay {
		if n < 3 || n > 25 {
			t.Errorf("%s on %s ran %d rounds", k.acct, k.day, n)
		}
	}
	for h, n := range callsByHour {
		if share := float64(n) / float64(totalCalls); share > 0.15 {
			t.Errorf("%.0f%% of all calls fall in hour %d", share*100, h)
		}
	}
	if len(callsByHour) != 14 {
		t.Errorf("calls cover %d distinct hours, want all 14", len(callsByHour))
	}
	seconds := map[int]int{}
	for _, ts := range firstOfDay {
		seconds[ts.Hour()*3600+ts.Minute()*60+ts.Second()]++
	}
	for sec, n := range seconds {
		if n > len(firstOfDay)/10 {
			t.Errorf("%d of %d first rounds start at second-of-day %d", n, len(firstOfDay), sec)
		}
	}

	s.checkRounds(entries, start, end.AddDate(0, 0, -1), 40*time.Minute)

	for _, name := range []string{"alpha", "beta"} {
		as := f.acct(name)
		if as.Status != store.StatusOK {
			t.Errorf("%s ended as %s: %s", name, as.Status, as.Reason)
		}
		if as.LastSuccess.Before(end.AddDate(0, 0, -4)) {
			t.Errorf("%s: last success %s", name, as.LastSuccess)
		}
		if len(as.APISkipUntil) > 1 || len(as.APIForbiddenCount) > 1 {
			t.Errorf("%s: skip maps grew: %v %v", name, as.APISkipUntil, as.APIForbiddenCount)
		}
	}
	if maxFiles > 31 {
		t.Errorf("log directory reached %d files", maxFiles)
	}
	if stateSizeAt30 == 0 || stateSizeEnd > stateSizeAt30+256 {
		t.Errorf("state.json grew from %d to %d bytes", stateSizeAt30, stateSizeEnd)
	}
	if elapsed > 60*time.Second {
		t.Errorf("simulation took %s", elapsed)
	}
}

// TestSimulationPeriodEnd checks that the last day is not bursty and that
// nothing runs after the period.
func TestSimulationPeriodEnd(t *testing.T) {
	ny := nyLoc(t)
	s := newSim(t, time.Date(2026, 1, 1, 0, 0, 0, 0, ny), func(c *config.Config) {
		pe := toml.LocalDate{Year: 2026, Month: 3, Day: 31} // a Tuesday
		c.Schedule.PeriodEnd = &pe
	})
	s.runUntil(time.Date(2026, 4, 20, 0, 0, 0, 0, ny), nil)
	entries := s.snapshot()

	cutoff := time.Date(2026, 4, 1, 0, 0, 0, 0, ny)
	lastDay := map[string]int{}
	for _, en := range entries {
		if en.TS.After(cutoff) || en.TS.Equal(cutoff) {
			t.Fatalf("entry after the period: %+v", en)
		}
		if en.Event == "round_end" && en.TS.In(ny).Format("2006-01-02") == "2026-03-31" {
			lastDay[en.Account]++
		}
	}
	for _, name := range []string{"alpha", "beta"} {
		// A 14 hour window at 40 minutes minimum spacing allows at most 22 rounds.
		if n := lastDay[name]; n < 1 || n > 22 {
			t.Errorf("%s: %d rounds on the last day", name, n)
		}
		if st := s.f.acct(name).Status; st != store.StatusPeriodEnded {
			t.Errorf("%s: status %s", name, st)
		}
	}
	s.checkRounds(entries, time.Date(2026, 1, 1, 0, 0, 0, 0, ny), time.Date(2026, 3, 31, 0, 0, 0, 0, ny), 40*time.Minute)
}

// TestSimulationBackwardClockJump: the clock runs a month fast for a day and
// is then corrected; scheduling must recover within one interval_max.
func TestSimulationBackwardClockJump(t *testing.T) {
	ny := nyLoc(t)
	s := newSim(t, time.Date(2026, 5, 1, 0, 0, 0, 0, ny), nil)
	f := s.f
	s.runUntil(time.Date(2026, 5, 5, 10, 0, 0, 0, ny), nil)

	// The clock jumps a month ahead and runs there for a day.
	f.clock.Advance(30 * 24 * time.Hour)
	s.runUntil(f.clock.Now().Add(24*time.Hour), nil)

	// NTP puts it back: Wednesday 11:00.
	correct := time.Date(2026, 5, 6, 11, 0, 0, 0, ny)
	mark := len(s.snapshot())
	f.clock.Set(correct)
	f.tickWait() // the next engine tick (30 s in production)
	s.runUntil(correct.Add(12*time.Hour), nil)

	if !f.hasEvent("clock_change") || !f.hasEvent("clock_fix") {
		t.Error("clock correction not logged")
	}
	var first time.Time
	for _, en := range s.snapshot()[mark:] {
		if en.API != "" {
			first = en.TS
			break
		}
	}
	if first.IsZero() {
		t.Fatal("no call after the correction")
	}
	if d := first.Sub(correct); d < 0 || d > 2*time.Hour {
		t.Errorf("first call %s after the correction, want within interval_max (2h)", d)
	}
	for _, name := range []string{"alpha", "beta"} {
		if as := f.acct(name); as.Status != store.StatusOK || as.NextRun.Sub(f.clock.Now()) > 4*24*time.Hour {
			t.Errorf("%s: %+v", name, as)
		}
	}
}
