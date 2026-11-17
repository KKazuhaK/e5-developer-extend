package doctor

import (
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
