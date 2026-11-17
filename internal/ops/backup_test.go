package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/KKazuhaK/e5-renew/internal/backup"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/store"
	"github.com/KKazuhaK/e5-renew/internal/vault"
)

func init() {
	backup.Iterations = 100_000              // the lowest value Open accepts; keeps the tests quick
	engineProbeWait = 100 * time.Millisecond // tests that hold the engine lock do not wait the production 1.5 s
}

const (
	bkPass        = "backup passphrase 1"
	bkSecretMain  = "CLIENT-SECRET-MAIN-AAA"
	bkSecretOld   = "CLIENT-SECRET-OLD-BBB"
	bkWebhookA    = "https://hooks.example/WEBHOOK-TOKEN-AAA"
	bkWebhookB    = "https://hooks.example/WEBHOOK-TOKEN-BBB"
	bkTokenCacheA = "TOKENCACHE-MAIN-AAA"
)

// machine is one simulated computer: its own directories and its own master key.
type machine struct {
	t               *testing.T
	cfgDir, dataDir string
	key             []byte
	*Ops
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	keyring.MockInit()
	key, err := vault.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	m := &machine{t: t, cfgDir: t.TempDir(), dataDir: t.TempDir(), key: key}
	m.Ops = &Ops{NoCallGap: true, VaultFor: func(*config.Config) (*vault.Vault, error) { return m.vault(key), nil }}
	for _, v := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		t.Setenv(v, "")
	}
	return m
}

func (m *machine) vault(key []byte) *vault.Vault {
	return vault.New(filepath.Join(m.dataDir, "vault"), func() ([]byte, error) { return key, nil })
}

// use points the process at this machine's directories.
func (m *machine) use() {
	m.t.Helper()
	m.t.Setenv("E5EXTEND_CONFIG_DIR", m.cfgDir)
	m.t.Setenv("E5EXTEND_DATA_DIR", m.dataDir)
	m.t.Setenv(vault.EnvMasterKey, "")
	m.t.Setenv("CREDENTIALS_DIRECTORY", "")
}

func (m *machine) addSecretAccount(name, secret string) {
	m.t.Helper()
	m.use()
	in := AccountInput{Name: name, Auth: config.AuthClientSecret, Tenant: "t.example", ClientID: testGUID, Secret: secret}
	if err := m.AddAccount(context.Background(), in); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) put(key, val string) {
	m.t.Helper()
	if err := m.vault(m.key).Put(key, []byte(val)); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) get(key string) (string, error) {
	b, err := m.vault(m.key).Get(key)
	return string(b), err
}

func (m *machine) mustGet(key string) string {
	m.t.Helper()
	s, err := m.get(key)
	if err != nil {
		m.t.Fatalf("%s: %v", key, err)
	}
	return s
}

// edit changes the saved config.
func (m *machine) edit(change func(*config.Config)) {
	m.t.Helper()
	m.use()
	cfg, path, err := m.LoadConfigOrDefault()
	if err != nil {
		m.t.Fatal(err)
	}
	change(cfg)
	if err := cfg.Save(path); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) config() *config.Config {
	m.t.Helper()
	cfg, err := config.Load(filepath.Join(m.cfgDir, "config.toml"))
	if err != nil {
		m.t.Fatal(err)
	}
	return cfg
}

func (m *machine) saveState(accounts map[string]int) {
	m.t.Helper()
	st := store.NewState()
	for name, rounds := range accounts {
		st.Accounts[name] = &store.AccountState{Status: store.StatusOK, Totals: store.Totals{Rounds: rounds}}
	}
	if err := store.New(m.dataDir).SaveState(st); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) state() *store.State {
	m.t.Helper()
	st, err := store.New(m.dataDir).LoadState()
	if err != nil {
		m.t.Fatal(err)
	}
	return st
}

func (m *machine) write(dir, name, content string) {
	m.t.Helper()
	root := m.dataDir
	if dir == "cfg" {
		root = m.cfgDir
	}
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		m.t.Fatal(err)
	}
}

// snapshot hashes every file of both directories, ignoring lock files.
func (m *machine) snapshot() map[string][32]byte {
	m.t.Helper()
	out := map[string][32]byte{}
	for _, root := range []string{m.cfgDir, m.dataDir} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && !strings.HasSuffix(p, ".lock") {
				b, _ := os.ReadFile(p)
				out[p] = sha256.Sum256(b)
			}
			return nil
		})
	}
	return out
}

func (m *machine) mustNotChange(before map[string][32]byte) {
	m.t.Helper()
	after := m.snapshot()
	if len(after) != len(before) {
		m.t.Fatalf("files changed: %d before, %d after", len(before), len(after))
	}
	for p, h := range before {
		if after[p] != h {
			m.t.Fatalf("%s changed", p)
		}
	}
}

// sourceMachine is a fully populated machine A.
func sourceMachine(t *testing.T) *machine {
	t.Helper()
	a := newMachine(t)
	a.addSecretAccount("main", bkSecretMain)
	a.use()
	in := AccountInput{Name: "dev", Auth: config.AuthDeviceCode, Tenant: "t.example", ClientID: testGUID}
	if err := a.AddAccount(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	a.edit(func(c *config.Config) {
		c.Schedule.IntervalMin = "50m"
		c.Notify.StaleAfter = "48h"
		c.Vault.Backend = config.BackendKeyring
		c.Accounts[1].HomeAccountID = "uid.tid"
	})
	a.put("tokencache/main", bkTokenCacheA)
	a.put("tokencache/dev", "TOKENCACHE-DEV")
	a.put("notify/webhook", bkWebhookA)
	a.saveState(map[string]int{"main": 7, "dev": 3})
	a.write("cfg", "gui.json", `{"language":"zh","theme":"dark"}`)
	a.write("data", "logs/calls-2026-11-16.jsonl", "{\"ts\":\"2026-11-16T10:00:00Z\",\"account\":\"main\"}\n")
	// Things that must never travel.
	a.write("data", "heartbeat", "x")
	a.write("data", "engine.lock", "x")
	a.write("data", ".reload", "x")
	a.write("data", "gui.log", "gui log text")
	return a
}

func (m *machine) export(withLogs bool) string {
	m.t.Helper()
	m.use()
	path := filepath.Join(m.t.TempDir(), "all.e5backup")
	if _, err := m.ExportBackup(context.Background(), path, bkPass, ExportOptions{WithLogs: withLogs}); err != nil {
		m.t.Fatal(err)
	}
	return path
}

func (m *machine) importFrom(path string, mode ImportMode) (ImportSummary, error) {
	m.use()
	return m.ImportBackup(context.Background(), path, bkPass, mode)
}

func noLeak(t *testing.T, what string, texts ...string) {
	t.Helper()
	for _, s := range texts {
		for _, secret := range []string{bkSecretMain, bkSecretOld, bkWebhookA, bkWebhookB, "WEBHOOK-TOKEN", bkTokenCacheA, bkPass} {
			if strings.Contains(s, secret) {
				t.Errorf("%s leaks %q: %s", what, secret, s)
			}
		}
	}
}

func TestBackupRoundTripBetweenMachines(t *testing.T) {
	a := sourceMachine(t)
	a.use()
	file := filepath.Join(t.TempDir(), "all.e5backup")
	esum, err := a.ExportBackup(context.Background(), file, bkPass, ExportOptions{WithLogs: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(esum.Accounts, ",") != "main,dev" || esum.VaultEntries != 4 || !esum.IncludesLogs || esum.LogFiles != 1 {
		t.Fatalf("export summary: %+v", esum)
	}
	raw, _ := os.ReadFile(file)
	for _, s := range []string{bkSecretMain, "WEBHOOK-TOKEN", bkTokenCacheA, "gui log text"} {
		if bytes.Contains(raw, []byte(s)) {
			t.Fatalf("the backup file holds %q in plain text", s)
		}
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", fi.Mode().Perm())
		}
	}
	// The payload carries exactly what it should, and none of the local-only files.
	p, err := backup.Open(raw, bkPass)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Vault) != 4 || p.State == nil || p.GUI == nil || len(p.Logs) != 1 {
		t.Fatalf("payload: vault=%d state=%v gui=%v logs=%d", len(p.Vault), p.State != nil, p.GUI != nil, len(p.Logs))
	}
	if !strings.Contains(p.Config, "main") {
		t.Fatal("config text missing")
	}

	// Machine B: empty, a different master key, its own [vault] choice.
	b := newMachine(t)
	if bytes.Equal(a.key, b.key) {
		t.Fatal("test machines share a key")
	}
	b.edit(func(c *config.Config) { c.Vault.Backend = config.BackendFile })

	isum, err := b.importFrom(file, ImportDefault)
	if err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(isum)
	noLeak(t, "import summary", string(js))
	if strings.Join(isum.Accounts, ",") != "main,dev" || isum.Mode != ImportDefault || isum.SourceOS != runtime.GOOS ||
		isum.VaultEntries != 4 || isum.Hint == "" || isum.NewMasterKey {
		t.Fatalf("import summary: %+v", isum)
	}

	if got := b.mustGet("secret/main"); got != bkSecretMain {
		t.Errorf("secret = %q", got)
	}
	if b.mustGet("tokencache/main") != bkTokenCacheA || b.mustGet("tokencache/dev") != "TOKENCACHE-DEV" || b.mustGet("notify/webhook") != bkWebhookA {
		t.Error("token caches or webhook differ")
	}
	// The data was re-encrypted with B's key: A's key cannot read it.
	if _, err := vault.New(filepath.Join(b.dataDir, "vault"), func() ([]byte, error) { return a.key, nil }).Get("secret/main"); !errors.Is(err, vault.ErrWrongKey) {
		t.Errorf("A's key opens B's vault: %v", err)
	}
	cfg := b.config()
	if cfg.Schedule.IntervalMin != "50m" || cfg.Notify.StaleAfter != "48h" || len(cfg.Accounts) != 2 || cfg.Accounts[1].HomeAccountID != "uid.tid" {
		t.Errorf("config: %+v", cfg)
	}
	if cfg.Vault.Backend != config.BackendFile {
		t.Errorf("[vault] was overwritten: %q", cfg.Vault.Backend)
	}
	if st := b.state(); st.Accounts["main"] == nil || st.Accounts["main"].Totals.Rounds != 7 || st.Accounts["dev"].Totals.Rounds != 3 {
		t.Errorf("state: %+v", st.Accounts)
	}
	if got := string(mustRead(t, filepath.Join(b.cfgDir, "gui.json"))); !strings.Contains(got, `"dark"`) {
		t.Errorf("gui.json: %s", got)
	}
	if got := string(mustRead(t, filepath.Join(b.dataDir, "logs", "calls-2026-11-16.jsonl"))); !strings.Contains(got, `"main"`) {
		t.Errorf("call records: %s", got)
	}
	for _, name := range []string{"heartbeat", ".reload", "gui.log", store.PendingImportFile} {
		if _, err := os.Stat(filepath.Join(b.dataDir, name)); err == nil {
			t.Errorf("%s was imported", name)
		}
	}
	// The import takes the engine lock to write the state; the file it leaves is not A's.
	if got, _ := os.ReadFile(filepath.Join(b.dataDir, "engine.lock")); string(got) == "x" {
		t.Error("engine.lock was imported")
	}
}

func TestExportWithoutLogsLeavesThemOut(t *testing.T) {
	a := sourceMachine(t)
	file := a.export(false)
	raw, _ := os.ReadFile(file)
	p, err := backup.Open(raw, bkPass)
	if err != nil || p.Logs != nil {
		t.Fatalf("logs %v err %v", p.Logs, err)
	}
}

func TestDefaultImportRefusesNonEmptyTargetAndWritesNothing(t *testing.T) {
	file := sourceMachine(t).export(true)

	withAccount := newMachine(t)
	withAccount.addSecretAccount("mine", "MINE")
	withData := newMachine(t)
	withData.put("secret/orphan", "x")

	for name, b := range map[string]*machine{"account": withAccount, "vault data": withData} {
		before := b.snapshot()
		_, err := b.importFrom(file, ImportDefault)
		if err == nil || !strings.Contains(err.Error(), "--merge") || !strings.Contains(err.Error(), "--replace") {
			t.Fatalf("%s: err = %v", name, err)
		}
		b.mustNotChange(before)
	}
}

func TestMergeConflictWritesNothing(t *testing.T) {
	file := sourceMachine(t).export(true)
	b := newMachine(t)
	b.addSecretAccount("main", bkSecretOld)
	b.addSecretAccount("fresh", "F")
	before := b.snapshot()
	_, err := b.importFrom(file, ImportMerge)
	if err == nil || !strings.Contains(err.Error(), "main") || strings.Contains(err.Error(), "dev,") {
		t.Fatalf("err = %v", err)
	}
	noLeak(t, "merge error", err.Error())
	b.mustNotChange(before)
	if b.mustGet("secret/main") != bkSecretOld {
		t.Fatal("the machine's own secret was touched")
	}
}

func TestMergeAddsOnlyNewAccounts(t *testing.T) {
	file := sourceMachine(t).export(true)
	b := newMachine(t)
	b.addSecretAccount("old", bkSecretOld)
	b.edit(func(c *config.Config) { c.Schedule.IntervalMin = "45m"; c.Vault.Backend = config.BackendFile })
	b.put("notify/webhook", bkWebhookB)
	b.put("tokencache/old", "OLD-CACHE")
	b.saveState(map[string]int{"old": 11})
	b.write("cfg", "gui.json", `{"language":"en","theme":"light"}`)

	sum, err := b.importFrom(file, ImportMerge)
	if err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(sum)
	noLeak(t, "merge summary", string(js))
	if strings.Join(sum.Accounts, ",") != "main,dev" || len(sum.Skipped) < 2 {
		t.Fatalf("summary: %+v", sum)
	}
	cfg := b.config()
	if len(cfg.Accounts) != 3 || cfg.Accounts[0].Name != "old" || cfg.Schedule.IntervalMin != "45m" ||
		cfg.Notify.StaleAfter == "48h" || cfg.Vault.Backend != config.BackendFile {
		t.Fatalf("config: %+v", cfg)
	}
	if b.mustGet("secret/main") != bkSecretMain || b.mustGet("tokencache/main") != bkTokenCacheA {
		t.Error("imported account's entries missing")
	}
	if b.mustGet("secret/old") != bkSecretOld || b.mustGet("tokencache/old") != "OLD-CACHE" || b.mustGet("notify/webhook") != bkWebhookB {
		t.Error("existing entries changed")
	}
	st := b.state()
	if st.Accounts["old"].Totals.Rounds != 11 || st.Accounts["main"].Totals.Rounds != 7 || st.Accounts["dev"].Totals.Rounds != 3 {
		t.Errorf("state: %+v", st.Accounts)
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(b.cfgDir, "gui.json"))), `"light"`) {
		t.Error("gui settings changed")
	}
	if _, err := os.Stat(filepath.Join(b.dataDir, "logs")); err == nil {
		t.Error("merge imported call records")
	}
}

func TestReplaceSwapsEverythingExceptVaultSetting(t *testing.T) {
	file := sourceMachine(t).export(true)
	b := newMachine(t)
	b.addSecretAccount("old", bkSecretOld)
	b.edit(func(c *config.Config) { c.Schedule.IntervalMin = "45m"; c.Vault.Backend = config.BackendFile })
	b.put("tokencache/old", "OLD-CACHE")
	b.put("notify/webhook", bkWebhookB)
	b.saveState(map[string]int{"old": 11})
	b.write("cfg", "gui.json", `{"language":"en","theme":"light"}`)
	b.write("data", "logs/calls-2026-11-15.jsonl", "old record\n")

	sum, err := b.importFrom(file, ImportReplace)
	if err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(sum)
	noLeak(t, "replace summary", string(js))
	cfg := b.config()
	if len(cfg.Accounts) != 2 || cfg.Accounts[0].Name != "main" || cfg.Schedule.IntervalMin != "50m" || cfg.Vault.Backend != config.BackendFile {
		t.Fatalf("config: %+v", cfg)
	}
	keys, _ := b.vault(b.key).List()
	if strings.Join(keys, ",") != "notify/webhook,secret/main,tokencache/dev,tokencache/main" {
		t.Fatalf("vault keys after replace: %v", keys)
	}
	if b.mustGet("notify/webhook") != bkWebhookA || b.mustGet("secret/main") != bkSecretMain {
		t.Error("vault content wrong")
	}
	if st := b.state(); st.Accounts["old"] != nil || st.Accounts["main"].Totals.Rounds != 7 {
		t.Errorf("state: %+v", st.Accounts)
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(b.cfgDir, "gui.json"))), `"dark"`) {
		t.Error("gui settings not replaced")
	}
	if _, err := os.Stat(filepath.Join(b.dataDir, "logs", "calls-2026-11-16.jsonl")); err != nil {
		t.Error("call records not copied")
	}
}

func TestReplaceRemovesWebhookTheBackupLacks(t *testing.T) {
	a := newMachine(t)
	a.addSecretAccount("main", bkSecretMain)
	file := a.export(false)
	b := newMachine(t)
	b.addSecretAccount("old", bkSecretOld)
	b.put("notify/webhook", bkWebhookB)
	if _, err := b.importFrom(file, ImportReplace); err != nil {
		t.Fatal(err)
	}
	if _, err := b.get("notify/webhook"); !vault.IsNotFound(err) {
		t.Fatalf("webhook kept: %v", err)
	}
}

func TestExportFailsListingEntriesThatCannotBeDecrypted(t *testing.T) {
	a := newMachine(t)
	a.addSecretAccount("main", bkSecretMain)
	a.addSecretAccount("two", "TWO-SECRET")
	// Two entries written with a key this machine no longer has.
	other, _ := vault.GenerateKey()
	for _, k := range []string{"secret/two", "tokencache/two"} {
		if err := a.vault(other).Put(k, []byte("UNREADABLE-PLAINTEXT")); err != nil {
			t.Fatal(err)
		}
	}
	a.use()
	file := filepath.Join(t.TempDir(), "x.e5backup")
	_, err := a.ExportBackup(context.Background(), file, bkPass, ExportOptions{})
	if err == nil {
		t.Fatal("export succeeded")
	}
	msg := err.Error()
	if !strings.Contains(msg, "secret/two") || !strings.Contains(msg, "tokencache/two") || strings.Contains(msg, "secret/main") {
		t.Errorf("keys not listed as expected: %s", msg)
	}
	if !strings.Contains(msg, "master key") {
		t.Errorf("no hint: %s", msg)
	}
	noLeak(t, "export error", msg, "UNREADABLE-PLAINTEXT")
	if strings.Contains(msg, "UNREADABLE-PLAINTEXT") {
		t.Error("error leaks a value")
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("a partial backup was written")
	}
}

func TestExportRefusesOverwriteUnlessForced(t *testing.T) {
	a := sourceMachine(t)
	a.use()
	dir := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(dir, "x.e5backup")
	if err := os.WriteFile(file, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExportBackup(context.Background(), file, bkPass, ExportOptions{}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v", err)
	}
	if string(mustRead(t, file)) != "precious" {
		t.Fatal("existing file was overwritten")
	}
	if _, err := a.ExportBackup(context.Background(), file, bkPass, ExportOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(mustRead(t, file), []byte("precious")) {
		t.Fatal("force did not overwrite")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
			t.Errorf("the chosen folder's mode changed to %v", fi.Mode().Perm())
		}
	}
	if err := CheckBackupTarget(dir, true); err == nil {
		t.Error("a directory was accepted as the target")
	}
	if _, err := a.ExportBackup(context.Background(), filepath.Join(dir, "short"), "short", ExportOptions{}); !errors.Is(err, backup.ErrShortPassphrase) {
		t.Errorf("short passphrase: %v", err)
	}
}

func TestImportRejectsBadFilesWithoutWriting(t *testing.T) {
	file := sourceMachine(t).export(true)
	b := newMachine(t)
	b.use()
	before := b.snapshot()

	if _, err := b.ImportBackup(context.Background(), file, "wrong passphrase", ImportDefault); !errors.Is(err, backup.ErrDecrypt) {
		t.Errorf("wrong passphrase: %v", err)
	}
	junk := filepath.Join(t.TempDir(), "junk")
	_ = os.WriteFile(junk, []byte("hello"), 0o600)
	if _, err := b.ImportBackup(context.Background(), junk, bkPass, ImportDefault); !errors.Is(err, backup.ErrNotBackup) {
		t.Errorf("junk: %v", err)
	}
	if _, err := b.ImportBackup(context.Background(), filepath.Join(t.TempDir(), "missing"), bkPass, ImportDefault); err == nil {
		t.Error("missing file accepted")
	}
	if _, err := b.ImportBackup(context.Background(), file, bkPass, "sideways"); err == nil {
		t.Error("unknown mode accepted")
	}
	b.mustNotChange(before)
}

func TestImportValidatesThePayloadBeforeWriting(t *testing.T) {
	good := "version = 1\n\n[[accounts]]\nname = \"main\"\nauth = \"client_secret\"\ntenant = \"t.example\"\nclient_id = \"" + testGUID + "\"\n"
	cases := map[string]*backup.Payload{
		"invalid config":     {Config: "version = 1\n[schedule]\ninterval_min = \"x\"\n"},
		"newer config":       {Config: "version = 99\n"},
		"unknown vault key":  {Config: good, Vault: map[string][]byte{"secret/ghost": []byte("x")}},
		"traversing key":     {Config: good, Vault: map[string][]byte{"../../evil": []byte("x")}},
		"bad state":          {Config: good, State: json.RawMessage(`[1,2]`)},
		"newer state":        {Config: good, State: json.RawMessage(`{"version":99,"accounts":{}}`)},
		"bad gui":            {Config: good, GUI: json.RawMessage(`"text"`)},
		"traversing logname": {Config: good, Logs: map[string]string{"../calls-2026-01-01.jsonl": "x"}},
		"odd logname":        {Config: good, Logs: map[string]string{"notes.txt": "x"}},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			b := newMachine(t)
			b.use()
			data, err := backup.Seal(p, bkPass)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "x.e5backup")
			_ = os.WriteFile(file, data, 0o600)
			before := b.snapshot()
			if _, err := b.ImportBackup(context.Background(), file, bkPass, ImportReplace); err == nil {
				t.Fatal("accepted")
			}
			b.mustNotChange(before)
		})
	}
}

func TestImportCreatesMasterKeyOnAFreshMachine(t *testing.T) {
	file := sourceMachine(t).export(false)
	keyring.MockInit()
	b := newMachine(t)
	b.use()
	keyring.MockInit() // an empty keyring: this machine has no master key yet
	real := &Ops{NoCallGap: true}
	sum, err := real.ImportBackup(context.Background(), file, bkPass, ImportDefault)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.NewMasterKey {
		t.Fatalf("summary: %+v", sum)
	}
	cfg := b.config()
	v, err := real.OpenVault(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.Get("secret/main"); err != nil || string(got) != bkSecretMain {
		t.Fatalf("secret after import: %q %v", got, err)
	}

	// A second machine that has data but lost its key must not get a new one.
	c := newMachine(t)
	c.use()
	c.put("secret/orphan", "x")
	keyring.MockInit()
	before := c.snapshot()
	if _, err := real.ImportBackup(context.Background(), file, bkPass, ImportReplace); !errors.Is(err, vault.ErrNoMasterKey) {
		t.Fatalf("err = %v", err)
	}
	c.mustNotChange(before)
}

// Once config.toml is saved the import has succeeded: a state.json that cannot
// be written is a note, in every mode, and the accounts and secrets are in place.
func TestFailureWritingStatisticsIsOnlyANote(t *testing.T) {
	file := sourceMachine(t).export(false)
	for _, mode := range []ImportMode{ImportDefault, ImportReplace, ImportMerge} {
		t.Run(string(mode), func(t *testing.T) {
			b := newMachine(t)
			b.use()
			if mode == ImportMerge {
				b.addSecretAccount("mine", bkSecretOld)
			}
			// A directory in the place of state.json makes writing it fail.
			if err := os.MkdirAll(filepath.Join(b.dataDir, "state.json", "x"), 0o700); err != nil {
				t.Fatal(err)
			}
			sum, err := b.importFrom(file, mode)
			if err != nil {
				t.Fatalf("a statistics failure failed the import: %v", err)
			}
			var found bool
			for _, n := range sum.NoteCodes {
				found = found || (n.Code == NoteStatsNotImported && strings.Join(n.Args, ",") == "state")
			}
			if !found || len(sum.Notes) != len(sum.NoteCodes) {
				t.Fatalf("notes: %+v / %v", sum.NoteCodes, sum.Notes)
			}
			noLeak(t, "notes", strings.Join(sum.Notes, "\n"))
			if b.mustGet("secret/main") != bkSecretMain || len(b.config().Accounts) < 2 {
				t.Error("accounts and secrets are not in place")
			}
		})
	}
}

func TestRollbackFailedAdviceDependsOnTheMode(t *testing.T) {
	cause := errors.New("disk full")
	var be *BackupError
	for _, mode := range []ImportMode{ImportDefault, ImportReplace} {
		err := rollbackFailed(cause, mode, []string{"secret/a"}, []string{"a"})
		if !errors.As(err, &be) || be.Code != CodeRollbackFailed || strings.Join(be.Names, ",") != "secret/a" {
			t.Fatalf("%s: %#v", mode, err)
		}
		if !strings.Contains(err.Error(), "e5extend doctor") || !strings.Contains(err.Error(), "--replace") || strings.Contains(err.Error(), "merge again") {
			t.Errorf("%s advice: %v", mode, err)
		}
	}
	err := rollbackFailed(cause, ImportMerge, []string{"secret/a"}, []string{"a", "b"})
	if !errors.As(err, &be) || be.Code != CodeRollbackFailedMerge || strings.Join(be.Names, ",") != "a,b" {
		t.Fatalf("merge: %#v", err)
	}
	for _, want := range []string{"a, b", "e5extend account remove", "merge again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("merge advice lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "--replace") {
		t.Errorf("merge advice sends the user to --replace: %v", err)
	}
}

func TestBackupTimestampIsRFC3339(t *testing.T) {
	a := newMachine(t)
	a.addSecretAccount("main", bkSecretMain)
	p, err := backup.Open(mustRead(t, a.export(false)), bkPass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339, p.Created); err != nil {
		t.Fatal(err)
	}
}

// ---- ordering and rollback ----

// vaultImage is every vault entry of a machine, decrypted.
func (m *machine) vaultImage() map[string]string {
	m.t.Helper()
	v := m.vault(m.key)
	keys, err := v.List()
	if err != nil {
		m.t.Fatal(err)
	}
	out := map[string]string{}
	for _, k := range keys {
		b, err := v.Get(k)
		if err != nil {
			m.t.Fatalf("%s: %v", k, err)
		}
		out[k] = string(b)
	}
	return out
}

func failSave(*config.Config, string) error { return errors.New("disk full") }

func TestReplaceKeepsOldVaultWhenTheConfigCannotBeSaved(t *testing.T) {
	file := sourceMachine(t).export(true)
	b := newMachine(t)
	b.addSecretAccount("old", bkSecretOld)
	b.put("tokencache/old", "OLD-CACHE")
	b.put("notify/webhook", bkWebhookB)
	b.saveState(map[string]int{"old": 11})
	b.use()
	beforeVault := b.vaultImage()
	beforeFiles := b.snapshot()

	b.Ops.SaveConfig = failSave
	_, err := b.importFrom(file, ImportReplace)
	if err == nil {
		t.Fatal("import succeeded although the config could not be saved")
	}
	var be *BackupError
	if !errors.As(err, &be) || be.Code != CodeSaveFailed || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("error: %#v", err)
	}
	noLeak(t, "error", err.Error())
	// The old secrets still decrypt, and nothing the import added is left behind.
	if got := b.vaultImage(); !equalMaps(got, beforeVault) {
		t.Fatalf("vault changed:\nbefore %v\nafter  %v", beforeVault, got)
	}
	if b.mustGet("secret/old") != bkSecretOld || b.mustGet("notify/webhook") != bkWebhookB {
		t.Error("old secrets do not decrypt")
	}
	b.mustNotChange(beforeFiles)

	// The same import works once the disk is fine.
	b.Ops.SaveConfig = nil
	if _, err := b.importFrom(file, ImportReplace); err != nil {
		t.Fatal(err)
	}
	if _, err := b.get("secret/old"); !vault.IsNotFound(err) {
		t.Errorf("the old entry is still there after a good replace: %v", err)
	}
}

func TestRollbackRestoresEntriesThatCannotBeDecrypted(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.addSecretAccount("main", bkSecretOld) // the backup also has "main": its entry gets overwritten
	b.use()
	// An entry encrypted with another key: it cannot be read, but rollback must give it back byte for byte.
	other, _ := vault.GenerateKey()
	if err := vault.New(filepath.Join(b.dataDir, "vault"), func() ([]byte, error) { return other, nil }).Put("tokencache/main", []byte("X")); err != nil {
		t.Fatal(err)
	}
	before := b.snapshot()
	b.Ops.SaveConfig = failSave
	if _, err := b.importFrom(file, ImportReplace); err == nil {
		t.Fatal("succeeded")
	}
	b.mustNotChange(before)
}

func TestDefaultImportRollsBackToAnEmptyVault(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.use()
	before := b.snapshot()
	b.Ops.SaveConfig = failSave
	if _, err := b.importFrom(file, ImportDefault); err == nil {
		t.Fatal("succeeded")
	}
	if keys, _ := b.vault(b.key).List(); len(keys) != 0 {
		t.Errorf("vault entries left behind: %v", keys)
	}
	b.mustNotChange(before)
}

func TestMergeRollsBackToTheOldVault(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.addSecretAccount("mine", bkSecretOld)
	b.use()
	before, files := b.vaultImage(), b.snapshot()
	b.Ops.SaveConfig = failSave
	if _, err := b.importFrom(file, ImportMerge); err == nil {
		t.Fatal("succeeded")
	}
	if got := b.vaultImage(); !equalMaps(got, before) {
		t.Fatalf("vault changed: %v", got)
	}
	b.mustNotChange(files)
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// Old entries a replace drops are removed only after the new config is saved.
func TestReplaceRemovesStaleEntriesOnlyAfterTheConfigIsSaved(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.addSecretAccount("old", bkSecretOld)
	b.use()
	var sawStaleAtSave bool
	b.Ops.SaveConfig = func(c *config.Config, path string) error {
		_, err := b.get("secret/old")
		sawStaleAtSave = err == nil
		return c.Save(path)
	}
	if _, err := b.importFrom(file, ImportReplace); err != nil {
		t.Fatal(err)
	}
	if !sawStaleAtSave {
		t.Error("the old entry was deleted before the config was saved")
	}
	if _, err := b.get("secret/old"); !vault.IsNotFound(err) {
		t.Errorf("stale entry kept: %v", err)
	}
}

// ---- the backup's own [vault] setting ----

func TestImportIgnoresTheBackupsVaultBackend(t *testing.T) {
	cfg := "version = 1\n\n[vault]\nbackend = \"cloud\"\n\n[[accounts]]\nname = \"main\"\nauth = \"client_secret\"\ntenant = \"t.example\"\nclient_id = \"" + testGUID + "\"\n"
	data, err := backup.Seal(&backup.Payload{Config: cfg, Vault: map[string][]byte{"secret/main": []byte(bkSecretMain)}}, bkPass)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "x.e5backup")
	_ = os.WriteFile(file, data, 0o600)
	b := newMachine(t)
	b.edit(func(c *config.Config) { c.Vault.Backend = config.BackendFile })
	if _, err := b.importFrom(file, ImportReplace); err != nil {
		t.Fatalf("a backend this machine never uses blocked the import: %v", err)
	}
	if got := b.config().Vault.Backend; got != config.BackendFile {
		t.Errorf("backend = %q, want this machine's", got)
	}
}

// ---- an invalid local config ----

const brokenConfig = "version = 1\n\n[vault]\nbackend = \"file\"\n\n[schedule]\ninterval_min = \"soon\"\n"

func TestReplaceSetsABrokenLocalConfigAside(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.use()
	b.write("cfg", "config.toml", brokenConfig)
	sum, err := b.importFrom(file, ImportReplace)
	if err != nil {
		t.Fatal(err)
	}
	cfg := b.config()
	if len(cfg.Accounts) != 2 || cfg.Vault.Backend != config.BackendFile {
		t.Errorf("config after replace: accounts=%d backend=%s (want [vault] taken from the broken file)", len(cfg.Accounts), cfg.Vault.Backend)
	}
	var aside string
	for _, n := range sum.NoteCodes {
		if n.Code == NoteConfigSetAside && len(n.Args) == 1 {
			aside = n.Args[0]
		}
	}
	if !strings.HasPrefix(aside, "config.toml.broken-") {
		t.Fatalf("no set-aside note: %+v", sum.NoteCodes)
	}
	if got := string(mustRead(t, filepath.Join(b.cfgDir, aside))); got != brokenConfig {
		t.Errorf("the broken file was not kept: %q", got)
	}
	if len(sum.Notes) != len(sum.NoteCodes) || !strings.Contains(strings.Join(sum.Notes, "\n"), aside) {
		t.Errorf("notes: %v", sum.Notes)
	}
}

func TestReplaceWithAnUnreadableVaultSectionUsesAuto(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.use()
	b.write("cfg", "config.toml", "this is [not toml\n")
	if _, err := b.importFrom(file, ImportReplace); err != nil {
		t.Fatal(err)
	}
	if got := b.config().Vault.Backend; got != config.BackendAuto {
		t.Errorf("backend = %q", got)
	}
}

func TestDefaultAndMergeRefuseABrokenLocalConfig(t *testing.T) {
	file := sourceMachine(t).export(false)
	for _, mode := range []ImportMode{ImportDefault, ImportMerge} {
		t.Run(string(mode), func(t *testing.T) {
			b := newMachine(t)
			b.use()
			b.write("cfg", "config.toml", brokenConfig)
			before := b.snapshot()
			_, err := b.importFrom(file, mode)
			var be *BackupError
			if !errors.As(err, &be) || be.Code != CodeLocalConfigBroken || !strings.Contains(err.Error(), "--replace") {
				t.Fatalf("error: %v", err)
			}
			b.mustNotChange(before)
		})
	}
}

// ---- typed errors ----

func TestRefusalsAreTyped(t *testing.T) {
	file := sourceMachine(t).export(false)
	b := newMachine(t)
	b.addSecretAccount("main", bkSecretOld)
	code := func(err error) (string, []string) {
		var be *BackupError
		if errors.As(err, &be) {
			return be.Code, be.Names
		}
		return "", nil
	}
	_, err := b.importFrom(file, ImportDefault)
	if c, _ := code(err); c != CodeTargetHasData {
		t.Errorf("default: %v", err)
	}
	_, err = b.importFrom(file, ImportMerge)
	if c, n := code(err); c != CodeMergeConflict || strings.Join(n, ",") != "main" {
		t.Errorf("merge: %v", err)
	}
	if c, _ := code(CheckBackupTarget(file, false)); c != CodeFileExists {
		t.Error("export target that exists is not typed")
	}
	bad := &backup.Payload{Config: "version = 1\n[schedule]\ninterval_min = \"x\"\n"}
	data, _ := backup.Seal(bad, bkPass)
	f := filepath.Join(t.TempDir(), "bad")
	_ = os.WriteFile(f, data, 0o600)
	_, err = newMachine(t).importFrom(f, ImportDefault)
	if c, _ := code(err); c != CodeInvalidBackup {
		t.Errorf("invalid content: %v", err)
	}
}

// ---- the size cap ----

func TestExportRefusesContentImportWouldReject(t *testing.T) {
	a := newMachine(t)
	a.addSecretAccount("main", bkSecretMain)
	a.use()
	// A sparse file of NULs: JSON writes each as six bytes, so 17 MiB of them go over the cap
	// while only a few KiB are written to disk.
	logs := filepath.Join(a.dataDir, "logs")
	_ = os.MkdirAll(logs, 0o700)
	f, err := os.Create(filepath.Join(logs, "calls-2026-11-01.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(17 << 20); err != nil {
		t.Skipf("no sparse files here: %v", err)
	}
	f.Close()

	target := filepath.Join(t.TempDir(), "big.e5backup")
	_, err = a.ExportBackup(context.Background(), target, bkPass, ExportOptions{WithLogs: true})
	if !errors.Is(err, backup.ErrPayloadTooLarge) || !strings.Contains(err.Error(), "call records") {
		t.Fatalf("error: %v", err)
	}
	if _, serr := os.Stat(target); serr == nil {
		t.Error("a file was written")
	}
	// Without the records it is fine.
	if _, err := a.ExportBackup(context.Background(), target, bkPass, ExportOptions{}); err != nil {
		t.Fatal(err)
	}
}
