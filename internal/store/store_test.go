package store

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	st, err := s.LoadState()
	if err != nil || len(st.Accounts) != 0 {
		t.Fatalf("empty: %v %+v", err, st)
	}
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.FixedZone("x", 8*3600))
	st.Accounts["main"] = &AccountState{
		NextRun: now, Status: StatusOK, GrantedScopes: []string{"Mail.Read"},
		APISkipUntil: map[string]time.Time{"teams.joined": now.Add(time.Hour)},
		Totals:       Totals{Rounds: 2, Calls: 9},
	}
	if err := s.SaveState(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	a := got.Accounts["main"]
	if !a.NextRun.Equal(now) || a.Totals.Calls != 9 || a.APISkipUntil["teams.joined"].IsZero() {
		t.Errorf("round trip: %+v", a)
	}
}

func TestDamagedState(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := New(dir).LoadState()
	if err == nil || st == nil || st.Accounts == nil {
		t.Errorf("want error and usable empty state, got %v %v", st, err)
	}
}

func TestAppendReadFilter(t *testing.T) {
	s := New(t.TempDir())
	base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acct := "a"
			if i%2 == 1 {
				acct = "b"
			}
			if err := s.Append(Entry{TS: base.Add(time.Duration(i) * time.Minute), Account: acct, API: "x", Status: 200, Class: "ok"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// A second day.
	if err := s.Append(Entry{TS: base.AddDate(0, 0, 1), Account: "a", Event: "start"}); err != nil {
		t.Fatal(err)
	}

	all, _ := s.Read(Filter{})
	if len(all) != 21 {
		t.Fatalf("all: %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].TS.Before(all[i-1].TS) {
			t.Fatal("not oldest first")
		}
	}
	a, _ := s.Read(Filter{Account: "a"})
	if len(a) != 11 {
		t.Errorf("account a: %d", len(a))
	}
	recent, _ := s.Read(Filter{Since: base.Add(15 * time.Minute)})
	if len(recent) != 5+1 {
		t.Errorf("since: %d", len(recent))
	}
	last, _ := s.Read(Filter{Limit: 3})
	if len(last) != 3 || last[2].Event != "start" {
		t.Errorf("limit: %+v", last)
	}
	files, _ := filepath.Glob(filepath.Join(s.Dir(), "logs", "calls-*.jsonl"))
	if len(files) != 2 {
		t.Errorf("files: %v", files)
	}
}

func TestReadSkipsDamagedLines(t *testing.T) {
	s := New(t.TempDir())
	ts := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	_ = s.Append(Entry{TS: ts, Account: "a", API: "x"})
	f := filepath.Join(s.Dir(), "logs", logName(ts))
	fh, _ := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = fh.WriteString("{torn\n")
	_ = fh.Close()
	_ = s.Append(Entry{TS: ts, Account: "a", API: "y"})
	got, _ := s.Read(Filter{})
	if len(got) != 2 {
		t.Errorf("got %d", len(got))
	}
}

func TestPrune(t *testing.T) {
	s := New(t.TempDir())
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 45; i++ {
		_ = s.Append(Entry{TS: now.AddDate(0, 0, -i), Account: "a", Event: "x"})
	}
	n, err := s.Prune(now, 30)
	if err != nil || n != 14 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	files, _ := filepath.Glob(filepath.Join(s.Dir(), "logs", "*"))
	if len(files) != 31 {
		t.Errorf("left %d files", len(files))
	}
	for _, f := range files {
		if !strings.HasPrefix(filepath.Base(f), "calls-") {
			t.Errorf("unexpected %s", f)
		}
	}
}

func TestNotifiedRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	st := NewState()
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	st.Accounts["a"] = &AccountState{Status: StatusOK, Notified: map[string]Notice{"stale": {At: at, Key: "72"}}}
	st.Accounts["b"] = &AccountState{Status: StatusOK}
	if err := s.SaveState(st); err != nil {
		t.Fatal(err)
	}
	back, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if n := back.Accounts["a"].Notified["stale"]; !n.At.Equal(at) || n.Key != "72" {
		t.Errorf("notice = %+v", n)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir(), "state.json"))
	if strings.Count(string(raw), "notified") != 1 {
		t.Errorf("empty notified maps must be omitted:\n%s", raw)
	}
}
