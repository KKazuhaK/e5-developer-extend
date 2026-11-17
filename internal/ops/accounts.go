package ops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// AccountInput describes an account for adding and editing. Secret is only
// read; no operation ever returns it.
type AccountInput struct {
	Name          string   `json:"name"`
	Auth          string   `json:"auth"`
	Tenant        string   `json:"tenant"`
	ClientID      string   `json:"clientId"`
	Username      string   `json:"username"`
	Categories    []string `json:"categories"`
	SecretExpires string   `json:"secretExpires"`
	Secret        string   `json:"secret,omitempty"`
	// UseStoredSecret takes the secret already in the vault under this
	// account's name (put there by automatic app setup) instead of Secret.
	UseStoredSecret bool `json:"useStoredSecret,omitempty"`
}

// Account converts the input into a config entry (without the secret).
func (in AccountInput) Account() config.Account {
	return config.Account{
		Name: in.Name, Auth: in.Auth, Tenant: in.Tenant, ClientID: in.ClientID, Username: in.Username,
		Categories: in.Categories, SecretExpires: in.SecretExpires,
	}
}

// InputOf is the inverse of Account; it never carries a secret.
func InputOf(a config.Account) AccountInput {
	return AccountInput{
		Name: a.Name, Auth: a.Auth, Tenant: a.Tenant, ClientID: a.ClientID, Username: a.Username,
		Categories: nonNil(a.Categories), SecretExpires: a.SecretExpires,
	}
}

// ValidateAccount checks an account on its own, so a bad value fails before
// any prompt. Problems are listed one per line.
func ValidateAccount(acct config.Account) error {
	probe := config.Default()
	probe.Accounts = []config.Account{acct}
	if err := probe.Validate(); err != nil {
		// The probe config holds only this account; drop its list index from the messages.
		return fmt.Errorf("invalid account:\n%s", strings.ReplaceAll(err.Error(), "accounts[0].", ""))
	}
	return nil
}

func checkCategories(cats []string) error {
	if len(cats) == 0 {
		return nil
	}
	apis, err := catalog.Load()
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, a := range apis {
		known[strings.ToLower(a.Category)] = true
	}
	for _, c := range cats {
		if !known[strings.ToLower(c)] {
			return fmt.Errorf("unknown API category %q", c)
		}
	}
	return nil
}

// CheckAccount returns an error when no account has this name.
func (o *Ops) CheckAccount(name string) error {
	cfg, _, err := o.LoadConfig()
	if err != nil {
		return err
	}
	_, err = o.FindAccount(cfg, name)
	return err
}

// AddAccount adds an account and stores its secret, when it needs one, in the vault.
func (o *Ops) AddAccount(ctx context.Context, in AccountInput) error {
	acct := in.Account()
	if err := ValidateAccount(acct); err != nil {
		return err
	}
	if err := checkCategories(acct.Categories); err != nil {
		return err
	}
	if acct.NeedsSecret() && in.Secret == "" && !in.UseStoredSecret {
		return errors.New("the secret must not be empty")
	}

	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, path, err := o.LoadConfigOrDefault()
	if err != nil {
		return err
	}
	if _, exists := cfg.Find(acct.Name); exists {
		return fmt.Errorf("account %q already exists", acct.Name)
	}
	// Everything stored for the account hangs on its ID, so a name can be reused
	// after a removal, and changed, without inheriting or losing anything.
	acct.ID = config.NewAccountID(cfg)
	cfg.Accounts = append(cfg.Accounts, acct)
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid account:\n%w", err)
	}

	var v *vault.Vault
	var pending string // the vault entry an app setup made under the bare name, moved to the ID
	if acct.NeedsSecret() && in.Secret == "" {
		// UseStoredSecret: an app setup stored the secret under the name, before
		// the account (and its ID) existed.
		if v, err = o.OpenVault(cfg); err != nil {
			return err
		}
		pending = vault.SecretKey(acct.Name)
		secret, err := v.Get(pending)
		if errors.Is(err, vault.ErrNotFound) {
			return fmt.Errorf("no secret is stored for account %q", acct.Name)
		} else if err != nil {
			return WithHint(fmt.Errorf("read the stored secret: %w", err), acct.Name)
		}
		if err := v.Put(vault.SecretKey(acct.ID), secret); err != nil {
			return WithHint(fmt.Errorf("store secret: %w", err), acct.Name)
		}
	} else if acct.NeedsSecret() {
		if v, err = o.OpenVault(cfg); err != nil {
			return err
		}
		if err := v.Put(vault.SecretKey(acct.ID), []byte(in.Secret)); err != nil {
			return WithHint(fmt.Errorf("store secret: %w", err), acct.Name)
		}
	}
	if err := cfg.Save(path); err != nil {
		if v != nil {
			_ = v.Delete(vault.SecretKey(acct.ID))
		}
		return err
	}
	if pending != "" {
		_ = v.Delete(pending) // best effort: nothing reads it any more
	}
	return nil
}

// GetAccount returns the editable fields of an account. There is no secret in it.
func (o *Ops) GetAccount(name string) (AccountInput, error) {
	cfg, _, err := o.LoadConfig()
	if err != nil {
		return AccountInput{}, err
	}
	acct, err := o.FindAccount(cfg, name)
	if err != nil {
		return AccountInput{}, err
	}
	return InputOf(*acct), nil
}

// UpdateAccount changes an existing account. The name cannot change. When the
// sign-in identity changes (auth mode, tenant, client or user) the saved
// sign-in is discarded and the account needs a new login. A non-empty Secret
// replaces the stored one.
func (o *Ops) UpdateAccount(ctx context.Context, in AccountInput) error {
	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, path, err := o.LoadConfig()
	if err != nil {
		return err
	}
	cur, err := o.FindAccount(cfg, in.Name)
	if err != nil {
		return err
	}
	next := in.Account()
	next.ID = cur.ID // the name is changed by RenameAccount only
	next.Paused, next.HomeAccountID = cur.Paused, cur.HomeAccountID
	next.SubscriptionExpires = cur.SubscriptionExpires // tenant-level; set with SetSubscriptionExpires
	identityChanged := cur.Auth != next.Auth || cur.Tenant != next.Tenant ||
		cur.ClientID != next.ClientID || cur.Username != next.Username
	if identityChanged {
		next.HomeAccountID = ""
	}
	if err := checkCategories(next.Categories); err != nil {
		return err
	}
	old := *cur
	*cur = next
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid account:\n%w", err)
	}

	v, err := o.OpenVault(cfg)
	if err != nil {
		return err
	}
	secretKey := vault.SecretKey(next.Key())
	switch {
	case next.NeedsSecret() && in.Secret != "":
		if err := v.Put(secretKey, []byte(in.Secret)); err != nil {
			return WithHint(fmt.Errorf("store secret: %w", err), next.Name)
		}
	case next.NeedsSecret() && !old.NeedsSecret():
		return errors.New("the new sign-in method needs a secret; enter one")
	}
	if err := cfg.Save(path); err != nil {
		return err
	}
	if !next.NeedsSecret() && old.NeedsSecret() {
		if err := v.Delete(secretKey); err != nil && !errors.Is(err, vault.ErrNotFound) {
			return err
		}
	}
	if identityChanged {
		if err := v.Delete(vault.TokenCacheKey(next.Key())); err != nil && !errors.Is(err, vault.ErrNotFound) {
			return err
		}
	}
	if identityChanged || in.Secret != "" {
		o.resetPasswordBlock(next.Key())
	}
	return nil
}

// PrepareSetSecret runs the checks of SetSecret that do not need the new
// secret, so a caller can fail before it prompts for one.
func (o *Ops) PrepareSetSecret(name, expires string) (config.Account, error) {
	cfg, _, err := o.LoadConfig()
	if err != nil {
		return config.Account{}, err
	}
	acct, err := o.FindAccount(cfg, name)
	if err != nil {
		return config.Account{}, err
	}
	if !acct.NeedsSecret() {
		return *acct, fmt.Errorf("account %q uses %s and has no stored secret", name, acct.Auth)
	}
	if expires != "" {
		if _, err := time.Parse("2006-01-02", expires); err != nil {
			return *acct, fmt.Errorf("--secret-expires: not a YYYY-MM-DD date: %q", expires)
		}
	}
	return *acct, nil
}

// SetSecret replaces the stored secret or password of an account and, when
// expires is not empty, its expiry date.
func (o *Ops) SetSecret(ctx context.Context, name, secret, expires string) error {
	acct, err := o.PrepareSetSecret(name, expires)
	if err != nil {
		return err
	}
	if secret == "" {
		return errors.New("the secret must not be empty")
	}
	cfg, _, err := o.LoadConfig()
	if err != nil {
		return err
	}
	v, err := o.OpenVault(cfg)
	if err != nil {
		return err
	}
	if err := v.Put(vault.SecretKey(acct.Key()), []byte(secret)); err != nil {
		return WithHint(fmt.Errorf("store secret: %w", err), name)
	}
	o.resetPasswordBlock(acct.Key())
	if expires != "" {
		return o.updateAccountKey(ctx, acct.Key(), func(ac *config.Account) { ac.SecretExpires = expires })
	}
	return nil
}

// resetPasswordBlock lets the engine try automatic password sign-in again for
// an account (given by its key) whose password or identity changed, or that a
// person signed in.
func (o *Ops) resetPasswordBlock(key string) {
	if dir, err := paths.DataDir(); err == nil {
		_ = engine.TriggerPasswordReset(dir, key)
	}
}

// updateAccount reloads the config under the lock, applies change to the
// account with the given name and saves. It is a no-op if there is none.
func (o *Ops) updateAccount(ctx context.Context, name string, change func(*config.Account)) error {
	return o.updateWith(ctx, func(c *config.Config) (*config.Account, bool) { return c.Find(name) }, change)
}

// updateAccountKey is updateAccount for the account with the given key; a
// rename in between does not matter.
func (o *Ops) updateAccountKey(ctx context.Context, key string, change func(*config.Account)) error {
	return o.updateWith(ctx, func(c *config.Config) (*config.Account, bool) { return c.FindKey(key) }, change)
}

func (o *Ops) updateWith(ctx context.Context, find func(*config.Config) (*config.Account, bool), change func(*config.Account)) error {
	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, path, err := o.LoadConfig()
	if err != nil {
		return err
	}
	acct, ok := find(cfg)
	if !ok {
		return nil
	}
	change(acct)
	return cfg.Save(path)
}

// RemoveAccount deletes an account with its stored secret and token cache.
// Failing to delete a vault entry is not fatal; it is returned as a warning.
func (o *Ops) RemoveAccount(ctx context.Context, name string) (warnings []string, err error) {
	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	cfg, _, err := o.LoadConfig()
	if err != nil {
		return nil, err
	}
	path, err := paths.ConfigFile()
	if err != nil {
		return nil, err
	}
	acct, ok := cfg.Find(name)
	if !ok {
		return nil, fmt.Errorf("no account named %q", name)
	}
	key := acct.Key()
	cfg.Remove(name)
	if err := cfg.Save(path); err != nil {
		return nil, err
	}
	v, err := o.OpenVault(cfg)
	if err != nil {
		return nil, err
	}
	for _, ent := range []string{vault.SecretKey(key), vault.TokenCacheKey(key)} {
		if err := v.Delete(ent); err != nil && !errors.Is(err, vault.ErrNotFound) {
			warnings = append(warnings, err.Error())
		}
	}
	return warnings, nil
}

// AccountEntry is one line of the account list.
type AccountEntry struct {
	Account config.Account
	// Status summarizes local state in words; it never touches the network or the master key.
	Status string
	// Credentials is true when the account can sign in without asking
	// again: a stored secret, or a saved sign-in for the other modes.
	Credentials bool
	// SecretExpired is true when the client secret's expiry date has passed.
	SecretExpired bool
}

// ListAccounts lists the accounts and their local sign-in state.
func (o *Ops) ListAccounts() ([]AccountEntry, error) {
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return nil, err
	}
	if len(cfg.Accounts) == 0 {
		return nil, nil
	}
	v, err := o.OpenVault(cfg)
	if err != nil {
		return nil, err
	}
	out := make([]AccountEntry, 0, len(cfg.Accounts))
	for _, acct := range cfg.Accounts {
		out = append(out, LocalEntry(v, acct))
	}
	return out, nil
}

// LocalEntry summarizes the local state of one account from the vault.
func LocalEntry(v interface{ Has(string) (bool, error) }, acct config.Account) AccountEntry {
	var parts []string
	e := AccountEntry{Account: acct}
	has := func(key string) bool { ok, _ := v.Has(key); return ok }

	switch acct.Auth {
	case config.AuthClientSecret:
		if has(vault.SecretKey(acct.Key())) {
			parts = append(parts, "secret stored")
			e.Credentials = true
		} else {
			parts = append(parts, "secret missing")
		}
	default:
		if acct.Auth == config.AuthPassword && !has(vault.SecretKey(acct.Key())) {
			parts = append(parts, "password missing")
		} else if acct.HomeAccountID != "" && has(vault.TokenCacheKey(acct.Key())) {
			parts = append(parts, "logged in")
			e.Credentials = true
		} else {
			parts = append(parts, "not logged in")
		}
	}
	if acct.SecretExpires != "" {
		if t, err := time.Parse("2006-01-02", acct.SecretExpires); err == nil {
			if time.Now().After(t.AddDate(0, 0, 1)) {
				parts = append(parts, "secret EXPIRED "+acct.SecretExpires)
				e.SecretExpired = true
			} else {
				parts = append(parts, "secret expires "+acct.SecretExpires)
			}
		}
	}
	if acct.Paused {
		parts = append(parts, "paused")
	}
	e.Status = strings.Join(parts, ", ")
	return e
}

// TokenInfo describes a token without exposing it.
type TokenInfo struct {
	// Account is the sign-in name or application ID, for display in the CLI.
	// Interfaces that must not show it leave it out.
	Account   string
	ExpiresOn time.Time
	Scopes    []string
}

func tokenInfo(tok auth.Token) TokenInfo {
	return TokenInfo{Account: tok.Account, ExpiresOn: tok.ExpiresOn, Scopes: slices.Clone(tok.Scopes)}
}

// LoginResult is the outcome of a sign-in.
type LoginResult struct {
	TokenInfo
	// Delegated is true for sign-ins as a user; false when a client secret was verified.
	Delegated bool
}

// Login signs the account in (device code, browser) or verifies its stored
// secret. ui presents the interactive steps.
func (o *Ops) Login(ctx context.Context, name string, ui auth.LoginUI) (LoginResult, error) {
	cfg, _, err := o.LoadConfig()
	if err != nil {
		return LoginResult{}, err
	}
	acct, err := o.FindAccount(cfg, name)
	if err != nil {
		return LoginResult{}, err
	}
	au, err := o.Authenticator(cfg, *acct)
	if err != nil {
		return LoginResult{}, WithHint(err, name)
	}
	tok, err := au.Login(ctx, ui)
	if err != nil {
		return LoginResult{}, WithHint(err, name)
	}
	if acct.Delegated() && tok.HomeAccountID != "" && tok.HomeAccountID != acct.HomeAccountID {
		err := o.updateAccountKey(ctx, acct.Key(), func(ac *config.Account) { ac.HomeAccountID = tok.HomeAccountID })
		if err != nil {
			return LoginResult{}, fmt.Errorf("signed in, but saving the config failed: %w", err)
		}
	}
	if acct.Auth == config.AuthPassword {
		o.resetPasswordBlock(acct.Key())
	}
	return LoginResult{TokenInfo: tokenInfo(tok), Delegated: acct.Delegated()}, nil
}

// TestAccount gets a token silently, which proves the stored sign-in works.
func (o *Ops) TestAccount(ctx context.Context, name string) (TokenInfo, error) {
	cfg, _, err := o.LoadConfig()
	if err != nil {
		return TokenInfo{}, err
	}
	acct, err := o.FindAccount(cfg, name)
	if err != nil {
		return TokenInfo{}, err
	}
	au, err := o.Authenticator(cfg, *acct)
	if err != nil {
		return TokenInfo{}, WithHint(err, name)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	tok, err := au.Token(ctx)
	if err != nil {
		return TokenInfo{}, WithHint(err, name)
	}
	return tokenInfo(tok), nil
}

// PauseResult says how a pause or resume will take effect.
type PauseResult struct {
	EngineRunning bool
}

// Pause stops scheduling rounds for an account.
func (o *Ops) Pause(ctx context.Context, name string) (PauseResult, error) {
	return o.setPaused(ctx, name, true)
}

// Resume resumes a paused account, or clears an automatic pause or attention state.
func (o *Ops) Resume(ctx context.Context, name string) (PauseResult, error) {
	return o.setPaused(ctx, name, false)
}

func (o *Ops) setPaused(ctx context.Context, name string, paused bool) (PauseResult, error) {
	cfg, _, err := o.LoadConfig()
	if err != nil {
		return PauseResult{}, err
	}
	acct, err := o.FindAccount(cfg, name)
	if err != nil {
		return PauseResult{}, err
	}
	key := acct.Key()
	if err := o.updateAccountKey(ctx, key, func(ac *config.Account) { ac.Paused = paused }); err != nil {
		return PauseResult{}, err
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return PauseResult{}, err
	}
	running := engine.Running(dataDir)
	if running {
		// Ask for an immediate reload instead of waiting for the periodic check.
		_ = engine.TriggerReload(dataDir)
	}
	if !paused {
		// The engine alone writes state.json, so it clears an automatic pause itself.
		if err := engine.TriggerResume(dataDir, key); err != nil {
			return PauseResult{}, err
		}
	}
	return PauseResult{EngineRunning: running}, nil
}
