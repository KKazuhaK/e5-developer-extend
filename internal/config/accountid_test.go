package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestKeyIsTheIDOrTheName(t *testing.T) {
	a := validAccount("main")
	if a.Key() != "main" {
		t.Errorf("key of an account without ID: %q", a.Key())
	}
	a.ID = "abc234"
	if a.Key() != "abc234" {
		t.Errorf("key of an account with ID: %q", a.Key())
	}
	if got := a.TenantKey(); got != "contoso.onmicrosoft.com" {
		t.Errorf("tenant key %q", got)
	}
	a.Tenant = "organizations"
	if got := a.TenantKey(); got != "account:abc234" {
		t.Errorf("tenant key of a tenant-less account: %q", got)
	}
}

func TestNewAccountID(t *testing.T) {
	re := regexp.MustCompile(`^[a-z2-7]{16}$`)
	seen := map[string]bool{}
	cfg := Default()
	for i := 0; i < 200; i++ {
		id := NewAccountID(cfg)
		if !re.MatchString(id) || seen[id] {
			t.Fatalf("bad or repeated ID %q", id)
		}
		seen[id] = true
		if !ValidName(id) {
			t.Fatalf("an ID must be a valid name: %q", id)
		}
	}
}

func TestConfigWithoutIDsSavesByteIdentical(t *testing.T) {
	cfg := Default()
	cfg.Accounts = []Account{validAccount("main"), validAccount("dev")}
	path := t.TempDir() + "/config.toml"
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	first := mustRead(t, path)
	if strings.Contains(first, "\nid =") {
		t.Fatalf("an empty ID was written:\n%s", first)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Save(path); err != nil {
		t.Fatal(err)
	}
	if second := mustRead(t, path); second != first {
		t.Errorf("saving again changed the file:\n%s\n---\n%s", first, second)
	}
}

func TestAccountKeysAreValidated(t *testing.T) {
	mk := func(accts ...Account) error {
		c := Default()
		c.Accounts = accts
		return c.Validate()
	}
	withID := func(name, id string) Account { a := validAccount(name); a.ID = id; return a }

	if err := mk(withID("a", "idaaaaaaaaaaaaaa"), withID("b", "idbbbbbbbbbbbbbb")); err != nil {
		t.Errorf("valid IDs: %v", err)
	}
	if err := mk(withID("a", "Bad ID")); err == nil {
		t.Error("an invalid ID was accepted")
	}
	if err := mk(withID("a", "same"), withID("b", "same")); err == nil {
		t.Error("two accounts with one ID were accepted")
	}
	// A name must not be another account's key: its state and vault entries would be shared.
	if err := mk(withID("a", "kkkkkkkkkkkkkkkk"), validAccount("kkkkkkkkkkkkkkkk")); err == nil {
		t.Error("an account named like another account's ID was accepted")
	}
	if err := mk(validAccount("x"), withID("y", "x")); err == nil {
		t.Error("an ID equal to another account's name (its key) was accepted")
	}
	// An account may be named like its own ID: that is a renamed legacy account.
	if err := mk(withID("c", "c")); err != nil {
		t.Errorf("name equal to the own ID: %v", err)
	}
	// A hand-edited file is checked the same way.
	text := "version = 1\n[[accounts]]\nname = \"one\"\nid = \"two\"\nauth = \"device_code\"\ntenant = \"t.example\"\nclient_id = \"" + guid + "\"\n" +
		"[[accounts]]\nname = \"two\"\nauth = \"device_code\"\ntenant = \"t.example\"\nclient_id = \"" + guid + "\"\n"
	if _, err := Parse([]byte(text)); err == nil || !strings.Contains(err.Error(), "key") {
		t.Errorf("hand-edited clash: %v", err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
