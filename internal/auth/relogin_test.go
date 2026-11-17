package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

func passwordAcct(home string) config.Account {
	a := fakeAcct(config.AuthPassword)
	a.Username = "me@contoso.test"
	a.HomeAccountID = home
	return a
}

// passwordSetup logs a password account in once and returns a fresh
// Authenticator over the same vault whose access token is about to expire, so
// the next Token() needs the refresh token.
func passwordSetup(t *testing.T, f *fakeAAD) (Authenticator, *vault.Vault) {
	t.Helper()
	f.expiresIn = 60
	v := testVaultWithKey(t, t.TempDir(), mustKey(t))
	if err := v.Put(vault.SecretKey("main"), []byte("hunter2")); err != nil {
		t.Fatal(err)
	}
	a, err := NewWithOptions(passwordAcct(""), v, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	tok, err := a.Login(ctx5(t), &testUI{})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := NewWithOptions(passwordAcct(tok.HomeAccountID), v, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	return a2, v
}

const ca70043 = `{"error":"invalid_grant","error_description":"AADSTS70043: The refresh token has expired or is invalid due to sign-in frequency checks by conditional access.","error_codes":[70043]}`

// testGate is a PasswordGate that records what Token asked of it.
type testGate struct {
	allow   bool
	blocked string
	failed  []string
	allows  int
	last    string
}

func (g *testGate) Allow() (bool, string) { g.allows++; return g.allow && g.blocked == "", g.blocked }
func (g *testGate) Result(code string)    { g.last = code }
func (g *testGate) LastCode() string      { return g.last }
func (g *testGate) Failed(code string)    { g.failed = append(g.failed, code); g.blocked = code }

// withGate is ctx5 plus permission to sign in with the password.
func withGate(t *testing.T) (context.Context, *testGate) {
	g := &testGate{allow: true}
	return AllowPasswordFallback(ctx5(t), g), g
}

func (f *fakeAAD) grants(g string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[g]
}

func TestPasswordSilentSuccessDoesNotUsePassword(t *testing.T) {
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	if _, err := a.Token(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	if n := f.grants("password"); n != 1 { // only the initial login
		t.Fatalf("password grants = %d, want 1", n)
	}
}

func TestPasswordSignsInAgainWhenRefreshIsRejected(t *testing.T) {
	ctx, _ := withGate(t)
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	f.failWith("refresh_token", jsonError(400, ca70043))
	tok, err := a.Token(ctx)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if tok.AccessToken == "" || tok.HomeAccountID != "uid1.tid1" {
		t.Fatalf("token = %+v", tok)
	}
	if n := f.grants("password"); n != 2 {
		t.Fatalf("password grants = %d, want 2", n)
	}
}

func TestPasswordNoSignInAfterLogout(t *testing.T) {
	ctx, _ := withGate(t)
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	if err := a.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	before := f.grants("password")
	if _, err := a.Token(ctx); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("got %v", err)
	}
	if f.grants("password") != before {
		t.Fatal("signed in again after an explicit logout")
	}
}

func TestPasswordSignInAgainFailsWithMFA(t *testing.T) {
	ctx, _ := withGate(t)
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	f.failWith("refresh_token", jsonError(400, ca70043))
	f.failWith("password", jsonError(400, `{"error":"invalid_grant","error_description":"AADSTS50076: Due to a configuration change, you must use multi-factor authentication.","error_codes":[50076]}`))
	_, err := a.Token(ctx)
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "AADSTS50076") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatal("password leaked into the error")
	}
}

func TestPasswordSignInAgainWithOtherRefusalIsLoginRequired(t *testing.T) {
	ctx, _ := withGate(t)
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	f.failWith("refresh_token", jsonError(400, ca70043))
	f.failWith("password", jsonError(400, `{"error":"access_denied","error_description":"AADSTS53003: Access has been blocked by Conditional Access policies.","error_codes":[53003]}`))
	_, err := a.Token(ctx)
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "AADSTS53003") {
		t.Fatalf("got %v", err)
	}
}

func TestPasswordMissingFromVault(t *testing.T) {
	ctx, _ := withGate(t)
	f := newFakeAAD(t)
	a, v := passwordSetup(t, f)
	f.failWith("refresh_token", jsonError(400, ca70043))
	if err := v.Delete(vault.SecretKey("main")); err != nil {
		t.Fatal(err)
	}
	before := f.grants("password")
	_, err := a.Token(ctx)
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "no stored password") {
		t.Fatalf("got %v", err)
	}
	if f.grants("password") != before {
		t.Fatal("password grant attempted without a password")
	}
}

func TestDeviceCodeDoesNotSignInAgain(t *testing.T) {
	f := newFakeAAD(t)
	f.expiresIn = 60
	v := testVaultWithKey(t, t.TempDir(), mustKey(t))
	tok := loginDelegated(t, f, v)
	acct := fakeAcct(config.AuthDeviceCode)
	acct.HomeAccountID = tok.HomeAccountID
	a, _ := NewWithOptions(acct, v, f.opts())
	f.failWith("refresh_token", jsonError(400, ca70043))
	if _, err := a.Token(ctx5(t)); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("got %v", err)
	}
	if f.grants("password") != 0 {
		t.Fatal("password grant for a device code account")
	}
}

func TestPasswordIsNotUsedWithoutTheGate(t *testing.T) {
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	f.failWith("refresh_token", jsonError(400, ca70043))
	before := f.grants("password")
	if _, err := a.Token(ctx5(t)); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("got %v", err)
	}
	if f.grants("password") != before {
		t.Fatal("doctor, test and GUI paths must never sign in with the password")
	}
}

func TestPasswordCredentialFailureIsReportedToTheGate(t *testing.T) {
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	ctx, g := withGate(t)
	f.failWith("refresh_token", jsonError(400, ca70043))
	f.failWith("password", jsonError(400, `{"error":"invalid_grant","error_description":"AADSTS50126: Error validating credentials due to invalid username or password.","error_codes":[50126]}`))
	if _, err := a.Token(ctx); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("got %v", err)
	}
	if len(g.failed) != 1 || g.failed[0] != "AADSTS50126" {
		t.Fatalf("gate saw %v", g.failed)
	}
	// Blocked: no further password request, and the error carries the code for the hint.
	before := f.grants("password")
	_, err := a.Token(ctx)
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "AADSTS50126") {
		t.Fatalf("got %v", err)
	}
	if f.grants("password") != before {
		t.Fatal("password tried again while blocked")
	}
}

func TestPasswordIntervalKeepsTheSilentError(t *testing.T) {
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	ctx, g := withGate(t)
	g.allow = false // too soon after the last attempt
	f.failWith("refresh_token", jsonError(400, ca70043))
	before := f.grants("password")
	_, err := a.Token(ctx)
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "AADSTS70043") {
		t.Fatalf("got %v", err)
	}
	if f.grants("password") != before {
		t.Fatal("password tried inside the interval")
	}
}

func TestPasswordNeverLoggedInIsNotSignedInAutomatically(t *testing.T) {
	f := newFakeAAD(t)
	v := testVaultWithKey(t, t.TempDir(), mustKey(t))
	if err := v.Put(vault.SecretKey("main"), []byte("hunter2")); err != nil {
		t.Fatal(err)
	}
	a, _ := NewWithOptions(passwordAcct(""), v, f.opts())
	ctx, g := withGate(t)
	_, err := a.Token(ctx)
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "has not signed in yet") {
		t.Fatalf("got %v", err)
	}
	if f.grants("password") != 0 || g.allows != 0 {
		t.Fatal("signed in without an explicit login")
	}
}

func TestPasswordSignInAfterLostHomeAccountIDReturnsIt(t *testing.T) {
	f := newFakeAAD(t)
	_, v := passwordSetup(t, f)
	// The sign-in cache exists but the config lost the id.
	a, _ := NewWithOptions(passwordAcct(""), v, f.opts())
	ctx, _ := withGate(t)
	tok, err := a.Token(ctx)
	if err != nil || tok.HomeAccountID != "uid1.tid1" {
		t.Fatalf("token = %+v, err %v", tok, err)
	}
}

func TestCredentialFailureCodes(t *testing.T) {
	for _, c := range []string{"50126", "50053", "50055", "50057", "50076", "50079", "53003", "65001"} {
		if got := CredentialFailure(errors.New("x AADSTS" + c + ": y")); got != "AADSTS"+c {
			t.Errorf("%s: %q", c, got)
		}
	}
	for _, c := range []string{"70043", "700082", "50173"} {
		if got := CredentialFailure(errors.New("AADSTS" + c)); got != "" {
			t.Errorf("%s counted as credential failure", c)
		}
	}
}

func TestPasswordInsideIntervalReportsTheLastRealCause(t *testing.T) {
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	ctx, g := withGate(t)
	g.allow, g.last = false, "AADSTS50055"
	f.failWith("refresh_token", jsonError(400, ca70043))
	_, err := a.Token(ctx)
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "AADSTS50055") || strings.Contains(err.Error(), "AADSTS70043") {
		t.Fatalf("got %v", err)
	}
}

func TestPasswordPausedAfterMissingConsentKeepsTheConsentClass(t *testing.T) {
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	ctx, g := withGate(t)
	g.blocked = "AADSTS65001"
	f.failWith("refresh_token", jsonError(400, ca70043))
	_, err := a.Token(ctx)
	if !errors.Is(err, ErrConsentRequired) || errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "AADSTS65001") {
		t.Fatalf("got %v", err)
	}
}

func TestConditionalAccessBlockIsNotLoginRequired(t *testing.T) {
	err := Classify(errors.New(`{"error":"access_denied","error_description":"AADSTS53003: Access has been blocked by Conditional Access policies.","error_codes":[53003]}`))
	if errors.Is(err, ErrLoginRequired) {
		t.Fatalf("a Conditional Access block cannot be fixed by signing in: %v", err)
	}
}
