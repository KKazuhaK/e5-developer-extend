package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

var scopes = []string{GraphScope}

// delegated signs in as a user: device code, browser or password.
type delegated struct {
	acct  config.Account
	store Store
	opts  Options

	mu     sync.Mutex
	client *public.Client

	reauth sync.Mutex // one password sign-in at a time
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

// Token returns an access token silently. Only when ctx carries a
// PasswordGate (AllowPasswordFallback; the background engine sets it) does a
// password account whose refresh token no longer works (expired, revoked, or
// cut off by a Conditional Access sign-in frequency policy) sign in again with
// its stored password, so a background job keeps running without a person.
// Everyone else, and device code and browser accounts, get ErrLoginRequired.
func (d *delegated) Token(ctx context.Context) (Token, error) {
	tok, err := d.silentToken(ctx)
	if err == nil || d.acct.Auth != config.AuthPassword || !errors.Is(err, ErrLoginRequired) || ctx.Err() != nil {
		return tok, err
	}
	gate := FallbackGate(ctx)
	if gate == nil {
		return tok, err
	}
	return d.passwordSignIn(ctx, gate, err)
}

// passwordSignIn is the non-interactive re-authentication for password
// accounts. The password is read from the vault for this call only and never
// logged. silentErr is what the silent refresh returned; it is the answer
// whenever the password must not be tried. A credential failure blocks further
// attempts through the gate, so a wrong password or a policy that needs MFA is
// not retried until the person has acted.
func (d *delegated) passwordSignIn(ctx context.Context, gate PasswordGate, silentErr error) (Token, error) {
	d.reauth.Lock()
	defer d.reauth.Unlock()
	// Signing in the first time is an explicit step, not something to do on the side.
	if _, err := d.store.Get(vault.TokenCacheKey(d.acct.Name)); err != nil {
		return Token{}, silentErr
	}
	pw, err := d.store.Get(vault.SecretKey(d.acct.Name))
	if errors.Is(err, vault.ErrNotFound) {
		return Token{}, fmt.Errorf("%w: account %q has no stored password to sign in again", ErrLoginRequired, d.acct.Name)
	}
	if err != nil {
		return Token{}, fmt.Errorf("account %q: read password from vault: %w", d.acct.Name, err)
	}
	ok, blocked := gate.Allow()
	if blocked != "" {
		return Token{}, d.pausedErr(blocked, "paused after")
	}
	if !ok {
		// Inside the interval: say what the last password sign-in ran into, which is the real cause.
		if last := gate.LastCode(); last != "" {
			return Token{}, d.pausedErr(last, "last failed with")
		}
		return Token{}, silentErr
	}
	c, err := d.msal()
	if err != nil {
		return Token{}, err
	}
	res, err := c.AcquireTokenByUsernamePassword(withRecovery(ctx), scopes, d.acct.Username, string(pw))
	if err != nil {
		err = d.wrap(err)
		gate.Result(aadCode(err.Error()))
		if code := CredentialFailure(err); code != "" {
			gate.Failed(code)
		}
		if errors.Is(err, ErrLoginRequired) || errors.Is(err, ErrTransient) || errors.Is(err, ErrInvalidClient) || errors.Is(err, ErrConsentRequired) {
			return Token{}, err
		}
		// Any other refusal means the stored password cannot sign in without a person.
		return Token{}, fmt.Errorf("%w: %s", ErrLoginRequired, describe(err.Error()))
	}
	// The same user signs in again, so the home account id stays stable; a
	// different one would mean the stored user name changed, which is a login.
	if d.acct.HomeAccountID != "" && res.Account.HomeAccountID != d.acct.HomeAccountID {
		return Token{}, fmt.Errorf("%w: the password signs in as a different account than the one set up for %q", ErrLoginRequired, d.acct.Name)
	}
	gate.Result("")
	return fromResult(res.AccessToken, res.ExpiresOn, res.Account.PreferredUsername, res.Account.HomeAccountID, d.acct), nil
}

// pausedErr is the error for a password sign-in that is not tried now because
// of an earlier failure with code. It keeps the code in its text so the hint
// for that failure applies, and keeps the consent class for a missing consent.
func (d *delegated) pausedErr(code, what string) error {
	kind := ErrLoginRequired
	if code == "AADSTS65001" {
		kind = ErrConsentRequired
	}
	return fmt.Errorf("%w: automatic password sign-in %s %s; fix the cause, then run: e5extend account set-secret %s (or account login)", kind, what, code, d.acct.Name)
}

func (d *delegated) silentToken(ctx context.Context) (Token, error) {
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
