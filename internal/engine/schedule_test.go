package engine

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/KKazuhaK/e5-renew/internal/config"
)

func lo(l, h time.Duration) time.Duration { return l }
func hi(l, h time.Duration) time.Duration { return h }

func mustSched(t *testing.T, mod func(*config.Schedule)) *schedule {
	t.Helper()
	c := config.Default().Schedule
	if mod != nil {
		mod(&c)
	}
	s, err := newSchedule(c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNextRun(t *testing.T) {
	ny := nyLoc(t)
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, ny) }
	allDays := func(c *config.Schedule) { c.WorkDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"} }

	tests := []struct {
		name  string
		mod   func(*config.Schedule)
		now   time.Time
		want  time.Time
		ended bool
	}{
		{"inside window", nil, at(2026, 11, 17, 10, 0), at(2026, 11, 17, 10, 40), false},
		{"after work_end goes to next morning", nil, at(2026, 11, 17, 21, 50), at(2026, 11, 18, 8, 0), false},
		{"before work_start", nil, at(2026, 11, 17, 5, 0), at(2026, 11, 17, 8, 0), false},
		{"friday evening to monday", nil, at(2026, 11, 20, 21, 50), at(2026, 11, 23, 8, 0), false},
		{"saturday to monday", nil, at(2026, 11, 21, 12, 0), at(2026, 11, 23, 8, 0), false},
		{"all day", func(c *config.Schedule) { c.WorkStart, c.WorkEnd = "00:00", "00:00" }, at(2026, 11, 17, 3, 0), at(2026, 11, 17, 3, 40), false},
		{"all day skips the weekend", func(c *config.Schedule) { c.WorkStart, c.WorkEnd = "08:00", "08:00" }, at(2026, 11, 20, 23, 30), at(2026, 11, 23, 0, 0), false},
		{"period start in the future", func(c *config.Schedule) {
			d := toml.LocalDate{Year: 2026, Month: 12, Day: 1}
			c.PeriodStart = &d
		}, at(2026, 11, 17, 10, 0), at(2026, 12, 1, 8, 0), false},
		{"inside period end day", func(c *config.Schedule) {
			d := toml.LocalDate{Year: 2026, Month: 11, Day: 17}
			c.PeriodEnd = &d
		}, at(2026, 11, 17, 10, 0), at(2026, 11, 17, 10, 40), false},
		{"past period end", func(c *config.Schedule) {
			d := toml.LocalDate{Year: 2026, Month: 11, Day: 17}
			c.PeriodEnd = &d
		}, at(2026, 11, 17, 21, 50), at(2026, 11, 18, 8, 0), true},
		// US clocks go forward on 2027-03-14 (02:00 -> 03:00) and back on 2026-11-01.
		{"spring forward day, weekend work", allDays, at(2027, 3, 13, 21, 50), at(2027, 3, 14, 8, 0), false},
		{"across spring forward, workdays only", nil, at(2027, 3, 12, 21, 50), at(2027, 3, 15, 8, 0), false},
		{"fall back day", allDays, at(2026, 10, 31, 22, 30), at(2026, 11, 1, 8, 0), false},
		{"all day on the 25-hour day", func(c *config.Schedule) {
			allDays(c)
			c.WorkStart, c.WorkEnd = "00:00", "00:00"
		}, at(2026, 10, 31, 23, 50), at(2026, 11, 1, 0, 30), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := mustSched(t, tc.mod)
			got, ended := s.next(tc.now, lo)
			if ended != tc.ended {
				t.Fatalf("ended = %v", ended)
			}
			if ended {
				return
			}
			if !got.Equal(tc.want) {
				t.Errorf("got %s want %s", got, tc.want)
			}
			// Wall-clock fields must match too (offset changes across DST).
			if got.Hour() != tc.want.Hour() || got.Day() != tc.want.Day() {
				t.Errorf("wall time %s want %s", got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

func TestNextRunDSTOffsets(t *testing.T) {
	ny := nyLoc(t)
	s := mustSched(t, func(c *config.Schedule) {
		c.WorkDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	})
	got, _ := s.next(time.Date(2027, 3, 13, 21, 50, 0, 0, ny), lo)
	if _, off := got.Zone(); off != -4*3600 {
		t.Errorf("after spring forward the offset should be -4h, got %d", off)
	}
	got, _ = s.next(time.Date(2026, 10, 31, 22, 30, 0, 0, ny), lo)
	if _, off := got.Zone(); off != -5*3600 {
		t.Errorf("after fall back the offset should be -5h, got %d", off)
	}
}

func TestNextRunJitterStaysInWindow(t *testing.T) {
	ny := nyLoc(t)
	s := mustSched(t, nil)
	if got, _ := s.next(time.Date(2026, 11, 17, 21, 50, 0, 0, ny), hi); got.Sub(time.Date(2026, 11, 18, 8, 0, 0, 0, ny)) > 15*time.Minute {
		t.Errorf("jitter too large: %s", got)
	}

	rng := rand.New(rand.NewPCG(1, 2))
	rnd := func(l, h time.Duration) time.Duration {
		if h <= l {
			return l
		}
		return l + time.Duration(rng.Int64N(int64(h-l)+1))
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, ny)
	for i := 0; i < 5000; i++ {
		now = now.Add(time.Duration(rng.Int64N(int64(30 * time.Hour))))
		got, ended := s.next(now, rnd)
		if ended || !got.After(now) || !s.within(got) {
			t.Fatalf("now %s -> %s (within=%v ended=%v)", now, got, s.within(got), ended)
		}
	}
}

func TestPlaceZeroWorkDaysMeansEveryDay(t *testing.T) {
	s := mustSched(t, func(c *config.Schedule) { c.WorkDays = nil })
	ny := nyLoc(t)
	got, _ := s.next(time.Date(2026, 11, 21, 10, 0, 0, 0, ny), lo)
	if got.Weekday() != time.Saturday {
		t.Errorf("got %s", got)
	}
}
