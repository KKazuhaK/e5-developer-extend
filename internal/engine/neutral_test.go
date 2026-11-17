package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// An account without a mailbox: one API per round, 404 for every mail, calendar,
// contacts and tasks API (a good part of the catalog), 200 for the rest. It
// must never be paused, however often a round picks a refused API.
func TestFewRefusedAPIsPerRoundNeverPauseTheAccount(t *testing.T) {
	f := newFixture(t, delegatedAcct("main"))
	f.cfg.Schedule.APIsPerRoundMin, f.cfg.Schedule.APIsPerRoundMax = 1, 1
	f.auths["main"].tok.Scopes = []string{"User.Read", "Mail.Read", "Calendars.Read", "Contacts.Read", "Tasks.Read", "Directory.Read.All"}
	f.h.setStatus(func(p string) (int, map[string]string) {
		for _, s := range []string{"messages", "mailFolders", "mailboxSettings", "events", "calendar", "contacts", "todo", "planner"} {
			if strings.Contains(p, s) {
				return 404, nil
			}
		}
		return 200, nil
	})
	f.start()
	for i := 0; i < 40; i++ {
		f.round("main")
		if as := f.acct("main"); as.Status != store.StatusOK {
			t.Fatalf("round %d: %+v", i+1, as)
		}
		if i%3 == 0 { // let the 24 h pauses expire now and then, as they do over days
			f.jump(25 * time.Hour)
		}
	}
	if f.acct("main").LastSuccess.IsZero() {
		t.Error("no success at all")
	}
}

func TestProbeDoesNotResumeOnTheTokenAlone(t *testing.T) {
	f, pausedAt := pausedFixture(t, nil)
	f.h.setStatus(nil) // Graph is fine again ...
	// ... but every API the account may call is still marked refused.
	f.e.updateAccount("main", func(as *store.AccountState) {
		as.APISkipUntil = map[string]time.Time{}
		for _, id := range []string{"user.me", "org.info", "user.memberof", "user.list", "group.list", "apps.list", "org.domains"} {
			as.APISkipUntil[id] = pausedAt.Add(30 * time.Hour)
		}
		// everything else the default scopes allow is refused as well
		for _, a := range f.e.apis {
			as.APISkipUntil[a.ID] = pausedAt.Add(30 * time.Hour)
		}
	})
	calls := f.h.total()
	f.clock.Set(pausedAt.Add(6 * time.Hour))
	f.tickWait()
	as := f.acct("main")
	if as.Status != store.StatusPaused || as.Probes != 1 || f.h.total() != calls {
		t.Fatalf("resumed or called without a usable API: %+v (%d calls)", as, f.h.total()-calls)
	}
	if f.hasEvent("auto_resume") {
		t.Error("auto_resume event")
	}
}
