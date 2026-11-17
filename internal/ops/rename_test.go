package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// secretAccount adds a client_secret account whose secret is its own name upper-cased.
func (e *testEnv) secretAccount(t *testing.T, name string) {
	t.Helper()
	in := AccountInput{Name: name, Auth: config.AuthClientSecret, Tenant: name + ".example", ClientID: testGUID, Secret: strings.ToUpper(name) + "-SECRET"}
	if err := e.AddAccount(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) secretOf(t *testing.T, key string) string {
	t.Helper()
	cfg, _, _ := e.LoadConfig()
	v, err := e.OpenVault(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := v.Get(vault.SecretKey(key))
	if errors.Is(err, vault.ErrNotFound) {
		return ""
	} else if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (e *testEnv) rename(t *testing.T, from, to string) {
	t.Helper()
	if err := e.RenameAccount(context.Background(), from, to); err != nil {
		t.Fatalf("rename %s to %s: %v", from, to, err)
	}
}

// state seeds state.json with a marker per account key.
func (e *testEnv) seedState(t *testing.T, rounds map[string]int) {
	t.Helper()
	st := store.NewState()
	for k, n := range rounds {
		st.Accounts[k] = &store.AccountState{Status: store.StatusOK, Totals: store.Totals{Rounds: n}}
	}
	if err := store.New(e.dataDir).SaveState(st); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) roundsOf(t *testing.T, key string) int {
	t.Helper()
	st, err := store.New(e.dataDir).LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if as := st.Accounts[key]; as != nil {
		return as.Totals.Rounds
	}
	return -1
}

// dataFiles lists every file under the data directory with its content.
func dataFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

func TestRenameSwapThroughATemporaryNameKeepsEverythingWithItsAccount(t *testing.T) {
	e := newTestEnv(t)
	e.secretAccount(t, "a")
	e.secretAccount(t, "b")
	ka, kb := e.keyOf(t, "a"), e.keyOf(t, "b")
	if ka == kb || ka == "a" || kb == "b" {
		t.Fatalf("keys %q %q", ka, kb)
	}
	e.seedState(t, map[string]int{ka: 11, kb: 22})
	st := store.New(e.dataDir)
	for _, en := range []store.Entry{
		{TS: time.Now(), Account: "a", AccountID: ka, Event: "x"},
		{TS: time.Now(), Account: "b", AccountID: kb, Event: "y"},
	} {
		if err := st.Append(en); err != nil {
			t.Fatal(err)
		}
	}

	e.rename(t, "a", "t")
	e.rename(t, "b", "a")
	e.rename(t, "t", "b")

	if got := e.keyOf(t, "b"); got != ka {
		t.Errorf("the account now called b has key %q, want %q", got, ka)
	}
	if got := e.keyOf(t, "a"); got != kb {
		t.Errorf("the account now called a has key %q, want %q", got, kb)
	}
	// The old "a" (now "b") still has its secret, state and log.
	if e.secretOf(t, e.keyOf(t, "b")) != "A-SECRET" || e.secretOf(t, e.keyOf(t, "a")) != "B-SECRET" {
		t.Error("a secret followed the name instead of the account")
	}
	if e.roundsOf(t, e.keyOf(t, "b")) != 11 || e.roundsOf(t, e.keyOf(t, "a")) != 22 {
		t.Error("a state followed the name instead of the account")
	}
	bf := e.AccountLogFilter("b")
	entries, err := st.Read(bf)
	if err != nil || len(entries) != 1 || entries[0].Event != "x" {
		t.Errorf("log of b: %+v %v", entries, err)
	}
	// Tenant keys follow the key too.
	cfg, _, _ := e.LoadConfig()
	b, _ := cfg.Find("b")
	if b.Tenant != "a.example" {
		t.Errorf("tenant of b: %q", b.Tenant)
	}
}

func TestRenameThenReAddingTheOldNameStartsEmpty(t *testing.T) {
	e := newTestEnv(t)
	e.secretAccount(t, "b")
	old := e.keyOf(t, "b")
	e.seedState(t, map[string]int{old: 9})
	if err := store.New(e.dataDir).Append(store.Entry{TS: time.Now(), Account: "b", AccountID: old, Event: "old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RemoveAccount(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	e.secretAccount(t, "b")
	fresh := e.keyOf(t, "b")
	if fresh == old {
		t.Fatal("a re-added account got the key of the removed one")
	}
	e.rename(t, "b", "c")
	if e.keyOf(t, "c") != fresh {
		t.Error("the rename changed the key")
	}
	if got := e.roundsOf(t, fresh); got != -1 {
		t.Errorf("the new account has state: %d", got)
	}
	if e.secretOf(t, fresh) != "B-SECRET" || e.secretOf(t, old) != "" {
		t.Error("vault entries mixed up")
	}
	entries, _ := store.New(e.dataDir).Read(e.AccountLogFilter("c"))
	if len(entries) != 0 {
		t.Errorf("the new account shows old log lines: %+v", entries)
	}
}

func TestRenameTwiceFailsAndChangesNothing(t *testing.T) {
	e := newTestEnv(t)
	e.secretAccount(t, "a")
	e.rename(t, "a", "z")
	before, _ := os.ReadFile(filepath.Join(e.cfgDir, "config.toml"))
	err := e.RenameAccount(context.Background(), "a", "z")
	if err == nil || !strings.Contains(err.Error(), "no account named") {
		t.Fatalf("second rename: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(e.cfgDir, "config.toml"))
	if string(before) != string(after) {
		t.Error("the failed rename changed the config")
	}
}

func TestRenameWritesOnlyTheConfig(t *testing.T) {
	e := newTestEnv(t)
	e.secretAccount(t, "a")
	k := e.keyOf(t, "a")
	e.seedState(t, map[string]int{k: 3})
	if err := store.New(e.dataDir).Append(store.Entry{TS: time.Now(), Account: "a", AccountID: k, Event: "x"}); err != nil {
		t.Fatal(err)
	}
	vaultBefore := dataFiles(t, filepath.Join(e.dataDir, "vault"))
	dataBefore := dataFiles(t, e.dataDir)
	cfgBefore, _ := os.ReadFile(filepath.Join(e.cfgDir, "config.toml"))

	e.rename(t, "a", "z")

	if !sameFiles(dataBefore, dataFiles(t, e.dataDir)) {
		t.Errorf("the data directory changed:\nbefore %v\nafter  %v", keys(dataBefore), keys(dataFiles(t, e.dataDir)))
	}
	if !sameFiles(vaultBefore, dataFiles(t, filepath.Join(e.dataDir, "vault"))) {
		t.Error("the vault changed")
	}
	cfgAfter, _ := os.ReadFile(filepath.Join(e.cfgDir, "config.toml"))
	if string(cfgAfter) == string(cfgBefore) || !strings.Contains(string(cfgAfter), `'z'`) {
		t.Errorf("config.toml did not change:\n%s", cfgAfter)
	}
}

func TestFailedConfigSaveLeavesEverything(t *testing.T) {
	e := newTestEnv(t)
	e.secretAccount(t, "a")
	e.seedState(t, map[string]int{e.keyOf(t, "a"): 3})
	cfgBefore, _ := os.ReadFile(filepath.Join(e.cfgDir, "config.toml"))
	dataBefore := dataFiles(t, e.dataDir)
	e.Ops.SaveConfig = func(*config.Config, string) error { return errors.New("disk full") }
	if err := e.RenameAccount(context.Background(), "a", "z"); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("rename: %v", err)
	}
	e.Ops.SaveConfig = nil
	cfgAfter, _ := os.ReadFile(filepath.Join(e.cfgDir, "config.toml"))
	if string(cfgBefore) != string(cfgAfter) || !sameFiles(dataBefore, dataFiles(t, e.dataDir)) {
		t.Error("a failed rename changed something")
	}
}

func TestRenameOfALegacyAccountKeepsItsNameKeyedData(t *testing.T) {
	e := newTestEnv(t)
	// An account of an earlier version: no ID, everything under its name.
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Name: "e5", Auth: config.AuthClientSecret, Tenant: "t.example", ClientID: testGUID}}
	if err := cfg.Save(filepath.Join(e.cfgDir, "config.toml")); err != nil {
		t.Fatal(err)
	}
	v, err := e.OpenVault(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Put(vault.SecretKey("e5"), []byte("LEGACY")); err != nil {
		t.Fatal(err)
	}
	e.seedState(t, map[string]int{"e5": 5})
	st := store.New(e.dataDir)
	if err := st.Append(store.Entry{TS: time.Now().Add(-time.Hour), Account: "e5", Event: "before"}); err != nil {
		t.Fatal(err)
	}

	e.rename(t, "e5", "work")

	got, _, _ := e.LoadConfig()
	a, _ := got.Find("work")
	if a.ID != "e5" || a.Key() != "e5" {
		t.Fatalf("the legacy account got %+v", a)
	}
	if e.secretOf(t, a.Key()) != "LEGACY" || e.roundsOf(t, a.Key()) != 5 {
		t.Error("the legacy secret or state was lost")
	}
	// A line written after the rename carries the ID and the new name; the old one shows under the new name.
	if err := st.Append(store.Entry{TS: time.Now(), Account: "work", AccountID: "e5", Event: "after"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := st.Read(e.AccountLogFilter("work"))
	if len(entries) != 2 || entries[0].Event != "before" || entries[1].Event != "after" {
		t.Errorf("log of work: %+v", entries)
	}
	// No other account may take the old name over.
	if err := e.AddAccount(context.Background(), AccountInput{Name: "e5", Auth: config.AuthDeviceCode, Tenant: "t.example", ClientID: testGUID}); err == nil {
		t.Error("an account was added under the key of another")
	}
}

func TestRenameRefusesANameThatIsAnotherAccountsKey(t *testing.T) {
	e := newTestEnv(t)
	e.secretAccount(t, "a")
	e.addDevice(t, "b")
	if err := e.RenameAccount(context.Background(), "b", e.keyOf(t, "a")); err == nil {
		t.Error("renamed to another account's ID")
	}
}

func sameFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
