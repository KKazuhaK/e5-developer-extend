package store

import (
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

	st.ApplyRename("a", "c", nil)
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
	st.ApplyRename("c", "z", nil)
	if !reflect.DeepEqual(st.RenamedFrom, map[string][]string{"z": {"a", "c"}}) {
		t.Errorf("renamed_from after going back: %v", st.RenamedFrom)
	}
}

func TestApplyRenamesRunsInOrderSoSwapsAndChainsWork(t *testing.T) {
	st := NewState()
	st.Accounts["a"] = &AccountState{Totals: Totals{Rounds: 1}, PasswordBlocked: &PasswordBlock{Code: "AADSTS50126"}}
	st.Accounts["b"] = &AccountState{Totals: Totals{Rounds: 2}}
	// A swap through a temporary name.
	applied := st.ApplyRenames([]Rename{{From: "a", To: "tmp"}, {From: "b", To: "a"}, {From: "tmp", To: "b"}}, nil)
	if len(applied) != 3 {
		t.Fatalf("applied %v", applied)
	}
	if st.Accounts["a"].Totals.Rounds != 2 || st.Accounts["b"].Totals.Rounds != 1 || st.Accounts["tmp"] != nil {
		t.Errorf("after the swap: %+v", st.Accounts)
	}
	if st.Accounts["b"].PasswordBlocked == nil || st.Accounts["a"].PasswordBlocked != nil {
		t.Error("the password block did not stay with its account")
	}
	if !reflect.DeepEqual(st.RenamedFrom["a"], []string{"b"}) || !reflect.DeepEqual(st.RenamedFrom["b"], []string{"a", "tmp"}) {
		t.Errorf("renamed_from: %v", st.RenamedFrom)
	}
	// A chain, and a rename onto an account that is added afterwards.
	st.ApplyRenames([]Rename{{From: "b", To: "c"}, {From: "c", To: "d"}}, nil)
	if st.Accounts["d"].Totals.Rounds != 1 || st.Accounts["b"] != nil || st.Accounts["c"] != nil {
		t.Errorf("after the chain: %+v", st.Accounts)
	}
}

func TestApplyRenameTakesFromDetachedAndReplacesAFreshState(t *testing.T) {
	st := NewState()
	st.Accounts["new"] = &AccountState{Status: StatusOK} // made by an engine whose config already had the new name
	detached := map[string]*AccountState{"old": {Totals: Totals{Rounds: 9}}}
	st.ApplyRename("old", "new", detached)
	if st.Accounts["new"].Totals.Rounds != 9 || len(detached) != 0 {
		t.Errorf("accounts %+v detached %v", st.Accounts, detached)
	}
	// An account that never had a state leaves nothing under the new name.
	st.Accounts["x"] = &AccountState{Totals: Totals{Rounds: 3}}
	st.ApplyRename("never-ran", "x", nil)
	if st.Accounts["x"] != nil {
		t.Error("another state was left under the new name")
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

func TestPendingRenameFilesAreClaimedInOrderAndNeverAppendedTo(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := SavePendingRenames(dir, at, []Rename{{From: "a", To: "tmp"}}); err != nil {
		t.Fatal(err)
	}
	first, err := ClaimPendingRenames(dir) // the engine claims [a to tmp] ...
	if err != nil || len(first) != 1 || HasPendingRename(dir) {
		t.Fatalf("claim: %v %v", first, err)
	}
	// ... while b to a is saved: a file of its own, not a change to the claimed one.
	if err := SavePendingRenames(dir, time.Time{}, []Rename{{From: "b", To: "a"}}); err != nil {
		t.Fatal(err)
	}
	if !HasPendingRename(dir) {
		t.Fatal("the rename saved during the claim is not waiting")
	}
	rs, written, errs := ReadClaimedRenames(first)
	if len(errs) != 0 || !reflect.DeepEqual(rs, []Rename{{From: "a", To: "tmp"}}) || !written.Equal(at) {
		t.Errorf("first claim: %v %v %v", rs, written, errs)
	}
	// The engine could not use the first claim and gives it back: it keeps its place in front of the newer file.
	if err := UnclaimRenames(first); err != nil {
		t.Fatal(err)
	}
	if err := SavePendingRenames(dir, time.Time{}, []Rename{{From: "tmp", To: "b"}}); err != nil {
		t.Fatal(err)
	}
	all, err := ClaimPendingRenames(dir)
	if err != nil || len(all) != 3 {
		t.Fatalf("claim: %v %v", all, err)
	}
	rs, _, _ = ReadClaimedRenames(all)
	if !reflect.DeepEqual(rs, []Rename{{From: "a", To: "tmp"}, {From: "b", To: "a"}, {From: "tmp", To: "b"}}) {
		t.Errorf("order: %v", rs)
	}
	// An interrupted claim is found again, and removing deletes everything.
	again, _ := ClaimPendingRenames(dir)
	if len(again) != 3 {
		t.Errorf("claimed files not found again: %v", again)
	}
	if err := RemovePendingRename(dir); err != nil {
		t.Fatal(err)
	}
	if left, _ := ClaimPendingRenames(dir); len(left) != 0 || HasPendingRename(dir) {
		t.Errorf("left: %v", left)
	}
}

func TestUncommittedRenameCountsOnlyWhenTheConfigGotThatFar(t *testing.T) {
	cfg := func(names ...string) func(string) bool {
		return func(n string) bool {
			for _, x := range names {
				if x == n {
					return true
				}
			}
			return false
		}
	}
	r := Rename{From: "a", To: "b", Uncommitted: true}
	if r.Valid(cfg("a")) || r.Valid(cfg("a", "b")) || r.Valid(cfg("c")) {
		t.Error("applied although the config did not get that far")
	}
	if !r.Valid(cfg("b")) {
		t.Error("not applied although the config has the new name")
	}
	if !(Rename{From: "a", To: "b"}).Valid(cfg("a")) {
		t.Error("a committed rename is always valid")
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
