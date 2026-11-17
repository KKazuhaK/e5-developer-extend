package store

import (
	"os"
	"runtime"
	"testing"
)

func acc(rounds int) *AccountState {
	return &AccountState{Status: StatusOK, Totals: Totals{Rounds: rounds}}
}

func TestPendingImportRoundTripAndCombining(t *testing.T) {
	dir := t.TempDir()
	if p, err := ReadPendingImport(dir); p != nil || err != nil {
		t.Fatalf("empty dir: %v %v", p, err)
	}
	if err := SavePendingImport(dir, &PendingImport{Mode: PendingReplace, Accounts: map[string]*AccountState{"a": acc(1)}}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(PendingImportPath(dir)); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", fi.Mode().Perm())
		}
	}
	// A merge arriving before the engine looked adds to the waiting replace and keeps its mode.
	if err := SavePendingImport(dir, &PendingImport{Mode: PendingMerge, Accounts: map[string]*AccountState{"b": acc(2)}}); err != nil {
		t.Fatal(err)
	}
	p, err := ReadPendingImport(dir)
	if err != nil || p.Mode != PendingReplace || len(p.Accounts) != 2 || p.Accounts["b"].Totals.Rounds != 2 {
		t.Fatalf("after merge: %+v %v", p, err)
	}
	// A replace supersedes whatever waits.
	_ = SavePendingImport(dir, &PendingImport{Mode: PendingReplace, Accounts: map[string]*AccountState{"c": acc(3)}})
	if p, _ := ReadPendingImport(dir); len(p.Accounts) != 1 || p.Accounts["c"] == nil {
		t.Fatalf("after replace: %+v", p)
	}
	if err := RemovePendingImport(dir); err != nil || HasPendingImport(dir) {
		t.Fatal("not removed")
	}
	if err := RemovePendingImport(dir); err != nil {
		t.Fatalf("removing twice: %v", err)
	}
	_ = os.WriteFile(PendingImportPath(dir), []byte(`{"mode":"sideways"}`), 0o600)
	if _, err := ReadPendingImport(dir); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

// A claimed handover is out of the way: an import arriving meanwhile writes a fresh file.
func TestClaimLeavesRoomForANewHandover(t *testing.T) {
	dir := t.TempDir()
	if files, err := ClaimPendingImports(dir); err != nil || len(files) != 0 {
		t.Fatalf("nothing to claim: %v %v", files, err)
	}
	_ = SavePendingImport(dir, &PendingImport{Mode: PendingReplace, Accounts: map[string]*AccountState{"a": acc(1)}})
	files, err := ClaimPendingImports(dir)
	if err != nil || len(files) != 1 || HasPendingImport(dir) {
		t.Fatalf("claim: %v %v", files, err)
	}
	_ = SavePendingImport(dir, &PendingImport{Mode: PendingMerge, Accounts: map[string]*AccountState{"b": acc(2)}})
	first, err := ReadPendingFile(files[0])
	if err != nil || first.Mode != PendingReplace || len(first.Accounts) != 1 || first.Accounts["a"] == nil {
		t.Fatalf("the claimed file changed: %+v %v", first, err)
	}
	fresh, _ := ReadPendingImport(dir)
	if fresh == nil || fresh.Mode != PendingMerge || len(fresh.Accounts) != 1 || fresh.Accounts["b"] == nil {
		t.Fatalf("the new file is not independent: %+v", fresh)
	}
	// An interrupted claim is picked up again, together with a new file.
	again, _ := ClaimPendingImports(dir)
	if len(again) != 2 {
		t.Errorf("claimed %v", again)
	}
	_ = RemovePendingImport(dir)
	if again, _ := ClaimPendingImports(dir); len(again) != 0 {
		t.Errorf("RemovePendingImport left %v", again)
	}
}
