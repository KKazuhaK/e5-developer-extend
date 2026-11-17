package services

import (
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// The wizard must not start automatic setup under the earlier name of a renamed
// account: its secret is stored under that name.
func TestAppSetupRefusesAnAccountsEarlierName(t *testing.T) {
	r := newSetupRig(t)
	acc := NewAccountService(r.b)
	// A legacy account (no ID, keyed by its name), then renamed.
	lcfg := config.Default()
	lcfg.Accounts = []config.Account{{Name: "old", Auth: config.AuthClientSecret, Tenant: "t.example", ClientID: testGUID}}
	path, err := paths.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := lcfg.Save(path); err != nil {
		t.Fatal(err)
	}
	lv, err := r.b.Ops.OpenVault(lcfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := lv.Put(vault.SecretKey("old"), []byte("X-SECRET")); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.Rename("old", "x"); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ := r.b.Ops.LoadConfig()
	x, _ := cfg.Find("x")
	key := x.Key()
	for _, start := range []AppSetupStart{
		{Name: "old", Auth: config.AuthDeviceCode},
		{Name: "old", AppOnly: true, Username: "u@contoso.example"},
	} {
		err := r.svc.Begin(r.id(), start)
		if err == nil || !strings.Contains(err.Error(), "still used internally by account") {
			t.Errorf("%+v: %v", start, err)
		}
	}
	if len(r.g.Apps) != 0 {
		t.Error("an app registration was created")
	}
	v, err := r.b.Ops.OpenVault(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.Get(vault.SecretKey(key)); err != nil || string(got) != "X-SECRET" {
		t.Errorf("the secret of x: %q %v", got, err)
	}
}
