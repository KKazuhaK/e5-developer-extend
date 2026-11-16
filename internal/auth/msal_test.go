package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"errors"

	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/vault"
)

const (
	fakeTenant = "contoso.test"
	fakeClient = "00000000-0000-0000-0000-0000000000aa"
)

// fakeAAD is a minimal identity service: OpenID metadata, device code and token endpoints.
type fakeAAD struct {
	srv *httptest.Server

	mu        sync.Mutex
	hits      map[string]int // grant type -> token endpoint calls
	total     int            // every request, metadata included
	expiresIn int
	failGrant map[string]func(w http.ResponseWriter)
	roles     bool
}

func newFakeAAD(t *testing.T) *fakeAAD {
	t.Helper()
	f := &fakeAAD{hits: map[string]int{}, expiresIn: 3600, failGrant: map[string]func(http.ResponseWriter){}}
	mux := http.NewServeMux()
	mux.HandleFunc("/"+fakeTenant+"/v2.0/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := f.srv.URL + "/" + fakeTenant
		json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint":        base + "/oauth2/v2.0/authorize",
			"token_endpoint":                base + "/oauth2/v2.0/token",
			"device_authorization_endpoint": base + "/oauth2/v2.0/devicecode",
			"issuer":                        base + "/v2.0",
		})
	})
	mux.HandleFunc("/"+fakeTenant+"/oauth2/v2.0/devicecode", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"user_code": "ABCD", "device_code": "DEV", "verification_uri": "https://example.invalid/device",
			"expires_in": 900, "interval": 1, "message": "enter ABCD at https://example.invalid/device",
		})
	})
	mux.HandleFunc("/"+fakeTenant+"/oauth2/v2.0/token", f.token)
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.total++
		f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAAD) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	grant := r.PostForm.Get("grant_type")
	f.mu.Lock()
	f.hits[grant]++
	fail := f.failGrant[grant]
	expires := f.expiresIn
	roles := f.roles
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if fail != nil {
		fail(w)
		return
	}
	enc := base64.RawURLEncoding.EncodeToString
	var access string
	resp := map[string]any{"token_type": "Bearer", "expires_in": expires, "ext_expires_in": expires}
	if roles {
		access = fakeJWT(`{"roles":["Mail.Read","User.Read.All"],"appid":"` + fakeClient + `"}`)
		resp["scope"] = "https://graph.microsoft.com/.default"
	} else {
		access = fakeJWT(`{"scp":"Mail.Read User.Read"}`)
		resp["scope"] = "https://graph.microsoft.com/.default https://graph.microsoft.com/Mail.Read https://graph.microsoft.com/User.Read openid profile"
		resp["refresh_token"] = "rt-1"
		resp["client_info"] = enc([]byte(`{"uid":"uid1","utid":"tid1"}`))
		resp["id_token"] = fakeJWT(`{"preferred_username":"me@contoso.test","oid":"uid1","tid":"tid1","sub":"s","aud":"` + fakeClient + `","iss":"` + f.srv.URL + `/` + fakeTenant + `/v2.0","name":"Me"}`)
	}
	resp["access_token"] = access
	json.NewEncoder(w).Encode(resp)
}

func fakeJWT(payload string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc([]byte(payload)) + "." + enc([]byte("sig"))
}

func (f *fakeAAD) tokenHits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.hits {
		n += v
	}
	return n
}

func (f *fakeAAD) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total
}

func (f *fakeAAD) failWith(grant string, fn func(w http.ResponseWriter)) {
	f.mu.Lock()
	f.failGrant[grant] = fn
	f.mu.Unlock()
}

func jsonError(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
}

func (f *fakeAAD) opts() Options {
	return Options{HTTPClient: f.srv.Client(), AuthorityHost: f.srv.URL, DisableInstanceDiscovery: true}
}

type testUI struct{ msg string }

func (u *testUI) DeviceCode(m string)      { u.msg = m }
func (u *testUI) OpenBrowser(string) error { return nil }

func testVaultWithKey(t *testing.T, dir string, key []byte) *vault.Vault {
	t.Helper()
	return vault.New(dir, func() ([]byte, error) { return key, nil })
}

func fakeAcct(mode string) config.Account {
	return config.Account{Name: "main", Auth: mode, Tenant: fakeTenant, ClientID: fakeClient}
}

func ctx5(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func loginDelegated(t *testing.T, f *fakeAAD, v *vault.Vault) Token {
	t.Helper()
	a, err := NewWithOptions(fakeAcct(config.AuthDeviceCode), v, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	ui := &testUI{}
	tok, err := a.Login(ctx5(t), ui)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if ui.msg == "" {
		t.Fatal("device code message not shown")
	}
	return tok
}

func TestDelegatedLoginAndCachedToken(t *testing.T) {
	f := newFakeAAD(t)
	dir := t.TempDir()
	key, _ := vault.GenerateKey()
	v := testVaultWithKey(t, dir, key)

	tok := loginDelegated(t, f, v)
	if tok.HomeAccountID != "uid1.tid1" || tok.Account != "me@contoso.test" || len(tok.Scopes) != 2 {
		t.Fatalf("login token = %+v", tok)
	}
	if ok, _ := v.Has("tokencache/main"); !ok {
		t.Fatal("cache not persisted")
	}

	acct := fakeAcct(config.AuthDeviceCode)
	acct.HomeAccountID = tok.HomeAccountID

	// A brand-new Authenticator reads the cache back from the vault.
	a2, _ := NewWithOptions(acct, testVaultWithKey(t, dir, key), f.opts())
	before := f.requests()
	for i := 0; i < 2; i++ {
		got, err := a2.Token(ctx5(t))
		if err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
		if got.AccessToken != tok.AccessToken || got.HomeAccountID != "uid1.tid1" {
			t.Fatalf("token %d = %+v", i, got)
		}
	}
	if d := f.requests() - before; d != 0 {
		t.Fatalf("silent Token() made %d HTTP requests, want 0", d)
	}
}

func TestHomeAccountMismatchIsLoginRequired(t *testing.T) {
	f := newFakeAAD(t)
	v := testVaultWithKey(t, t.TempDir(), mustKey(t))
	loginDelegated(t, f, v)

	acct := fakeAcct(config.AuthDeviceCode)
	acct.HomeAccountID = "someone.else"
	a, _ := NewWithOptions(acct, v, f.opts())
	before := f.tokenHits()
	if _, err := a.Token(ctx5(t)); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("got %v", err)
	}
	if f.tokenHits() != before {
		t.Fatal("unexpected token request")
	}
}

func mustKey(t *testing.T) []byte {
	t.Helper()
	k, err := vault.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRefreshFailures(t *testing.T) {
	const refresh = "refresh_token"
	tests := []struct {
		name string
		fail func(f *fakeAAD)
		want error
	}{
		{"invalid_grant", func(f *fakeAAD) {
			f.failWith(refresh, jsonError(400, `{"error":"invalid_grant","error_description":"AADSTS700082: The refresh token has expired due to inactivity.","error_codes":[700082]}`))
		}, ErrLoginRequired},
		{"server error", func(f *fakeAAD) { f.failWith(refresh, jsonError(503, `unavailable`)) }, ErrTransient},
		{"throttled", func(f *fakeAAD) { f.failWith(refresh, jsonError(429, `{"error":"temporarily_unavailable"}`)) }, ErrTransient},
		{"connection refused", func(f *fakeAAD) { f.srv.Close() }, ErrTransient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAAD(t)
			f.expiresIn = 60 // inside msal's refresh window, so Token() must refresh
			v := testVaultWithKey(t, t.TempDir(), mustKey(t))
			tok := loginDelegated(t, f, v)

			acct := fakeAcct(config.AuthDeviceCode)
			acct.HomeAccountID = tok.HomeAccountID
			a, _ := NewWithOptions(acct, v, f.opts())
			tt.fail(f)
			_, err := a.Token(ctx5(t))
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestLogoutDropsInMemoryCache(t *testing.T) {
	f := newFakeAAD(t)
	v := testVaultWithKey(t, t.TempDir(), mustKey(t))
	a, _ := NewWithOptions(fakeAcct(config.AuthDeviceCode), v, f.opts())
	tok, err := a.Login(ctx5(t), &testUI{})
	if err != nil {
		t.Fatal(err)
	}
	// Same instance: its in-memory cache must not resurrect the removed one.
	acct := fakeAcct(config.AuthDeviceCode)
	acct.HomeAccountID = tok.HomeAccountID
	same, _ := NewWithOptions(acct, v, f.opts())
	if _, err := same.Token(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	if err := same.Logout(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	before := f.requests()
	if _, err := same.Token(ctx5(t)); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("got %v", err)
	}
	if f.requests() != before {
		t.Fatal("network used after logout")
	}
}

func TestUnreadableCacheRecovery(t *testing.T) {
	f := newFakeAAD(t)
	dir := t.TempDir()
	v1 := testVaultWithKey(t, dir, mustKey(t))
	tok := loginDelegated(t, f, v1)

	// Same data, different master key: the cache cannot be decrypted.
	v2 := testVaultWithKey(t, dir, mustKey(t))
	acct := fakeAcct(config.AuthDeviceCode)
	acct.HomeAccountID = tok.HomeAccountID
	a, _ := NewWithOptions(acct, v2, f.opts())
	_, err := a.Token(ctx5(t))
	if !errors.Is(err, ErrLoginRequired) || !errors.Is(err, vault.ErrWrongKey) {
		t.Fatalf("Token with unreadable cache: %v", err)
	}
	// Login discards the bad blob and succeeds.
	if _, err := a.Login(ctx5(t), &testUI{}); err != nil {
		t.Fatalf("login must recover: %v", err)
	}
	if _, err := a.Token(ctx5(t)); err != nil {
		t.Fatalf("token after recovery: %v", err)
	}
}

func TestClientSecretFlow(t *testing.T) {
	f := newFakeAAD(t)
	f.roles = true
	v := testVaultWithKey(t, t.TempDir(), mustKey(t))
	if err := v.Put(vault.SecretKey("main"), []byte("s3cret")); err != nil {
		t.Fatal(err)
	}
	a, _ := NewWithOptions(fakeAcct(config.AuthClientSecret), v, f.opts())
	tok, err := a.Login(ctx5(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Account != fakeClient || len(tok.Scopes) != 2 || tok.HomeAccountID != "" {
		t.Fatalf("token = %+v", tok)
	}
	before := f.tokenHits()
	if _, err := a.Token(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	if f.tokenHits() != before {
		t.Fatal("second Token() should come from cache")
	}

	// Wrong secret.
	f2 := newFakeAAD(t)
	f2.failWith("client_credentials", jsonError(401, `{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret provided.","error_codes":[7000215]}`))
	b, _ := NewWithOptions(fakeAcct(config.AuthClientSecret), v, f2.opts())
	if _, err := b.Token(ctx5(t)); !errors.Is(err, ErrInvalidClient) {
		t.Fatalf("got %v", err)
	}
}
