package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// An account renamed away from "old" still owns secret/old: automatic setup under
// that name must stop before it creates an app or touches the vault.
func TestAccountAddAutoRefusesAnAccountsEarlierName(t *testing.T) {
	e, g, _ := setupEnv(t)
	useAuth(e, &fakeAuth{tok: goodToken()})
	// A legacy account (no ID, keyed by its name), then renamed.
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Name: "old", Auth: config.AuthClientSecret, Tenant: "t.example", ClientID: testGUID}}
	if err := cfg.Save(filepath.Join(e.cfgDir, "config.toml")); err != nil {
		t.Fatal(err)
	}
	lv, err := vault.Open(cfg.Vault.Backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := lv.Put(vault.SecretKey("old"), []byte("X-SECRET")); err != nil {
		t.Fatal(err)
	}
	e.mustRun("", "account", "rename", "old", "x")
	key := e.keyOf("x")
	if key != "old" {
		t.Fatalf("key %q", key)
	}
	apps := len(g.Apps)

	for _, args := range [][]string{
		{"account", "add", "old", "--auto", "--auth", "client_secret", "--username", "u@contoso.test", "--yes"},
		{"account", "add", "old", "--auto", "--auth", "device_code", "--yes"},
		{"app", "setup", "--app-only", "--account", "old", "--yes"},
	} {
		_, _, err := e.run("", args...)
		if err == nil || !strings.Contains(err.Error(), "still used internally by account") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if len(g.Apps) != apps {
		t.Error("an app registration was created")
	}
	v, _ := vault.Open(e.loadCfg().Vault.Backend)
	if got, err := v.Get(vault.SecretKey(key)); err != nil || string(got) != "X-SECRET" {
		t.Errorf("the secret of x: %q %v", got, err)
	}
}
