package ops

import (
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func TestSnapshotShowsThrottlingPausedAPIsAndAutoResume(t *testing.T) {
	e := newTestEnv(t)
	e.addDevice(t, "main")
	now := time.Now()
	st := store.New(e.dataDir)
	state := store.NewState()
	state.Accounts["main"] = &store.AccountState{
		Status: store.StatusPaused, Reason: "3 rounds in a row failed; it is tried again automatically",
		NextProbe: now.Add(6 * time.Hour), Probes: 1,
		APISkipUntil: map[string]time.Time{"mail.messages": now.Add(time.Hour), "old": now.Add(-time.Hour)},
	}
	if err := st.SaveState(state); err != nil {
		t.Fatal(err)
	}
	for _, class := range []string{"ok", "throttled", "throttled", "forbidden", "not_found", "server_error"} {
		if err := st.Append(store.Entry{TS: now, Account: "main", Round: "r1", API: "user.me", Class: class}); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := e.Snapshot()
	if err != nil || len(snap.Accounts) != 1 {
		t.Fatalf("%+v %v", snap, err)
	}
	a := snap.Accounts[0]
	if a.TodayOK != 1 || a.TodayFailed != 1 || a.TodayThrottled != 2 || a.PausedAPIs != 1 || a.AutoResumeAt == nil || a.AutoResumeTries != 1 {
		t.Errorf("%+v", a)
	}
}
