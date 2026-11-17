package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// ropcAuth stands in for a password account: the silent refresh always fails,
// and it signs in with the "password" only as far as the engine's gate allows.
type ropcAuth struct {
	mu      sync.Mutex
	noGate  int
	ropc    int
	failure error // what the password sign-in returns
	homeID  string
}

func (a *ropcAuth) Token(ctx context.Context) (auth.Token, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g := auth.FallbackGate(ctx)
	if g == nil {
		a.noGate++
		return auth.Token{}, fmt.Errorf("%w: refresh rejected", auth.ErrLoginRequired)
	}
	ok, blocked := g.Allow()
	if blocked != "" {
		return auth.Token{}, fmt.Errorf("%w: paused after %s", auth.ErrLoginRequired, blocked)
	}
	if !ok {
		return auth.Token{}, fmt.Errorf("%w: refresh rejected", auth.ErrLoginRequired)
	}
	a.ropc++
	if a.failure != nil {
		g.Result(auth.ErrorCode(a.failure))
		if code := auth.CredentialFailure(a.failure); code != "" {
			g.Failed(code)
		}
		return auth.Token{}, a.failure
	}
	return auth.Token{AccessToken: "t", Account: "alice@example.com", Scopes: delegatedScopes,
		ExpiresOn: time.Now().Add(time.Hour), HomeAccountID: a.homeID}, nil
}
func (a *ropcAuth) Login(context.Context, auth.LoginUI) (auth.Token, error) { return auth.Token{}, nil }
func (a *ropcAuth) Logout(context.Context) error                            { return nil }

func passwordFixture(t *testing.T, ra *ropcAuth) *fixture {
	acct := delegatedAcct("pw")
	acct.Auth = config.AuthPassword
	acct.Username = "alice@example.com"
	acct.HomeAccountID = "uid.tid"
	f := newFixture(t, acct)
	f.opts.NewAuth = func(config.Account) (auth.Authenticator, error) { return ra, nil }
	f.start()
	return f
}

func TestPasswordFailureBlocksFurtherSignIns(t *testing.T) {
	ra := &ropcAuth{failure: fmt.Errorf("%w: AADSTS50126: wrong password", auth.ErrLoginRequired)}
	f := passwordFixture(t, ra)
	f.round("pw")
	st := f.acct("pw")
	if st.PasswordBlocked == nil || st.PasswordBlocked.Code != "AADSTS50126" || ra.ropc != 1 {
		t.Fatalf("state %+v, ropc %d", st, ra.ropc)
	}
	// Even well past the interval, no further attempt while blocked.
	f.jump(24 * time.Hour)
	f.round("pw")
	if ra.ropc != 1 {
		t.Fatalf("ropc = %d, want 1", ra.ropc)
	}

	// set-secret / login / identity edit send the reset trigger.
	if err := TriggerPasswordReset(f.dir, "pw"); err != nil {
		t.Fatal(err)
	}
	f.tickWait()
	st = f.acct("pw")
	if st.PasswordBlocked != nil || !st.LastPasswordAttempt.IsZero() {
		t.Fatalf("not cleared: %+v", st)
	}
	f.round("pw")
	if ra.ropc != 2 {
		t.Fatalf("ropc = %d after reset, want 2", ra.ropc)
	}
}

func TestPasswordSignInIntervalIsPersisted(t *testing.T) {
	ra := &ropcAuth{failure: fmt.Errorf("%w: temporarily broken", auth.ErrLoginRequired)}
	f := passwordFixture(t, ra)
	f.round("pw")
	if ra.ropc != 1 || f.acct("pw").LastPasswordAttempt.IsZero() {
		t.Fatalf("ropc %d, state %+v", ra.ropc, f.acct("pw"))
	}
	f.clock.Advance(5 * time.Minute)
	f.round("pw")
	if ra.ropc != 1 {
		t.Fatalf("second attempt inside the interval: %d", ra.ropc)
	}
	// A restart reads the attempt time back from state.json.
	f.start()
	f.round("pw")
	if ra.ropc != 1 {
		t.Fatalf("attempt time lost over a restart: %d", ra.ropc)
	}
	f.jump(auth.MinPasswordInterval)
	f.round("pw")
	if ra.ropc != 2 {
		t.Fatalf("no attempt after the interval: %d", ra.ropc)
	}
}

func TestPasswordSignInSavesTheHomeAccountID(t *testing.T) {
	ra := &ropcAuth{homeID: "new.id"}
	f := passwordFixture(t, ra)
	var saved string
	f.e.o.SaveHomeAccount = func(_ context.Context, a config.Account, id string) error { saved = a.Name + "=" + id; return nil }
	f.round("pw")
	if saved != "pw=new.id" {
		t.Fatalf("saved %q", saved)
	}
	if a, _ := f.e.cfg.Find("pw"); a.HomeAccountID != "new.id" {
		t.Fatalf("in-memory config not updated: %+v", a)
	}
}

func TestForcedRefreshDoesNotRepeatAFailedPasswordSignIn(t *testing.T) {
	ra := &ropcAuth{failure: fmt.Errorf("%w: AADSTS50076: MFA", auth.ErrLoginRequired)}
	f := passwordFixture(t, ra)
	f.e.updateAccount("pw", func(*store.AccountState) {})
	f.e.withLock(func() { f.e.rt["pw"] = &accountRT{forceTok: true} })
	f.round("pw")
	if ra.ropc != 1 {
		t.Fatalf("ropc = %d, want 1", ra.ropc)
	}
}

func TestResetDuringTheRequestWinsOverTheFailure(t *testing.T) {
	ra := &ropcAuth{}
	f := passwordFixture(t, ra)
	g := &passwordGate{e: f.e, name: "pw"}
	if ok, _ := g.Allow(); !ok {
		t.Fatal("attempt not allowed")
	}
	// A person sets a new password while the request is out.
	f.e.resetPasswordGate("pw")
	g.Result("AADSTS50126")
	g.Failed("AADSTS50126")
	st := f.acct("pw")
	if st.PasswordBlocked != nil || st.LastPasswordCode != "" {
		t.Fatalf("the old attempt's failure blocked the account: %+v", st)
	}
}

func TestFailureIsStoredWithTheAttempt(t *testing.T) {
	ra := &ropcAuth{failure: fmt.Errorf("%w: AADSTS50126: wrong", auth.ErrLoginRequired)}
	f := passwordFixture(t, ra)
	f.round("pw")
	if st := f.acct("pw"); st.LastPasswordCode != "AADSTS50126" {
		t.Fatalf("%+v", st)
	}
}
