package config

import "testing"

func TestDefaultScheduleIsAllDayEveryDay(t *testing.T) {
	s := Default().Schedule
	if s.WorkStart != s.WorkEnd {
		t.Errorf("work window %s-%s: start and end must be equal (all day)", s.WorkStart, s.WorkEnd)
	}
	if len(s.WorkDays) != 7 {
		t.Errorf("work days = %v", s.WorkDays)
	}
	if err := Default().Validate(); err != nil {
		t.Error(err)
	}
}

// The GUI paces "intensive" and "rescue" must pass the validator.
func TestPacePresetsAreValid(t *testing.T) {
	for name, p := range map[string]struct {
		min, max   string
		nMin, nMax int
	}{
		"intensive": {"8m", "17m", 15, 25},
		"rescue":    {"5m", "10m", 20, 30},
	} {
		c := Default()
		c.Schedule.IntervalMin, c.Schedule.IntervalMax = p.min, p.max
		c.Schedule.APIsPerRoundMin, c.Schedule.APIsPerRoundMax = p.nMin, p.nMax
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
