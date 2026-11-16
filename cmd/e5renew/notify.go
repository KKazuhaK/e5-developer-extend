package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/KKazuhaK/e5-renew/internal/auth"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/notify"
	"github.com/KKazuhaK/e5-renew/internal/paths"
	"github.com/KKazuhaK/e5-renew/internal/vault"
)

// webhookSource looks the webhook address up on every call: first the vault,
// then the plaintext config entry older versions used. Decrypted values are
// cached until the files change, so the keyring is not asked on every tick.
type webhookSource struct {
	a       *app
	cfg     *config.Config
	cfgPath string

	mu       sync.Mutex
	v        *vault.Vault
	vaultSig string
	vaultURL string
	cfgSig   string
	cfgURL   string
}

func fileSig(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", fi.ModTime().UnixNano(), fi.Size())
}

func (w *webhookSource) URL() (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.v == nil {
		v, err := w.a.openVault(w.cfg)
		if err != nil {
			return "", err
		}
		w.v = v
	}
	if has, _ := w.v.Has(notify.VaultKey); has {
		vdir, err := paths.VaultDir()
		if err != nil {
			return "", err
		}
		sig := fileSig(filepath.Join(vdir, filepath.FromSlash(notify.VaultKey)+".bin"))
		if sig != w.vaultSig || w.vaultURL == "" {
			b, err := w.v.Get(notify.VaultKey)
			if err != nil {
				return "", err
			}
			w.vaultSig, w.vaultURL = sig, strings.TrimSpace(string(b))
		}
		return w.vaultURL, nil
	}
	if u := w.legacyLocked(); u != "" {
		return u, nil
	}
	return "", notify.ErrNotConfigured
}

// legacyLocked returns the plaintext address from config.toml, re-read when the file changes.
func (w *webhookSource) legacyLocked() string {
	if w.cfgPath == "" {
		return w.cfg.Notify.WebhookURL
	}
	if sig := fileSig(w.cfgPath); sig != w.cfgSig {
		w.cfgSig, w.cfgURL = sig, ""
		if c, err := config.Load(w.cfgPath); c != nil && err == nil {
			w.cfgURL = c.Notify.WebhookURL
		}
	}
	return w.cfgURL
}

// Configured says whether an address is stored, without decrypting it.
func (w *webhookSource) Configured() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.v == nil {
		v, err := w.a.openVault(w.cfg)
		if err != nil {
			return false
		}
		w.v = v
	}
	if has, _ := w.v.Has(notify.VaultKey); has {
		return true
	}
	return w.legacyLocked() != ""
}

func (a *app) webhook(cfg *config.Config, cfgPath string, log *slog.Logger, retries bool) *notify.Webhook {
	src := &webhookSource{a: a, cfg: cfg, cfgPath: cfgPath}
	w := &notify.Webhook{URL: src.URL, Configured: src.Configured, HTTP: auth.NewHTTPClient(cfg.Network.Proxy), Log: log}
	switch {
	case a.webhookBackoff != nil:
		w.Backoff = a.webhookBackoff
	case !retries:
		w.Backoff = []time.Duration{}
	}
	return w
}

// notifier builds the engine's alert sink.
func (a *app) notifier(cfg *config.Config, cfgPath string, log *slog.Logger) *notify.Dispatcher {
	return notify.NewDispatcher(a.webhook(cfg, cfgPath, log, true), log)
}

func (a *app) newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "notify", Short: "Set up webhook alerts"}
	cmd.AddCommand(a.newNotifySetCmd(), a.newNotifyClearCmd(), a.newNotifyTestCmd())
	return cmd
}

func validWebhook(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		// The text is not echoed: it may be a pasted address with a token.
		return errors.New("the webhook address must be an http:// or https:// URL")
	}
	return nil
}

func (a *app) newNotifySetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-webhook [url]",
		Short: "Store the webhook address for alerts (in the encrypted vault)",
		Long: "Store the webhook address in the vault, because it often contains a token.\n" +
			"Without an argument the address is read without echo from the terminal, or one line from stdin.\n" +
			"A plaintext notify.webhook_url in config.toml is removed.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw string
			if len(args) == 1 {
				raw = args[0]
			} else {
				var err error
				if raw, err = readSecret(cmd, "Webhook address: "); err != nil {
					return err
				}
			}
			raw = strings.TrimSpace(raw)
			if err := validWebhook(raw); err != nil {
				return err
			}
			cfg, _, err := a.loadConfigOrDefault()
			if err != nil {
				return err
			}
			v, err := a.openVault(cfg)
			if err != nil {
				return withHint(err, "")
			}
			if err := v.Put(notify.VaultKey, []byte(raw)); err != nil {
				return withHint(fmt.Errorf("store the webhook address: %w", err), "")
			}
			cleared, err := a.clearPlainWebhook(cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "webhook address stored in the vault (it is not shown again)")
			if cleared {
				fmt.Fprintln(out, "removed notify.webhook_url from config.toml")
			}
			fmt.Fprintln(out, "next: e5renew notify test")
			return nil
		},
	}
}

// clearPlainWebhook removes notify.webhook_url from the config file, if the file exists and has one.
func (a *app) clearPlainWebhook(cmd *cobra.Command) (bool, error) {
	cfg, _, err := a.loadConfigOrDefault()
	if err != nil || cfg.Notify.WebhookURL == "" {
		return false, err
	}
	unlock, err := a.lockConfig(cmd)
	if err != nil {
		return false, err
	}
	defer unlock()
	cfg, path, err := a.loadConfig()
	if err != nil {
		return false, err
	}
	if cfg.Notify.WebhookURL == "" {
		return false, nil
	}
	cfg.Notify.WebhookURL = ""
	return true, cfg.Save(path)
}

func (a *app) newNotifyClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove the stored webhook address",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := a.loadConfigOrDefault()
			if err != nil {
				return err
			}
			v, err := a.openVault(cfg)
			if err != nil {
				return err
			}
			removed := false
			if err := v.Delete(notify.VaultKey); err == nil {
				removed = true
			} else if !errors.Is(err, vault.ErrNotFound) {
				return err
			}
			cleared, err := a.clearPlainWebhook(cmd)
			if err != nil {
				return err
			}
			if removed || cleared {
				fmt.Fprintln(cmd.OutOrStdout(), "webhook removed; alerts are off")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "no webhook was set")
			}
			return nil
		},
	}
}

func (a *app) newNotifyTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Send a test alert to the webhook",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, path, err := a.loadConfigOrDefault()
			if err != nil {
				return err
			}
			w := a.webhook(cfg, path, nil, false)
			ctx, cancel := context.WithTimeout(contextOrBackground(cmd), 2*notify.DefaultTimeout)
			defer cancel()
			ev := notify.Event{Event: notify.Test, Message: "this is a test alert; alerts are working", Time: time.Now()}
			if err := w.Send(ctx, ev); err != nil {
				if errors.Is(err, notify.ErrNotConfigured) {
					return errors.New("no webhook is set; run: e5renew notify set-webhook <url>")
				}
				return withHint(fmt.Errorf("the test alert was not delivered: %w", err), "")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "test alert delivered")
			return nil
		},
	}
}
