package engine

import (
	"fmt"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

// randFn returns a random duration in [lo, hi].
type randFn func(lo, hi time.Duration) time.Duration

const maxJitter = 15 * time.Minute

type civilDate struct {
	y int
	m time.Month
	d int
}

func (c civilDate) at(loc *time.Location, h, min int) time.Time {
	return time.Date(c.y, c.m, c.d, h, min, 0, 0, loc)
}

// schedule is the parsed form of config.Schedule.
type schedule struct {
	min, max   time.Duration
	days       map[time.Weekday]bool // nil: every day
	startH     int
	startM     int
	endH       int
	endM       int
	allDay     bool
	periodFrom *civilDate
	periodTo   *civilDate
}

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

func newSchedule(c config.Schedule) (*schedule, error) {
	s := &schedule{}
	var err error
	if s.min, s.max, err = c.Intervals(); err != nil {
		return nil, err
	}
	if len(c.WorkDays) > 0 {
		s.days = map[time.Weekday]bool{}
		for _, d := range c.WorkDays {
			wd, ok := weekdayNames[d]
			if !ok {
				return nil, fmt.Errorf("invalid work day %q", d)
			}
			s.days[wd] = true
		}
	}
	st, err := time.Parse("15:04", c.WorkStart)
	if err != nil {
		return nil, err
	}
	en, err := time.Parse("15:04", c.WorkEnd)
	if err != nil {
		return nil, err
	}
	s.startH, s.startM, s.endH, s.endM = st.Hour(), st.Minute(), en.Hour(), en.Minute()
	s.allDay = st.Equal(en)
	if p := c.PeriodStart; p != nil {
		s.periodFrom = &civilDate{p.Year, time.Month(p.Month), p.Day}
	}
	if p := c.PeriodEnd; p != nil {
		s.periodTo = &civilDate{p.Year, time.Month(p.Month), p.Day}
	}
	return s, nil
}

func (s *schedule) workday(d time.Weekday) bool { return s.days == nil || s.days[d] }

// ended reports whether t is after the last day of the run period.
func (s *schedule) ended(t time.Time) bool {
	if s.periodTo == nil {
		return false
	}
	return !t.Before(s.periodTo.at(t.Location(), 0, 0).AddDate(0, 0, 1))
}

// next returns the time of the next round after now.
func (s *schedule) next(now time.Time, rnd randFn) (time.Time, bool) {
	return s.place(now.Add(rnd(s.min, s.max)), rnd)
}

// place moves t into the run period, a work day and the work window. Anything
// that had to be moved gets a random offset so rounds do not start at the same
// second every day. It reports true when t is past the end of the period.
func (s *schedule) place(t time.Time, rnd randFn) (time.Time, bool) {
	loc := t.Location()
	pushed := false
	if s.periodFrom != nil {
		if start := s.periodFrom.at(loc, 0, 0); t.Before(start) {
			t, pushed = start, true
		}
	}
	// Calendar arithmetic with time.Date keeps this right across DST changes.
	for range 20 {
		if s.ended(t) {
			return t, true
		}
		y, m, d := t.Date()
		nextDay := time.Date(y, m, d+1, 0, 0, 0, 0, loc)
		if !s.workday(t.Weekday()) {
			t, pushed = nextDay, true
			continue
		}
		if s.allDay {
			break
		}
		ws := time.Date(y, m, d, s.startH, s.startM, 0, 0, loc)
		we := time.Date(y, m, d, s.endH, s.endM, 0, 0, loc)
		if t.Before(ws) {
			t, pushed = ws, true
			continue
		}
		if !t.Before(we) {
			t, pushed = nextDay, true
			continue
		}
		break
	}
	if pushed {
		y, m, d := t.Date()
		end := time.Date(y, m, d+1, 0, 0, 0, 0, loc)
		if !s.allDay {
			end = time.Date(y, m, d, s.endH, s.endM, 0, 0, loc)
		}
		room := min(maxJitter, end.Sub(t)/2)
		t = t.Add(rnd(0, room))
	}
	return t, s.ended(t)
}

// within reports whether t is on a work day inside the work window.
func (s *schedule) within(t time.Time) bool {
	if !s.workday(t.Weekday()) {
		return false
	}
	if s.allDay {
		return true
	}
	y, m, d := t.Date()
	ws := time.Date(y, m, d, s.startH, s.startM, 0, 0, t.Location())
	we := time.Date(y, m, d, s.endH, s.endM, 0, 0, t.Location())
	return !t.Before(ws) && t.Before(we)
}
