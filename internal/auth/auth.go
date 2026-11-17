// Package auth obtains Microsoft Graph access tokens for configured accounts
// using msal-go.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

// GraphScope requests every permission already consented for the app registration.
const GraphScope = "https://graph.microsoft.com/.default"

// Token is an access token with display metadata.
type Token struct {
	AccessToken   string
	ExpiresOn     time.Time
	Scopes        []string // from the JWT scp (delegated) or roles (application) claim
	Account       string   // UPN or application ID, for display
	TenantID      string   // tid claim of the token, when it has one
	HomeAccountID string   // delegated only; stored back into the config after login
}

// LoginUI lets the caller present interactive login steps.
type LoginUI interface {
	// DeviceCode shows the device code instructions to the user.
	DeviceCode(message string)
	// OpenBrowser opens url in a browser.
	OpenBrowser(url string) error
}

// Authenticator gets tokens for one account.
type Authenticator interface {
	// Token returns a valid access token. It never prompts, and signs a
	// password account in again only when ctx allows it (AllowPasswordFallback).
	Token(ctx context.Context) (Token, error)
	// Login runs the interactive sign-in (device code, browser) or the first
	// verification (client_secret, password).
	Login(ctx context.Context, ui LoginUI) (Token, error)
	// Logout discards the account's token cache.
	Logout(ctx context.Context) error
}

// Store is the secret storage the authenticator needs; *vault.Vault implements it.
type Store interface {
	Get(key string) ([]byte, error)
	Put(key string, value []byte) error
	Delete(key string) error
}

// Options tune how an Authenticator reaches the identity service.
type Options struct {
	// HTTPClient defaults to NewHTTPClient("").
	HTTPClient *http.Client
	// AuthorityHost defaults to https://login.microsoftonline.com; tests point it at a fake.
	AuthorityHost string
	// DisableInstanceDiscovery skips Microsoft's cloud instance discovery; for tests and sovereign clouds.
	DisableInstanceDiscovery bool
}

// New returns an Authenticator for acct. httpClient may be nil, in which
// case NewHTTPClient("") is used.
func New(acct config.Account, store Store, httpClient *http.Client) (Authenticator, error) {
	return NewWithOptions(acct, store, Options{HTTPClient: httpClient})
}

// NewWithOptions is New with explicit Options.
func NewWithOptions(acct config.Account, store Store, opts Options) (Authenticator, error) {
	if opts.HTTPClient == nil {
		opts.HTTPClient = NewHTTPClient("")
	}
	if opts.AuthorityHost == "" {
		opts.AuthorityHost = "https://login.microsoftonline.com"
	}
	if acct.Name == "" {
		return nil, errors.New("auth: account has no name")
	}
	switch acct.Auth {
	case config.AuthDeviceCode, config.AuthBrowser, config.AuthPassword:
		return &delegated{acct: acct, store: store, opts: opts}, nil
	case config.AuthClientSecret:
		return &application{acct: acct, store: store, opts: opts}, nil
	default:
		return nil, fmt.Errorf("auth: account %q has unsupported auth mode %q", acct.Name, acct.Auth)
	}
}

func (o Options) authority(tenant string) string {
	return strings.TrimRight(o.AuthorityHost, "/") + "/" + tenant
}
