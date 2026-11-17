package ops

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

type gateProbe struct{ sawGate bool }

func (p *gateProbe) Token(ctx context.Context) (auth.Token, error) {
	p.sawGate = auth.FallbackGate(ctx) != nil
	return auth.Token{AccessToken: "t", HomeAccountID: "uid.tid"}, nil
}
func (p *gateProbe) Login(context.Context, auth.LoginUI) (auth.Token, error) {
	return auth.Token{HomeAccountID: "uid.tid"}, nil
}
func (p *gateProbe) Logout(context.Context) error { return nil }

func TestAccountTestNeverAllowsPasswordSignIn(t *testing.T) {
	e := newTestEnv(t)
	probe := &gateProbe{}
	e.Ops.NewAuth = func(config.Account, auth.Store, *http.Client) (auth.Authenticator, error) { return probe, nil }
	ctx := context.Background()
	in := AccountInput{Name: "pw", Auth: config.AuthPassword, Tenant: "t.example", ClientID: testGUID, Username: "a@t.example", Secret: "x"}
	if err := e.AddAccount(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := e.TestAccount(ctx, "pw"); err != nil {
		t.Fatal(err)
	}
	if probe.sawGate {
		t.Fatal("account test may only refresh silently")
	}
}

func TestPasswordBlockIsClearedBySetSecretLoginAndEdit(t *testing.T) {
	e := newTestEnv(t)
	probe := &gateProbe{}
	e.Ops.NewAuth = func(config.Account, auth.Store, *http.Client) (auth.Authenticator, error) { return probe, nil }
	ctx := context.Background()
	in := AccountInput{Name: "pw", Auth: config.AuthPassword, Tenant: "t.example", ClientID: testGUID, Username: "a@t.example", Secret: "x", Categories: []string{"user"}}
	if err := e.AddAccount(ctx, in); err != nil {
		t.Fatal(err)
	}
	trigger := filepath.Join(e.dataDir, "trigger", e.keyOf(t, "pw")+".pwreset")
	expect := func(what string, do func() error) {
		t.Helper()
		_ = os.Remove(trigger)
		if err := do(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if _, err := os.Stat(trigger); err != nil {
			t.Errorf("%s did not ask the engine to clear the password block", what)
		}
	}
	expect("set-secret", func() error { return e.SetSecret(ctx, "pw", "new", "") })
	expect("login", func() error { _, err := e.Login(ctx, "pw", nil); return err })
	in.Username = "b@t.example"
	expect("edit identity", func() error { return e.UpdateAccount(ctx, in) })
}

func TestHintsForPasswordSignInFailures(t *testing.T) {
	for code, want := range map[string]string{
		"AADSTS50053": "Smart Lockout",
		"AADSTS50055": "set-secret x",
		"AADSTS50057": "disabled in Entra",
		"AADSTS53003": "Conditional Access policy",
		"AADSTS50126": "set-secret x",
	} {
		err := fmt.Errorf("%w: %s: failed", auth.ErrLoginRequired, code)
		if h := Hint(err, "x"); !strings.Contains(h, want) {
			t.Errorf("%s: %q", code, h)
		}
	}
	if h := Hint(fmt.Errorf("%w: AADSTS70043", auth.ErrLoginRequired), "x"); !strings.Contains(h, "often comes with MFA") {
		t.Errorf("70043 hint lacks the MFA warning: %q", h)
	}
}

func TestSaveHomeAccountOnlyForTheSameIdentity(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	in := AccountInput{Name: "pw", Auth: config.AuthPassword, Tenant: "t.example", ClientID: testGUID, Username: "a@t.example", Secret: "x", Categories: []string{"user"}}
	if err := e.AddAccount(ctx, in); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		cfg, _, err := e.LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		a, _ := cfg.Find("pw")
		return a.HomeAccountID
	}
	cfg, _, _ := e.LoadConfig()
	cur, _ := cfg.Find("pw")
	other := *cur
	other.Username = "someone@else"
	if err := e.saveHomeAccount(ctx, other, "wrong.id"); err != nil {
		t.Fatal(err)
	}
	if read() == "wrong.id" {
		t.Fatal("wrote the id of a sign-in with another identity")
	}
	if err := e.saveHomeAccount(ctx, *cur, "right.id"); err != nil {
		t.Fatal(err)
	}
	if read() != "right.id" {
		t.Fatal("id not saved")
	}
}
