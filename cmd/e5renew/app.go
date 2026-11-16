package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/KKazuhaK/e5-renew/internal/auth"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/paths"
	"github.com/KKazuhaK/e5-renew/internal/service"
	"github.com/KKazuhaK/e5-renew/internal/vault"
)

// app holds the pieces commands depend on, so tests can replace them.
type app struct {
	newAuth func(acct config.Account, store auth.Store, httpClient *http.Client) (auth.Authenticator, error)
	// graphBase overrides the Graph endpoint; tests point it at a fake server.
	graphBase string
	// noCallGap removes the pause between calls; tests only.
	noCallGap bool

	// The fields below let tests keep the real service manager, the real
	// binary path and the terminal out of the picture. Zero values mean the
	// real thing.
	runner      service.Runner // runs systemctl, launchctl, schtasks, loginctl
	exe         func() (string, error)
	svc         svcIdentity
	interactive func(in io.Reader) bool
	// webhookBackoff replaces the retry waits of alert delivery.
	webhookBackoff []time.Duration
}

func newApp() *app {
	return &app{newAuth: auth.New}
}

// loadConfig reads the config file and turns validation errors into a readable message.
func (a *app) loadConfig() (*config.Config, string, error) {
	path, err := paths.ConfigFile()
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(path)
	switch {
	case errors.Is(err, config.ErrNotFound):
		return nil, path, fmt.Errorf("%w\nrun: e5renew config init", err)
	case err != nil && cfg != nil:
		return nil, path, fmt.Errorf("%s is invalid; check it with: e5renew config validate\n%w", path, err)
	case err != nil:
		return nil, path, err
	}
	return cfg, path, nil
}

// loadConfigOrDefault is like loadConfig but starts from defaults when no file exists.
func (a *app) loadConfigOrDefault() (*config.Config, string, error) {
	cfg, path, err := a.loadConfig()
	if errors.Is(err, config.ErrNotFound) {
		return config.Default(), path, nil
	}
	return cfg, path, err
}

func (a *app) openVault(cfg *config.Config) (*vault.Vault, error) {
	return vault.Open(cfg.Vault.Backend)
}

func (a *app) authenticator(cfg *config.Config, acct config.Account) (auth.Authenticator, error) {
	v, err := a.openVault(cfg)
	if err != nil {
		return nil, err
	}
	return a.newAuth(acct, v, auth.NewHTTPClient(cfg.Network.Proxy))
}

func (a *app) findAccount(cfg *config.Config, name string) (*config.Account, error) {
	acct, ok := cfg.Find(name)
	if !ok {
		return nil, fmt.Errorf("no account named %q\nrun: e5renew account list", name)
	}
	return acct, nil
}

var errInterrupted = errors.New("interrupted")

// readSecret reads a secret without echo from a terminal, or one line from
// stdin otherwise. It returns early, with nothing read, when the command's
// context is cancelled (Ctrl+C, SIGTERM).
func readSecret(cmd *cobra.Command, prompt string) (string, error) {
	ctx := contextOrBackground(cmd)
	in := cmd.InOrStdin()
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return readLineCtx(ctx, in)
	}
	fd := int(f.Fd())
	state, _ := term.GetState(fd)
	fmt.Fprint(cmd.ErrOrStderr(), prompt)
	type result struct {
		b   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := term.ReadPassword(fd)
		ch <- result{b, err}
	}()
	select {
	case r := <-ch:
		fmt.Fprintln(cmd.ErrOrStderr())
		if r.err != nil {
			return "", fmt.Errorf("read secret: %w", r.err)
		}
		return string(r.b), nil
	case <-ctx.Done():
		if state != nil {
			_ = term.Restore(fd, state)
		}
		fmt.Fprintln(cmd.ErrOrStderr())
		return "", errInterrupted
	}
}

// readLineCtx is readLine that gives up when ctx is cancelled.
func readLineCtx(ctx context.Context, r io.Reader) (string, error) {
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := readLine(r)
		ch <- result{s, err}
	}()
	select {
	case res := <-ch:
		return res.s, res.err
	case <-ctx.Done():
		return "", errInterrupted
	}
}

// readLine reads one line byte by byte so that nothing beyond it is consumed.
func readLine(r io.Reader) (string, error) {
	var sb strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				break
			}
			sb.WriteByte(buf[0])
		}
		if err != nil {
			if errors.Is(err, io.EOF) && sb.Len() > 0 {
				break
			}
			return "", fmt.Errorf("read input: %w", err)
		}
	}
	return strings.TrimRight(sb.String(), "\r"), nil
}

// lockConfig serializes config read-modify-write cycles across processes.
func (a *app) lockConfig(cmd *cobra.Command) (func(), error) {
	p, err := paths.ConfigLockFile()
	if err != nil {
		return nil, err
	}
	return paths.Lock(contextOrBackground(cmd), p)
}

// hint returns the next step to suggest for err, or "".
func hint(err error, name string) string {
	text := err.Error()
	switch {
	case strings.Contains(text, "AADSTS7000218"):
		return "enable \"Allow public client flows\" in the app registration (Authentication > Advanced settings)"
	case strings.Contains(text, "AADSTS50126"):
		return "the stored password is wrong or expired: run `e5renew account set-secret " + name + "`"
	case strings.Contains(text, "AADSTS50076"), strings.Contains(text, "AADSTS50079"):
		return "this account requires MFA; switch to device_code or browser (remove it and add it again with --auth device_code)"
	case errors.Is(err, auth.ErrLoginRequired) && errors.Is(err, vault.ErrWrongKey):
		return "the stored token cache cannot be decrypted (did the master key change?); run: e5renew account login " + name + " (this discards the unreadable cache)"
	case errors.Is(err, auth.ErrLoginRequired):
		return "run: e5renew account login " + name
	case errors.Is(err, auth.ErrInvalidClient):
		return "check the client ID and client secret; to replace the secret run: e5renew account set-secret " + name
	case errors.Is(err, auth.ErrConsentRequired):
		return "an administrator must grant consent for the app's API permissions (Azure portal > App registrations > API permissions > Grant admin consent)"
	case errors.Is(err, auth.ErrTransient):
		return "network or service problem; check connectivity and the proxy setting, then try again"
	case errors.Is(err, vault.ErrNoMasterKey):
		return "if the vault already holds data, restore the old key (E5RENEW_MASTER_KEY or the system keyring entry) or run: e5renew vault reset; otherwise set E5RENEW_MASTER_KEY or run: e5renew vault init"
	case errors.Is(err, vault.ErrWrongKey):
		return "restore the original master key (E5RENEW_MASTER_KEY or the system keyring entry); otherwise re-enter the secret with: e5renew account set-secret " + name
	}
	return ""
}

func withHint(err error, name string) error {
	if h := hint(err, name); h != "" {
		return fmt.Errorf("%w\n%s", err, h)
	}
	return err
}

func contextOrBackground(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
