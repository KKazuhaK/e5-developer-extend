package ops

import (
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func TestAPIStatsRows(t *testing.T) {
	e := newTestEnv(t)
	e.addDevice(t, "a")
	e.addDevice(t, "b")
	now := time.Now()
	state := store.NewState()
	state.Accounts["a"] = &store.AccountState{Status: store.StatusOK, APIStats: map[string]*store.APIStat{
		"user.me":    {OK: 3, Failed: 1, LastClass: "ok", LastStatus: 200, LastAt: now},
		"mail.rules": {Failed: 2, LastClass: "forbidden", LastStatus: 403, LastAt: now},
	}}
	state.Accounts["b"] = &store.AccountState{Status: store.StatusOK, APIStats: map[string]*store.APIStat{
		"user.me": {OK: 2, LastClass: "ok", LastStatus: 200, LastAt: now.Add(time.Hour)},
	}}
	if err := store.New(e.dataDir).SaveState(state); err != nil {
		t.Fatal(err)
	}
	rows, err := e.APIStats("a")
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %+v, err %v", rows, err)
	}
	if rows[0].ID != "user.me" || rows[0].Total != 4 || rows[0].OK != 3 || rows[0].Failed != 1 {
		t.Errorf("row 0 = %+v", rows[0])
	}
	all, err := e.APIStats("")
	if err != nil || len(all) != 2 || all[0].ID != "user.me" || all[0].Total != 6 || all[0].OK != 5 {
		t.Errorf("sum = %+v, err %v", all, err)
	}
	if _, err := e.APIStats("nobody"); err == nil {
		t.Error("unknown account accepted")
	}
}
