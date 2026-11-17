package store

import (
	"os"
	"reflect"
	"testing"
	"time"
)

func TestApplyRenameMovesEverythingKeyedByTheName(t *testing.T) {
	st := NewState()
	st.Accounts["a"] = &AccountState{Status: StatusOK, Totals: Totals{Rounds: 3}}
	st.Accounts["b"] = &AccountState{Totals: Totals{Rounds: 1}}
	st.Tenants = map[string]*TenantState{"account:a": {Access: AccessOK}, "contoso.example": {}}
	st.TenantAliases = map[string]string{"account:a": "guid", "other": "account:a"}
	st.RenamedFrom = map[string][]string{"a": {"z"}}

	st.ApplyRename("a", "c")
	if st.Accounts["a"] != nil || st.Accounts["c"] == nil || st.Accounts["c"].Totals.Rounds != 3 || st.Accounts["b"].Totals.Rounds != 1 {
		t.Errorf("accounts: %+v", st.Accounts)
	}
	if st.Tenants["account:a"] != nil || st.Tenants["account:c"] == nil || st.Tenants["contoso.example"] == nil {
		t.Errorf("tenants: %+v", st.Tenants)
	}
	if st.TenantAliases["account:c"] != "guid" || st.TenantAliases["other"] != "account:c" || len(st.TenantAliases) != 2 {
		t.Errorf("aliases: %v", st.TenantAliases)
	}
	if !reflect.DeepEqual(st.RenamedFrom, map[string][]string{"c": {"z", "a"}}) {
		t.Errorf("renamed_from: %v", st.RenamedFrom)
	}

	// Back to an earlier name: it is no longer "renamed from" itself.
	st.ApplyRename("c", "z")
	if !reflect.DeepEqual(st.RenamedFrom, map[string][]string{"z": {"a", "c"}}) {
		t.Errorf("renamed_from after going back: %v", st.RenamedFrom)
	}
}

func TestApplyRenamesChecksTheConfiguration(t *testing.T) {
	st := NewState()
	st.Accounts["a"] = &AccountState{Totals: Totals{Rounds: 3}}
	st.Accounts["x"] = &AccountState{Totals: Totals{Rounds: 9}}
	st.Accounts["y"] = &AccountState{Totals: Totals{Rounds: 2}}
	cfg := map[string]bool{"b": true, "x": true, "y": true, "w": true}
	applied, later := st.ApplyRenames([]Rename{
		{From: "a", To: "b"}, // applies
		{From: "q", To: "r"}, // new name not configured yet: later
		{From: "x", To: "w"}, // old name still configured: dropped
		{From: "y", To: "y"}, // nothing to do
	}, func(n string) bool { return cfg[n] })
	if len(applied) != 1 || applied[0].To != "b" || len(later) != 1 || later[0].To != "r" {
		t.Errorf("applied %v later %v", applied, later)
	}
	if st.Accounts["b"] == nil || st.Accounts["a"] != nil || st.Accounts["x"] == nil {
		t.Errorf("accounts: %+v", st.Accounts)
	}

	// A new name whose state has recorded rounds is never replaced.
	st.Accounts["n"] = &AccountState{Totals: Totals{Rounds: 4}}
	st.Accounts["m"] = &AccountState{Totals: Totals{Rounds: 1}}
	cfg["n"] = true
	if applied, _ := st.ApplyRenames([]Rename{{From: "m", To: "n"}}, func(n string) bool { return cfg[n] }); len(applied) != 0 || st.Accounts["n"].Totals.Rounds != 4 {
		t.Errorf("replaced a state with rounds: %v", applied)
	}
}

func TestTidyRenames(t *testing.T) {
	st := NewState()
	st.RenamedFrom = map[string][]string{"b": {"a", "old"}, "gone": {"g"}, "d": {"c"}}
	cfg := map[string]bool{"b": true, "a": true, "d": true}
	st.TidyRenames(func(n string) bool { return cfg[n] })
	// "a" is a configured account again, so it is not an earlier name of b.
	if !reflect.DeepEqual(st.RenamedFrom, map[string][]string{"b": {"old"}, "d": {"c"}}) {
		t.Errorf("renamed_from: %v", st.RenamedFrom)
	}
}

func TestCollapseRenames(t *testing.T) {
	got := CollapseRenames([]Rename{{"a", "b"}, {"x", "y"}, {"b", "c"}, {"c", "a"}, {"p", "q"}, {"q", "p"}})
	if !reflect.DeepEqual(got, []Rename{{"x", "y"}}) {
		t.Errorf("%v", got)
	}
}

func TestPendingRenameFile(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := SavePendingRenames(dir, at, []Rename{{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := SavePendingRenames(dir, time.Time{}, []Rename{{"b", "c"}}); err != nil {
		t.Fatal(err)
	}
	files, err := ClaimPendingRenames(dir)
	if err != nil || len(files) != 1 || HasPendingRename(dir) {
		t.Fatalf("claim: %v %v", files, err)
	}
	rs, written, errs := ReadClaimedRenames(files)
	if len(errs) != 0 || !reflect.DeepEqual(rs, []Rename{{"a", "c"}}) || !written.Equal(at) {
		t.Errorf("read: %v %v %v", rs, written, errs)
	}
	// An interrupted claim is found again.
	again, _ := ClaimPendingRenames(dir)
	if len(again) != 1 {
		t.Errorf("claimed file not found again: %v", again)
	}
	if err := RemovePendingRename(dir); err != nil {
		t.Fatal(err)
	}
	if again, _ := ClaimPendingRenames(dir); len(again) != 0 {
		t.Errorf("left: %v", again)
	}
	// A rename that cancels out leaves no file.
	_ = SavePendingRenames(dir, time.Time{}, []Rename{{"a", "b"}})
	_ = SavePendingRenames(dir, time.Time{}, []Rename{{"b", "a"}})
	if _, err := os.Stat(PendingRenamePath(dir)); err == nil {
		t.Error("an empty rename file was kept")
	}
}

func TestReadFollowsRenamedFrom(t *testing.T) {
	s := New(t.TempDir())
	now := time.Now()
	for _, a := range []string{"old", "new", "other"} {
		if err := s.Append(Entry{TS: now, Account: a, API: "me"}); err != nil {
			t.Fatal(err)
		}
	}
	st := NewState()
	st.RenamedFrom = map[string][]string{"new": {"old"}}
	if err := s.SaveState(st); err != nil {
		t.Fatal(err)
	}
	if es, _ := s.Read(Filter{Account: "new"}); len(es) != 2 {
		t.Errorf("new: %v", es)
	}
	if es, _ := s.Read(Filter{Account: "old"}); len(es) != 1 {
		t.Errorf("old: %v", es)
	}
	if es, _ := s.Read(Filter{}); len(es) != 3 {
		t.Errorf("all: %v", es)
	}
}
