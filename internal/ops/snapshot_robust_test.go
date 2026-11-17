package ops

import (
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/callstat"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func TestSnapshotShowsThrottlingPausedAPIsAndAutoResume(t *testing.T) {
	e := newTestEnv(t)
	e.addDevice(t, "main")
	now := time.Now()
	st := store.New(e.dataDir)
	state := store.NewState()
	state.Accounts[e.keyOf(t, "main")] = &store.AccountState{
		Status: store.StatusPaused, Reason: "3 rounds in a row failed; it is tried again automatically",
		NextProbe: now.Add(6 * time.Hour), Probes: 1,
		APISkipUntil: map[string]time.Time{"mail.messages": now.Add(time.Hour), "old": now.Add(-time.Hour)},
	}
	if err := st.SaveState(state); err != nil {
		t.Fatal(err)
	}
	for _, en := range mixedLog(now) {
		en.AccountID = e.keyOf(t, "main")
		if err := st.Append(en); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := e.Snapshot()
	if err != nil || len(snap.Accounts) != 1 {
		t.Fatalf("%+v %v", snap, err)
	}
	a := snap.Accounts[0]
	if a.TodayOK != 1 || a.TodayFailed != 1 || a.TodayThrottled != 1 || a.PausedAPIs != 1 || a.AutoResumeAt == nil || a.AutoResumeTries != 1 {
		t.Errorf("%+v", a)
	}
}

// mixedLog: a throttled-then-ok call, a throttled call, a failure, and refusals.
func mixedLog(now time.Time) []store.Entry {
	e := func(api, class string, status int, ago time.Duration) store.Entry {
		return store.Entry{TS: now.Add(-ago), Account: "main", Round: "r1", API: api, Class: class, Status: status}
	}
	return []store.Entry{
		e("a", "throttled", 429, 5*time.Second), e("a", "ok", 200, 4*time.Second),
		e("b", "throttled", 429, 3*time.Second), e("b", "throttled", 429, 2*time.Second),
		e("c", "server_error", 500, time.Second),
		e("d", "forbidden", 403, time.Second), e("e", "not_found", 404, time.Second),
	}
}

// The status page and the daily summary classify the same log the same way.
func TestSnapshotAndDailySummaryAgree(t *testing.T) {
	now := time.Now()
	entries := mixedLog(now)
	cn := callstat.Tally(entries, now.Add(-time.Hour))

	cfg := config.Default()
	cfg.Notify.DailySummaryAt = now.Format("15:04")
	acct := config.Account{Name: "main", Auth: config.AuthClientSecret}
	st := store.NewState()
	st.Accounts["main"] = &store.AccountState{Status: store.StatusOK}
	evs, _ := notify.Evaluate(notify.Input{Now: now, Start: now.Add(-100 * time.Hour), Config: cfg.Notify,
		Accounts: []config.Account{acct}, State: st, Calls: map[string][]store.Entry{"main": entries}})
	var row notify.SummaryRow
	for _, ev := range evs {
		if ev.Event == notify.DailySummary {
			row = ev.Summary[0]
		}
	}
	if row.OK != cn.OK || row.Failed != cn.Failed || row.Throttled != cn.Throttled || cn != (callstat.Counts{OK: 1, Failed: 1, Throttled: 1}) {
		t.Errorf("summary %+v, tally %+v", row, cn)
	}
}
