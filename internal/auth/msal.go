package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"

	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/vault"
)

var scopes = []string{GraphScope}

// delegated signs in as a user: device code, browser or password.
type delegated struct {
	acct  config.Account
	store Store
	opts  Options

	mu     sync.Mutex
	client *public.Client
}

func (d *delegated) msal() (public.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client != nil {
		return *d.client, nil
	}
	c, err := public.New(d.acct.ClientID,
		public.WithAuthority(d.opts.authority(d.acct.Tenant)),
		public.WithCache(newVaultCache(d.store, d.acct.Name)),
		public.WithHTTPClient(d.opts.HTTPClient),
		public.WithInstanceDiscovery(!d.opts.DisableInstanceDiscovery),
	)
	if err != nil {
		return public.Client{}, fmt.Errorf("account %q: create MSAL client: %w", d.acct.Name, err)
	}
	d.client = &c
	return c, nil
}

func (d *delegated) Token(ctx context.Context) (Token, error) {
	if d.acct.HomeAccountID == "" {
		return Token{}, fmt.Errorf("%w: account %q has not signed in yet", ErrLoginRequired, d.acct.Name)
	}
	c, err := d.msal()
	if err != nil {
		return Token{}, err
	}
	accounts, err := c.Accounts(ctx)
	if err != nil {
		return Token{}, d.wrap(err)
	}
	for _, a := range accounts {
		if a.HomeAccountID != d.acct.HomeAccountID {
			continue
		}
		opts := []public.AcquireSilentOption{public.WithSilentAccount(a)}
		if Forced(ctx) {
			opts = append(opts, public.WithClaims(forceClaims))
		}
		res, err := c.AcquireTokenSilent(ctx, scopes, opts...)
		if err != nil {
			return Token{}, d.wrap(err)
		}
		return fromResult(res.AccessToken, res.ExpiresOn, res.Account.PreferredUsername, res.Account.HomeAccountID, d.acct), nil
	}
	return Token{}, fmt.Errorf("%w: no cached sign-in for account %q", ErrLoginRequired, d.acct.Name)
}

func (d *delegated) Login(ctx context.Context, ui LoginUI) (Token, error) {
	ctx = withRecovery(ctx)
	c, err := d.msal()
	if err != nil {
		return Token{}, err
	}
	var res public.AuthResult
	switch d.acct.Auth {
	case config.AuthDeviceCode:
		dc, derr := c.AcquireTokenByDeviceCode(ctx, scopes)
		if derr != nil {
			return Token{}, d.wrap(derr)
		}
		ui.DeviceCode(dc.Result.Message)
		res, err = dc.AuthenticationResult(ctx)
	case config.AuthBrowser:
		opts := []public.AcquireInteractiveOption{
			public.WithRedirectURI("http://localhost"),
			public.WithOpenURL(ui.OpenBrowser),
		}
		if d.acct.Username != "" {
			opts = append(opts, public.WithLoginHint(d.acct.Username))
		}
		res, err = c.AcquireTokenInteractive(ctx, scopes, opts...)
	case config.AuthPassword:
		pw, gerr := d.store.Get(vault.SecretKey(d.acct.Name))
		if gerr != nil {
			return Token{}, fmt.Errorf("account %q: read password from vault: %w", d.acct.Name, gerr)
		}
		res, err = c.AcquireTokenByUsernamePassword(ctx, scopes, d.acct.Username, string(pw))
	}
	if err != nil {
		return Token{}, d.wrap(err)
	}
	return fromResult(res.AccessToken, res.ExpiresOn, res.Account.PreferredUsername, res.Account.HomeAccountID, d.acct), nil
}

func (d *delegated) Logout(context.Context) error {
	return newVaultCache(d.store, d.acct.Name).clear()
}

func (d *delegated) wrap(err error) error {
	return wrapErr(d.acct.Name, err)
}

// application signs in as the app registration with a client secret.
type application struct {
	acct  config.Account
	store Store
	opts  Options

	mu     sync.Mutex
	client *confidential.Client
}

func (a *application) msal() (confidential.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		return *a.client, nil
	}
	secret, err := a.store.Get(vault.SecretKey(a.acct.Name))
	if errors.Is(err, vault.ErrNotFound) {
		return confidential.Client{}, fmt.Errorf("%w: no client secret stored for account %q", ErrInvalidClient, a.acct.Name)
	}
	if err != nil {
		return confidential.Client{}, fmt.Errorf("account %q: read client secret from vault: %w", a.acct.Name, err)
	}
	cred, err := confidential.NewCredFromSecret(string(secret))
	if err != nil {
		return confidential.Client{}, fmt.Errorf("%w: account %q: %v", ErrInvalidClient, a.acct.Name, err)
	}
	c, err := confidential.New(a.opts.authority(a.acct.Tenant), a.acct.ClientID, cred,
		confidential.WithCache(newVaultCache(a.store, a.acct.Name)),
		confidential.WithHTTPClient(a.opts.HTTPClient),
		confidential.WithInstanceDiscovery(!a.opts.DisableInstanceDiscovery),
	)
	if err != nil {
		return confidential.Client{}, fmt.Errorf("account %q: create MSAL client: %w", a.acct.Name, err)
	}
	a.client = &c
	return c, nil
}

// Token exchanges the secret for a token; msal serves it from its cache while valid.
func (a *application) Token(ctx context.Context) (Token, error) {
	// The cache only saves a round trip here, so an unreadable one is rebuilt.
	ctx = withRecovery(ctx)
	c, err := a.msal()
	if err != nil {
		return Token{}, err
	}
	var opts []confidential.AcquireByCredentialOption
	if Forced(ctx) {
		opts = append(opts, confidential.WithClaims(forceClaims))
	}
	res, err := c.AcquireTokenByCredential(ctx, scopes, opts...)
	if err != nil {
		return Token{}, wrapErr(a.acct.Name, err)
	}
	return fromResult(res.AccessToken, res.ExpiresOn, "", "", a.acct), nil
}

// Login verifies the secret by requesting a token.
func (a *application) Login(ctx context.Context, _ LoginUI) (Token, error) {
	return a.Token(ctx)
}

func (a *application) Logout(context.Context) error {
	return newVaultCache(a.store, a.acct.Name).clear()
}

func wrapErr(name string, err error) error {
	err = Classify(err)
	if errors.Is(err, ErrLoginRequired) || errors.Is(err, ErrInvalidClient) ||
		errors.Is(err, ErrConsentRequired) || errors.Is(err, ErrTransient) {
		return err
	}
	return fmt.Errorf("account %q: %w", name, err)
}

func fromResult(accessToken string, expires time.Time, user, homeID string, acct config.Account) Token {
	display := user
	if display == "" {
		if c, ok := parseClaims(accessToken); ok && acct.Auth == config.AuthClientSecret {
			display = c.AppID
			if display == "" {
				display = c.AZP
			}
		}
	}
	if display == "" {
		display = acct.ClientID
	}
	return Token{
		AccessToken:   accessToken,
		ExpiresOn:     expires,
		Scopes:        parseScopes(accessToken),
		Account:       display,
		HomeAccountID: homeID,
	}
}
