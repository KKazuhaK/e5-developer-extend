package services

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-renew/internal/backup"
	"github.com/KKazuhaK/e5-renew/internal/ops"
	"github.com/KKazuhaK/e5-renew/internal/vault"
)

func init() { backup.Iterations = 100_000 }

const guiBackupPass = "gui backup pass"

func TestBackupServiceRoundTrip(t *testing.T) {
	// Machine A.
	a := newRig(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey([]byte("0123456789abcdef0123456789abcdef")))
	if err := NewAccountService(a.b).Add(AccountInput{
		Name: "main", Auth: "client_secret", Tenant: "t.example", ClientID: testGUID, Secret: secretValue,
	}); err != nil {
		t.Fatal(err)
	}
	if err := NewSettingsService(a.b).SetWebhook(webhookURL); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "mine") // no extension: the service adds one
	a.b.PickSaveFile = func(title, name string) (string, error) {
		if title != "Save here" || name != "e5extend-backup-"+time.Now().Format("20060102")+".e5backup" {
			t.Errorf("dialog args: %q %q", title, name)
		}
		return file, nil
	}
	bs := NewBackupService(a.b)
	res, err := bs.Export(guiBackupPass, false, "Save here")
	if err != nil {
		t.Fatal(err)
	}
	a.keep(res)
	if res.Cancelled || res.Summary.Path != file+".e5backup" || strings.Join(res.Summary.Accounts, ",") != "main" || res.Summary.VaultEntries != 2 {
		t.Fatalf("export result: %+v", res)
	}
	if _, err := os.Stat(file + ".e5backup"); err != nil {
		t.Fatal(err)
	}

	// Cancelling the dialog changes nothing.
	a.b.PickSaveFile = func(string, string) (string, error) { return "", nil }
	if res, err := bs.Export(guiBackupPass, false, "x"); err != nil || !res.Cancelled {
		t.Fatalf("cancel: %+v %v", res, err)
	}
	a.b.PickSaveFile = nil
	if _, err := bs.Export(guiBackupPass, false, "x"); err == nil {
		t.Fatal("export without a dialog succeeded")
	}

	// Machine B: empty, another master key.
	b := newRig(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey([]byte("fedcba9876543210fedcba9876543210")))
	bb := NewBackupService(b.b)
	if has, err := bb.TargetHasData(); err != nil || has {
		t.Fatalf("empty target: %v %v", has, err)
	}
	b.b.PickOpenFile = func(string) (string, error) { return file + ".e5backup", nil }
	if res, err := bb.Import("wrong passphrase", "default", "Open"); err != nil || res.Problem == nil || res.Problem.Code != "wrong_passphrase" {
		t.Fatalf("wrong passphrase: %+v %v", res, err)
	}
	if _, err := bb.Import(guiBackupPass, "sideways", "Open"); err == nil {
		t.Fatal("bad mode accepted")
	}
	ires, err := bb.Import(guiBackupPass, "default", "Open")
	if err != nil {
		t.Fatal(err)
	}
	b.keep(ires)
	if ires.Cancelled || strings.Join(ires.Summary.Accounts, ",") != "main" || ires.Summary.Hint == "" {
		t.Fatalf("import result: %+v", ires)
	}
	if has, _ := bb.TargetHasData(); !has {
		t.Error("TargetHasData is false after an import")
	}
	cfgB, _, err := b.b.Ops.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	v, err := b.b.Ops.OpenVault(cfgB)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.Get("secret/main"); err != nil || string(got) != secretValue {
		t.Fatalf("secret in B: %q %v", got, err)
	}
	if res, err := bb.Import(guiBackupPass, "default", "Open"); err != nil || res.Problem == nil || res.Problem.Code != "target_has_data" {
		t.Fatalf("second default import: %+v %v", res, err)
	}
	if res, err := bb.Import(guiBackupPass, "merge", "Open"); err != nil || res.Problem == nil || res.Problem.Code != "merge_conflict" || strings.Join(res.Problem.Names, ",") != "main" {
		t.Fatalf("merge import: %+v %v", res, err)
	}
	b.b.PickOpenFile = func(string) (string, error) { return "", nil }
	if res, err := bb.Import(guiBackupPass, "replace", "Open"); err != nil || !res.Cancelled {
		t.Fatalf("cancel: %+v %v", res, err)
	}

	for _, r := range append(a.returned, b.returned...) {
		for _, bad := range []string{secretValue, webhookToken, guiBackupPass} {
			if strings.Contains(r, bad) {
				t.Errorf("a result carries %q: %s", bad, r)
			}
		}
	}
	if _, err := json.Marshal(res); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultBackupName(t *testing.T) {
	if got := defaultBackupName(time.Date(2026, 11, 16, 23, 0, 0, 0, time.Local)); got != "e5extend-backup-20261116.e5backup" {
		t.Errorf("got %q", got)
	}
	if !regexp.MustCompile(`^e5extend-backup-\d{8}\.e5backup$`).MatchString(defaultBackupName(time.Now())) {
		t.Error("pattern")
	}
}

// The extension is added after the dialog confirmed a different name: that
// path was never confirmed, so it must not be overwritten.
func TestExportDoesNotOverwriteAFileTheDialogDidNotConfirm(t *testing.T) {
	r := newRig(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey([]byte("0123456789abcdef0123456789abcdef")))
	if err := NewAccountService(r.b).Add(AccountInput{
		Name: "main", Auth: "client_secret", Tenant: "t.example", ClientID: testGUID, Secret: secretValue,
	}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	existing := filepath.Join(dir, "mine.e5backup")
	if err := os.WriteFile(existing, []byte("PRECIOUS"), 0o600); err != nil {
		t.Fatal(err)
	}
	bs := NewBackupService(r.b)

	// "mine" becomes "mine.e5backup", which exists.
	r.b.PickSaveFile = func(string, string) (string, error) { return filepath.Join(dir, "mine"), nil }
	res, err := bs.Export(guiBackupPass, false, "Save")
	if err != nil || res.Problem == nil || res.Problem.Code != "file_exists" {
		t.Fatalf("got %+v %v", res, err)
	}
	if got, _ := os.ReadFile(existing); string(got) != "PRECIOUS" {
		t.Fatal("the file was overwritten")
	}
	if strings.Contains(res.Problem.Detail, "--") {
		t.Errorf("a command line flag reached the interface: %q", res.Problem.Detail)
	}

	// The exact path the dialog returned (it asked about overwriting) is overwritten.
	r.b.PickSaveFile = func(string, string) (string, error) { return existing, nil }
	res, err = bs.Export(guiBackupPass, false, "Save")
	if err != nil || res.Problem != nil {
		t.Fatalf("got %+v %v", res, err)
	}
	if got, _ := os.ReadFile(existing); string(got) == "PRECIOUS" {
		t.Fatal("the confirmed overwrite did not happen")
	}
}

func TestProblemsAreTypedAndCarryNoFlags(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
	}{
		{backup.ErrNotBackup, "not_backup"}, {backup.ErrTooNew, "newer_version"}, {backup.ErrDecrypt, "wrong_passphrase"},
		{backup.ErrTooLarge, "too_large"}, {backup.ErrPayloadTooLarge, "payload_too_large"}, {backup.ErrBadFile, "bad_file"},
		{backup.ErrShortPassphrase, "short_passphrase"},
		{&ops.BackupError{Code: ops.CodeLocalConfigBroken, Msg: "x --replace", Err: errors.New("line 3")}, "local_config_broken"},
		{&ops.BackupError{Code: ops.CodeRollbackFailedMerge, Names: []string{"a"}, Msg: "x --replace", Err: errors.New("disk")}, "rollback_failed_merge"},
	} {
		p := problemOf(c.err)
		if p == nil || p.Code != c.code {
			t.Errorf("%v: %+v", c.err, p)
			continue
		}
		if strings.Contains(p.Detail, "--") {
			t.Errorf("%s: flag in %q", c.code, p.Detail)
		}
	}
	if problemOf(errors.New("other")) != nil {
		t.Error("an unknown error was typed")
	}
}
