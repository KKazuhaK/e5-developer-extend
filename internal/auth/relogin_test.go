package auth

import (
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
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	f.failWith("refresh_token", jsonError(400, ca70043))
	tok, err := a.Token(ctx5(t))
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

func TestPasswordSignInAgainWithoutCache(t *testing.T) {
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	if err := a.Logout(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Token(ctx5(t)); err != nil {
		t.Fatalf("token: %v", err)
	}
}

func TestPasswordSignInAgainFailsWithMFA(t *testing.T) {
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	f.failWith("refresh_token", jsonError(400, ca70043))
	f.failWith("password", jsonError(400, `{"error":"invalid_grant","error_description":"AADSTS50076: Due to a configuration change, you must use multi-factor authentication.","error_codes":[50076]}`))
	_, err := a.Token(ctx5(t))
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "AADSTS50076") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatal("password leaked into the error")
	}
}

func TestPasswordSignInAgainWithOtherRefusalIsLoginRequired(t *testing.T) {
	f := newFakeAAD(t)
	a, _ := passwordSetup(t, f)
	f.failWith("refresh_token", jsonError(400, ca70043))
	f.failWith("password", jsonError(400, `{"error":"access_denied","error_description":"AADSTS53003: Access has been blocked by Conditional Access policies.","error_codes":[53003]}`))
	_, err := a.Token(ctx5(t))
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "AADSTS53003") {
		t.Fatalf("got %v", err)
	}
}

func TestPasswordMissingFromVault(t *testing.T) {
	f := newFakeAAD(t)
	a, v := passwordSetup(t, f)
	f.failWith("refresh_token", jsonError(400, ca70043))
	if err := v.Delete(vault.SecretKey("main")); err != nil {
		t.Fatal(err)
	}
	before := f.grants("password")
	_, err := a.Token(ctx5(t))
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
