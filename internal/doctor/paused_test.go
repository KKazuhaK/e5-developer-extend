package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func TestPausedAPIsAndAutoResumeAreShown(t *testing.T) {
	r := newRig(t)
	r.state(func(as *store.AccountState) {
		as.APISkipUntil = map[string]time.Time{"mail.messages": now.Add(3 * time.Hour), "mail.folders": now.Add(time.Hour), "old": now.Add(-time.Hour)}
	})
	expect(t, r.run(true), "account.main.apis", OK, "2 APIs paused for 24 h")

	r.state(func(as *store.AccountState) {
		as.Status, as.Reason = store.StatusPaused, "3 rounds in a row failed"
		as.NextProbe = now.Add(90 * time.Minute)
	})
	expect(t, r.run(true), "account.main.state", Fail, "tried again automatically in 1h30m0s")
}

func TestFirstSignInHint(t *testing.T) {
	const id = "account.main.apis"
	hint := "office.com"
	t.Run("refused mail and never a success", func(t *testing.T) {
		r := newRig(t)
		r.logRound("r1", -5*time.Hour, map[string]int{"mail.messages": 404, "user.me": 200})
		r.logRound("r2", -4*time.Hour, map[string]int{"drive.info": 403})
		r.logRound("r3", -3*time.Hour, map[string]int{"mail.settings": 404})
		r.logRound("r4", -2*time.Hour, map[string]int{"mail.rules": 404})
		expect(t, r.run(true), id, Warn, hint)
	})
	t.Run("many paused APIs", func(t *testing.T) {
		r := newRig(t)
		r.logRound("r1", -5*time.Hour, map[string]int{"mail.messages": 404, "user.me": 200})
		r.state(func(as *store.AccountState) {
			until := now.Add(3 * time.Hour)
			as.APISkipUntil = map[string]time.Time{"mail.messages": until, "mail.folders": until, "drive.info": until}
		})
		expect(t, r.run(true), id, Warn, hint)
	})
	t.Run("a mail success means no hint", func(t *testing.T) {
		r := newRig(t)
		r.logRound("r1", -6*time.Hour, map[string]int{"mail.folders": 200})
		for i, rd := range []string{"r2", "r3", "r4"} {
			r.logRound(rd, -time.Duration(5-i)*time.Hour, map[string]int{"mail.messages": 404})
		}
		expect(t, r.run(true), id, Warn, "category mail")
		if c := r.run(true)[id]; strings.Contains(c.Detail, hint) {
			t.Errorf("hint shown: %q", c.Detail)
		}
	})
	t.Run("a few refusals are fine", func(t *testing.T) {
		r := newRig(t)
		r.logRound("r1", -5*time.Hour, map[string]int{"mail.messages": 404, "user.me": 200})
		expect(t, r.run(true), id, OK, "no category")
	})
}
