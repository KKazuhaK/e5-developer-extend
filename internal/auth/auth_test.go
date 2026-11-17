package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	msalerrors "github.com/AzureAD/microsoft-authentication-library-for-go/apps/errors"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

type memStore struct{ m map[string][]byte }

func newMem() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) Get(k string) ([]byte, error) {
	v, ok := s.m[k]
	if !ok {
		return nil, fmt.Errorf("%w: %s", vault.ErrNotFound, k)
	}
	return v, nil
}
func (s *memStore) Put(k string, v []byte) error { s.m[k] = v; return nil }
func (s *memStore) Delete(k string) error {
	if _, ok := s.m[k]; !ok {
		return vault.ErrNotFound
	}
	delete(s.m, k)
	return nil
}

func jwt(payload string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(payload)) + ".sig"
}

func TestParseScopes(t *testing.T) {
	tests := []struct {
		name string
		jwt  string
		want []string
	}{
		{"delegated", jwt(`{"scp":"User.Read Mail.Read  Files.Read"}`), []string{"User.Read", "Mail.Read", "Files.Read"}},
		{"application", jwt(`{"roles":["Mail.Read","User.Read.All"]}`), []string{"Mail.Read", "User.Read.All"}},
		{"scp wins", jwt(`{"scp":"A","roles":["B"]}`), []string{"A"}},
		{"none", jwt(`{}`), nil},
		{"not a jwt", "abc", nil},
		{"bad base64", "a.!!!.c", nil},
		{"bad json", "a." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".c", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseScopes(tt.jwt)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func callErr(status int, body string) error {
	return msalerrors.CallErr{
		Resp: &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))},
		Err:  fmt.Errorf("http call error: reply status code was %d:\n%s", status, body),
	}
}

func TestClassify(t *testing.T) {
	aad := func(code, kind string) string {
		return fmt.Sprintf(`{"error":%q,"error_description":"AADSTS%s: Something happened.\r\nTrace ID: abc"}`, kind, code)
	}
	tests := []struct {
		name string
		err  error
		want error // nil: unchanged
	}{
		{"consent", callErr(400, aad("65001", "invalid_grant")), ErrConsentRequired},
		{"secret wrong", callErr(401, aad("7000215", "invalid_client")), ErrInvalidClient},
		{"secret expired", callErr(401, aad("7000222", "invalid_client")), ErrInvalidClient},
		{"app missing", callErr(400, aad("700016", "unauthorized_client")), ErrInvalidClient},
		{"password changed", callErr(400, aad("50173", "invalid_grant")), ErrLoginRequired},
		{"refresh expired", callErr(400, aad("700082", "invalid_grant")), ErrLoginRequired},
		{"mfa", callErr(400, aad("50076", "interaction_required")), ErrLoginRequired},
		{"mfa enroll", callErr(400, aad("50079", "interaction_required")), ErrLoginRequired},
		{"bad password", callErr(400, aad("50126", "invalid_grant")), ErrLoginRequired},
		{"code from error_codes", callErr(400, `{"error":"invalid_grant","error_description":"mentions AADSTS65001 in passing","error_codes":[50173]}`), ErrLoginRequired},
		{"plain invalid_grant", callErr(400, `{"error":"invalid_grant"}`), ErrLoginRequired},
		{"interaction", errors.New("interaction_required: need UI"), ErrLoginRequired},
		{"empty cache", errors.New("no token found"), ErrLoginRequired},
		{"429", callErr(429, "slow down"), ErrTransient},
		{"503", callErr(503, "oops"), ErrTransient},
		{"5xx beats body", callErr(500, aad("50173", "invalid_grant")), ErrTransient},
		{"deadline", fmt.Errorf("wrap: %w", context.DeadlineExceeded), ErrTransient},
		{"net error", &net.OpError{Op: "dial", Err: errors.New("refused")}, ErrTransient},
		{"url error", &url.Error{Op: "Post", URL: "https://x", Err: errors.New("EOF")}, ErrTransient},
		{"canceled", context.Canceled, nil},
		{"other 400", callErr(400, `{"error":"invalid_request"}`), nil},
		{"other", errors.New("something else"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.err)
			if tt.want == nil {
				if got != tt.err {
					t.Fatalf("expected unchanged error, got %v", got)
				}
				return
			}
			if !errors.Is(got, tt.want) {
				t.Fatalf("got %v, want class %v", got, tt.want)
			}
			for _, other := range []error{ErrLoginRequired, ErrInvalidClient, ErrConsentRequired, ErrTransient} {
				if other != tt.want && errors.Is(got, other) {
					t.Fatalf("also matches %v", other)
				}
			}
		})
	}
}

func TestClassifyMessageIsConcise(t *testing.T) {
	err := Classify(callErr(400, `{"error":"invalid_grant","error_description":"AADSTS50173: The grant expired.\r\nTrace ID: abc"}`))
	if got := err.Error(); got != "login required: AADSTS50173: The grant expired." {
		t.Fatalf("message = %q", got)
	}
	var ce msalerrors.CallErr
	if !errors.As(err, &ce) {
		t.Fatal("original CallErr not reachable")
	}
	if Classify(nil) != nil {
		t.Fatal("nil")
	}
	again := Classify(err)
	if again != err {
		t.Fatal("already classified errors must pass through")
	}
}

type fakeCache struct{ data []byte }

func (f *fakeCache) Marshal() ([]byte, error) { return f.data, nil }
func (f *fakeCache) Unmarshal(b []byte) error { f.data = append([]byte(nil), b...); return nil }

func TestVaultCacheRoundTrip(t *testing.T) {
	store := newMem()
	c := newVaultCache(store, "main")
	ctx := context.Background()

	empty := &fakeCache{}
	if err := c.Replace(ctx, empty, cache.ReplaceHints{}); err != nil || string(empty.data) != "{}" {
		t.Fatalf("empty replace: %v %v", err, empty.data)
	}
	if err := c.Export(ctx, &fakeCache{data: []byte(`{"AccessToken":{}}`)}, cache.ExportHints{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.m["tokencache/main"]; !ok {
		t.Fatal("cache not stored under tokencache/<account>")
	}
	got := &fakeCache{}
	if err := c.Replace(ctx, got, cache.ReplaceHints{}); err != nil || string(got.data) != `{"AccessToken":{}}` {
		t.Fatalf("replace: %v %q", err, got.data)
	}
	if err := c.clear(); err != nil {
		t.Fatal(err)
	}
	if err := c.clear(); err != nil {
		t.Fatalf("clearing twice: %v", err)
	}
}

func TestVaultCacheWithRealVault(t *testing.T) {
	key, _ := vault.GenerateKey()
	v := vault.New(t.TempDir(), func() ([]byte, error) { return key, nil })
	c := newVaultCache(v, "acct")
	if err := c.Export(context.Background(), &fakeCache{data: []byte("x")}, cache.ExportHints{}); err != nil {
		t.Fatal(err)
	}
	got := &fakeCache{}
	if err := c.Replace(context.Background(), got, cache.ReplaceHints{}); err != nil || string(got.data) != "x" {
		t.Fatalf("%v %q", err, got.data)
	}
}

func acct(mode string) config.Account {
	return config.Account{Name: "main", Auth: mode, Tenant: "contoso.onmicrosoft.com", ClientID: "00000000-0000-0000-0000-000000000001"}
}

func TestNew(t *testing.T) {
	for _, m := range []string{config.AuthDeviceCode, config.AuthBrowser, config.AuthPassword, config.AuthClientSecret} {
		if _, err := New(acct(m), newMem(), nil); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
	if _, err := New(acct("x"), newMem(), nil); err == nil {
		t.Error("unsupported mode accepted")
	}
	if _, err := New(config.Account{Auth: config.AuthDeviceCode}, newMem(), nil); err == nil {
		t.Error("nameless account accepted")
	}
}

func TestTokenWithoutLoginIsLoginRequired(t *testing.T) {
	a, _ := New(acct(config.AuthDeviceCode), newMem(), nil)
	_, err := a.Token(context.Background())
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("got %v", err)
	}

	// Home account recorded but nothing cached: still login required, no network.
	ac := acct(config.AuthBrowser)
	ac.HomeAccountID = "uid.utid"
	a, _ = New(ac, newMem(), nil)
	if _, err := a.Token(context.Background()); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("got %v", err)
	}
}

func TestClientSecretWithoutSecret(t *testing.T) {
	a, _ := New(acct(config.AuthClientSecret), newMem(), nil)
	if _, err := a.Token(context.Background()); !errors.Is(err, ErrInvalidClient) {
		t.Fatalf("got %v", err)
	}
}

func TestLogoutClearsCache(t *testing.T) {
	store := newMem()
	store.m["tokencache/main"] = []byte("c")
	a, _ := New(acct(config.AuthDeviceCode), store, nil)
	if err := a.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.m["tokencache/main"]; ok {
		t.Fatal("cache still present")
	}
}

func TestFromResultDisplay(t *testing.T) {
	app := acct(config.AuthClientSecret)
	tok := fromResult(jwt(`{"roles":["Mail.Read"],"appid":"app-1"}`), time.Time{}, "", "", app)
	if tok.Account != "app-1" || len(tok.Scopes) != 1 {
		t.Fatalf("%+v", tok)
	}
	tok = fromResult(jwt(`{"scp":"User.Read"}`), time.Time{}, "a@b.c", "h", acct(config.AuthDeviceCode))
	if tok.Account != "a@b.c" || tok.HomeAccountID != "h" {
		t.Fatalf("%+v", tok)
	}
}

// A writer that is not this cache (a backup import, another process) wins:
// Export does not overwrite a value that changed since Replace read it.
func TestVaultCacheExportYieldsToAnotherWriter(t *testing.T) {
	ctx := context.Background()
	hdr := cache.ExportHints{}
	read := func(c *vaultCache) string {
		t.Helper()
		g := &fakeCache{}
		if err := c.Replace(ctx, g, cache.ReplaceHints{}); err != nil {
			t.Fatal(err)
		}
		return string(g.data)
	}

	t.Run("value replaced meanwhile", func(t *testing.T) {
		store := newMem()
		store.m["tokencache/main"] = []byte("old")
		c := newVaultCache(store, "main")
		if read(c) != "old" {
			t.Fatal("did not read the stored value")
		}
		store.m["tokencache/main"] = []byte("imported") // another writer
		if err := c.Export(ctx, &fakeCache{data: []byte("mine")}, hdr); err != nil {
			t.Fatal(err)
		}
		if got := string(store.m["tokencache/main"]); got != "imported" {
			t.Fatalf("the import was overwritten: %q", got)
		}
		if read(c) != "imported" { // the next Replace sees it, and a later Export may write again
			t.Fatal("did not reload")
		}
		if err := c.Export(ctx, &fakeCache{data: []byte("mine2")}, hdr); err != nil {
			t.Fatal(err)
		}
		if got := string(store.m["tokencache/main"]); got != "mine2" {
			t.Fatalf("export after reloading did not write: %q", got)
		}
	})

	t.Run("value appeared where there was none", func(t *testing.T) {
		store := newMem()
		c := newVaultCache(store, "main")
		read(c) // nothing stored
		store.m["tokencache/main"] = []byte("imported")
		_ = c.Export(ctx, &fakeCache{data: []byte("mine")}, hdr)
		if got := string(store.m["tokencache/main"]); got != "imported" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("value removed meanwhile", func(t *testing.T) {
		store := newMem()
		store.m["tokencache/main"] = []byte("old")
		c := newVaultCache(store, "main")
		read(c)
		delete(store.m, "tokencache/main") // e.g. a replace import dropped the account
		_ = c.Export(ctx, &fakeCache{data: []byte("mine")}, hdr)
		if _, ok := store.m["tokencache/main"]; ok {
			t.Fatal("a removed cache came back")
		}
	})

	t.Run("unchanged value is written", func(t *testing.T) {
		store := newMem()
		store.m["tokencache/main"] = []byte("old")
		c := newVaultCache(store, "main")
		read(c)
		if err := c.Export(ctx, &fakeCache{data: []byte("new")}, hdr); err != nil {
			t.Fatal(err)
		}
		if string(store.m["tokencache/main"]) != "new" {
			t.Fatal("not written")
		}
	})
}
