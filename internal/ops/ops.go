// Package ops holds the operations behind both user interfaces: the command
// line (cmd/e5extend) and the desktop app (gui). Every operation returns typed
// results and never prints, so each interface formats them its own way.
//
// Nothing here returns or logs a secret, a token or the webhook address.
package ops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// SvcIdentity overrides who the background job is installed for; tests only.
type SvcIdentity struct {
	Home, UID, User string
}

// Ops carries the pieces operations depend on, so tests can replace them. The
// zero value works and means the real thing.
type Ops struct {
	// NewAuth builds the authenticator of an account (auth.New when nil).
	NewAuth func(acct config.Account, store auth.Store, httpClient *http.Client) (auth.Authenticator, error)
	// GraphBase overrides the Graph endpoint; tests point it at a fake server.
	GraphBase string
	// NoCallGap removes the pause between calls; tests only.
	NoCallGap bool
	// Runner runs systemctl, launchctl, schtasks and loginctl.
	Runner service.Runner
	// Exe returns the path of the binary to register as the background job.
	Exe func() (string, error)
	// Svc overrides who the background job is installed for.
	Svc SvcIdentity
	// Platform overrides the service manager's platform (runtime.GOOS when
	// empty); tests use it to exercise one manager on every host.
	Platform string
	// WebhookBackoff replaces the retry waits of alert delivery.
	WebhookBackoff []time.Duration
	// VaultFor replaces how the vault is opened; tests use it to give each
	// simulated machine its own master key.
	VaultFor func(cfg *config.Config) (*vault.Vault, error)
	// Bootstrap replaces the administrator sign-in of automatic app setup
	// (auth.Bootstrap when nil); tests use it to skip the identity service.
	Bootstrap func(ctx context.Context, method, tenant, clientID string, scopes []string, ui auth.LoginUI, c *http.Client) (auth.Token, error)
	// SetupBackoff replaces the waits for Microsoft to replicate a new app
	// registration; nil means about two minutes in total.
	SetupBackoff []time.Duration
	// SetupHostname replaces os.Hostname in the client secret's name.
	SetupHostname string
	// SaveConfig replaces how an import saves config.toml; tests inject failures.
	SaveConfig func(cfg *config.Config, path string) error
	// RefreshWait is how long a subscription refresh waits for a running
	// engine; zero means the default of about 75 seconds.
	RefreshWait time.Duration
	// Update replaces the outside world of the update check and upgrade.
	Update UpdateHooks
}

// New returns Ops that talk to the real services.
func New() *Ops { return &Ops{NewAuth: auth.New} }

func (o *Ops) newAuth(acct config.Account, store auth.Store, c *http.Client) (auth.Authenticator, error) {
	if o.NewAuth != nil {
		return o.NewAuth(acct, store, c)
	}
	return auth.New(acct, store, c)
}

// LoadConfig reads the config file and turns validation errors into a readable message.
func (o *Ops) LoadConfig() (*config.Config, string, error) {
	path, err := paths.ConfigFile()
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(path)
	switch {
	case errors.Is(err, config.ErrNotFound):
		return nil, path, fmt.Errorf("%w\nrun: e5extend config init", err)
	case err != nil && cfg != nil:
		return nil, path, fmt.Errorf("%s is invalid; check it with: e5extend config validate\n%w", path, err)
	case err != nil:
		return nil, path, err
	}
	return cfg, path, nil
}

// LoadConfigOrDefault is like LoadConfig but starts from defaults when no file exists.
func (o *Ops) LoadConfigOrDefault() (*config.Config, string, error) {
	cfg, path, err := o.LoadConfig()
	if errors.Is(err, config.ErrNotFound) {
		return config.Default(), path, nil
	}
	return cfg, path, err
}

// OpenVault opens the vault named by the configuration.
func (o *Ops) OpenVault(cfg *config.Config) (*vault.Vault, error) {
	if o.VaultFor != nil {
		return o.VaultFor(cfg)
	}
	return vault.Open(cfg.Vault.Backend)
}

// Authenticator builds the authenticator of acct on top of the vault.
func (o *Ops) Authenticator(cfg *config.Config, acct config.Account) (auth.Authenticator, error) {
	v, err := o.OpenVault(cfg)
	if err != nil {
		return nil, err
	}
	return o.newAuth(acct, v, auth.NewHTTPClient(cfg.Network.Proxy))
}

// FindAccount returns the named account or an error that says how to list them.
func (o *Ops) FindAccount(cfg *config.Config, name string) (*config.Account, error) {
	acct, ok := cfg.Find(name)
	if !ok {
		return nil, fmt.Errorf("no account named %q\nrun: e5extend account list", name)
	}
	return acct, nil
}

// LockConfig serializes config read-modify-write cycles across processes.
func (o *Ops) LockConfig(ctx context.Context) (func(), error) {
	p, err := paths.ConfigLockFile()
	if err != nil {
		return nil, err
	}
	return paths.Lock(ctx, p)
}

// Hint returns the next step to suggest for err, or "".
func Hint(err error, name string) string {
	_, text := hintInfo(err, name)
	return text
}

// HintCode names the suggestion Hint gives for err (one of doctor.HintCodes),
// or "" when Hint has none.
func HintCode(err error) string {
	code, _ := hintInfo(err, "")
	return code
}

// hintInfo is the code and the English text of the suggestion for err.
func hintInfo(err error, name string) (code, text string) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "AADSTS7000218"):
		return "hint.publicClient", "enable \"Allow public client flows\" in the app registration (Authentication > Advanced settings)"
	case strings.Contains(msg, "AADSTS70043"):
		// Seen on a real tenant: a Conditional Access "sign-in frequency" policy
		// caps the refresh token's lifetime, so signing in again only buys that
		// much time and the background job keeps stopping.
		return "hint.signInFrequency", "ways out, in order: (a) use the app identity (recommended), which user sign-in policies do not affect: e5extend account add " + name + "-app --auto --auth client_secret (choose another name if taken); " +
			"(b) password sign-in, only if the account has no MFA (the password is stored encrypted on this machine); a sign-in frequency policy often comes with MFA, so this usually fails there; " +
			"(c) exclude this app registration from the Conditional Access policy (Entra > Protection > Conditional Access), then run `e5extend account login " + name + "`"
	case strings.Contains(msg, "AADSTS50126"):
		return "hint.passwordWrong", "the stored password is wrong or expired: run `e5extend account set-secret " + name + "` (automatic password sign-in stays paused until then)"
	case strings.Contains(msg, "AADSTS53003"):
		return "hint.caBlocked", "blocked by a Conditional Access policy; ask the administrator, or check the policy's conditions (location, device, workload identity)"
	case strings.Contains(msg, "AADSTS50053"):
		return "hint.locked", "the account is locked (Smart Lockout); wait, sign in on the web to unlock it, then run `e5extend account login " + name + "`"
	case strings.Contains(msg, "AADSTS50055"):
		return "hint.passwordExpired", "the password has expired: change it, then run `e5extend account set-secret " + name + "`"
	case strings.Contains(msg, "AADSTS50057"):
		return "hint.disabled", "the account is disabled in Entra; enable it there, then run `e5extend account login " + name + "`"
	case strings.Contains(msg, "AADSTS50076"), strings.Contains(msg, "AADSTS50079"):
		return "hint.mfa", "this account requires MFA; switch to device_code or browser (remove it and add it again with --auth device_code)"
	case errors.Is(err, auth.ErrLoginRequired) && errors.Is(err, vault.ErrWrongKey):
		return "hint.tokenCacheKey", "the stored token cache cannot be decrypted (did the master key change?); run: e5extend account login " + name + " (this discards the unreadable cache)"
	case errors.Is(err, auth.ErrLoginRequired):
		return "hint.loginRequired", "run: e5extend account login " + name
	case errors.Is(err, auth.ErrInvalidClient):
		return "hint.invalidClient", "check the client ID and client secret; to replace the secret run: e5extend account set-secret " + name
	case errors.Is(err, auth.ErrConsentRequired):
		return "hint.consent", "an administrator must grant consent for the app's API permissions (Azure portal > App registrations > API permissions > Grant admin consent)"
	case errors.Is(err, auth.ErrTransient):
		return "hint.transient", "network or service problem; check connectivity and the proxy setting, then try again"
	case errors.Is(err, vault.ErrRemoteSession):
		return "hint.remoteSession", "the key is probably fine but out of reach from this remote session: run the command in the desktop session, or set E5EXTEND_MASTER_KEY for this shell; do not reset the vault"
	case errors.Is(err, vault.ErrNoMasterKey):
		return "hint.noMasterKey", "if the vault already holds data, restore the old key (E5EXTEND_MASTER_KEY or the system keyring entry); if it is empty, run: e5extend vault init; " +
			"only as a last resort, e5extend vault reset deletes all stored secrets"
	case errors.Is(err, vault.ErrWrongKey):
		return "hint.wrongKey", "restore the original master key (E5EXTEND_MASTER_KEY or the system keyring entry); otherwise re-enter the secret with: e5extend account set-secret " + name
	}
	return "", ""
}

// WithHint appends the suggested next step, if there is one, to err.
func WithHint(err error, name string) error {
	if h := Hint(err, name); h != "" {
		return fmt.Errorf("%w\n%s", err, h)
	}
	return err
}
