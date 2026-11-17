package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/backup"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

func init() { backup.Iterations = 100_000 }

const cliPass = "cli backup pass"

// addMain creates the account "main" with a stored secret in env e.
func addMain(t *testing.T, e *env, secret string) {
	t.Helper()
	e.mustRun(secret+"\n", "account", "add", "main", "--auth", "client_secret",
		"--tenant", "t.example", "--client-id", testGUID)
}

func TestBackupExportImportCLI(t *testing.T) {
	a := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey([]byte("0123456789abcdef0123456789abcdef")))
	addMain(t, a, "CLI-SECRET-VALUE")
	file := filepath.Join(t.TempDir(), "all.e5backup")

	out := a.mustRun(cliPass+"\n", "backup", "export", file)
	if !strings.Contains(out, "accounts: main") || strings.Contains(out, "CLI-SECRET-VALUE") || strings.Contains(out, cliPass) {
		t.Fatalf("export output: %s", out)
	}
	if fi, err := os.Stat(file); err != nil || fi.Size() == 0 {
		t.Fatalf("no backup file: %v", err)
	}

	// Another machine: new directories and another master key.
	b := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey([]byte("fedcba9876543210fedcba9876543210")))
	if _, _, err := b.run("wrong passphrase\n", "backup", "import", file); err == nil || !strings.Contains(err.Error(), "wrong backup passphrase") {
		t.Fatalf("wrong passphrase: %v", err)
	}
	out = b.mustRun(cliPass+"\n", "backup", "import", file)
	if !strings.Contains(out, "accounts: main") || !strings.Contains(out, "e5extend doctor") || strings.Contains(out, "CLI-SECRET-VALUE") {
		t.Fatalf("import output: %s", out)
	}
	if list := b.mustRun("", "account", "list"); !strings.Contains(list, "main") || !strings.Contains(list, "secret stored") {
		t.Fatalf("account list: %s", list)
	}
	v, err := vault.Open("auto")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.Get("secret/" + b.keyOf("main")); err != nil || string(got) != "CLI-SECRET-VALUE" {
		t.Fatalf("secret after import: %q %v", got, err)
	}

	// Importing again into the now non-empty machine is refused.
	if _, _, err := b.run(cliPass+"\n", "backup", "import", file); err == nil || !strings.Contains(err.Error(), "--merge") {
		t.Fatalf("second import: %v", err)
	}
	if _, _, err := b.run(cliPass+"\n", "backup", "import", file, "--merge", "--replace"); err == nil {
		t.Fatal("--merge with --replace accepted")
	}
	if _, _, err := b.run(cliPass+"\n", "backup", "import", file, "--merge"); err == nil || !strings.Contains(err.Error(), "main") {
		t.Fatalf("merge conflict: %v", err)
	}
	b.mustRun(cliPass+"\n", "backup", "import", file, "--replace")
}

func TestBackupExportRefusesExistingFile(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	addMain(t, e, "S")
	file := filepath.Join(t.TempDir(), "all.e5backup")
	if err := os.WriteFile(file, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No passphrase is given: the refusal comes before it would be read.
	if _, _, err := e.run("", "backup", "export", file); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != "keep me" {
		t.Fatal("file overwritten")
	}
	e.mustRun(cliPass+"\n", "backup", "export", file, "--force")
	if b, _ := os.ReadFile(file); string(b) == "keep me" {
		t.Fatal("--force did not overwrite")
	}
}

func TestBackupPassphraseFromStdinOnly(t *testing.T) {
	e := newEnv(t)
	t.Setenv(vault.EnvMasterKey, vault.EncodeKey(make([]byte, 32)))
	t.Setenv("E5EXTEND_BACKUP_PASSPHRASE", cliPass) // must be ignored
	addMain(t, e, "S")
	file := filepath.Join(t.TempDir(), "x.e5backup")
	if _, _, err := e.run("", "backup", "export", file); err == nil {
		t.Fatal("export without a passphrase on stdin succeeded")
	}
	if _, _, err := e.run("short\n", "backup", "export", file); err == nil || !strings.Contains(err.Error(), "at least 8") {
		t.Fatalf("short passphrase: %v", err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("a file was written")
	}
	// A terminal asks twice; a mismatch stops the export.
	e.interactive = true
	if _, _, err := e.run(cliPass+"\n"+cliPass+"x\n", "backup", "export", file); err == nil {
		t.Fatal("mismatch accepted")
	}
}
