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

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// app holds the pieces commands depend on, so tests can replace them. The
// business logic lives in internal/ops; commands only read input and format output.
type app struct {
	newAuth func(acct config.Account, store auth.Store, httpClient *http.Client) (auth.Authenticator, error)
	// graphBase overrides the Graph endpoint; tests point it at a fake server.
	graphBase string
	// noCallGap removes the pause between calls; tests only.
	noCallGap bool

	// bootstrap replaces the administrator sign-in of automatic setup; tests only.
	bootstrap    func(ctx context.Context, method, tenant, clientID string, scopes []string, ui auth.LoginUI, c *http.Client) (auth.Token, error)
	setupBackoff []time.Duration
	setupHost    string

	// The fields below let tests keep the real service manager, the real
	// binary path and the terminal out of the picture. Zero values mean the
	// real thing.
	runner      service.Runner // runs systemctl, launchctl, schtasks, loginctl
	exe         func() (string, error)
	svc         svcIdentity
	interactive func(in io.Reader) bool
	// webhookBackoff replaces the retry waits of alert delivery.
	webhookBackoff []time.Duration
	// update replaces the outside world of the update commands; tests only.
	update ops.UpdateHooks
}

// svcIdentity overrides who the service is installed for; tests only.
type svcIdentity = ops.SvcIdentity

func newApp() *app {
	return &app{newAuth: auth.New}
}

// ops returns the operations layer configured like this app.
func (a *app) ops() *ops.Ops {
	return &ops.Ops{
		NewAuth: a.newAuth, GraphBase: a.graphBase, NoCallGap: a.noCallGap,
		Runner: a.runner, Exe: a.exe, Svc: a.svc, WebhookBackoff: a.webhookBackoff,
		Bootstrap: a.bootstrap, SetupBackoff: a.setupBackoff, SetupHostname: a.setupHost,
		Update: a.update,
	}
}

// loadConfig reads the config file and turns validation errors into a readable message.
func (a *app) loadConfig() (*config.Config, string, error) { return a.ops().LoadConfig() }

// loadConfigOrDefault is like loadConfig but starts from defaults when no file exists.
func (a *app) loadConfigOrDefault() (*config.Config, string, error) {
	return a.ops().LoadConfigOrDefault()
}

func (a *app) openVault(cfg *config.Config) (*vault.Vault, error) { return a.ops().OpenVault(cfg) }

func (a *app) authenticator(cfg *config.Config, acct config.Account) (auth.Authenticator, error) {
	return a.ops().Authenticator(cfg, acct)
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

// hint returns the next step to suggest for err, or "".
func hint(err error, name string) string { return ops.Hint(err, name) }

func withHint(err error, name string) error { return ops.WithHint(err, name) }

func contextOrBackground(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
