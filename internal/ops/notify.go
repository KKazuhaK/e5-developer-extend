package ops

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

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

// webhookSource looks the webhook address up on every call: first the vault,
// then the plaintext config entry older versions used. Decrypted values are
// cached until the files change, so the keyring is not asked on every tick.
type webhookSource struct {
	o       *Ops
	cfg     *config.Config
	cfgPath string

	mu       sync.Mutex
	v        *vault.Vault
	vaultSig string
	vaultURL string
	cfgSig   string
	cfgURL   string
}

func (o *Ops) webhookSource(cfg *config.Config, cfgPath string) *webhookSource {
	return &webhookSource{o: o, cfg: cfg, cfgPath: cfgPath}
}

func fileSig(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", fi.ModTime().UnixNano(), fi.Size())
}

// URL returns the stored address.
func (w *webhookSource) URL() (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.v == nil {
		v, err := w.o.OpenVault(w.cfg)
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
		v, err := w.o.OpenVault(w.cfg)
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

// Webhook builds the alert sender. With retries false a failed delivery is not retried.
func (o *Ops) Webhook(cfg *config.Config, cfgPath string, log *slog.Logger, retries bool) *notify.Webhook {
	src := o.webhookSource(cfg, cfgPath)
	w := &notify.Webhook{URL: src.URL, Configured: src.Configured, HTTP: auth.NewHTTPClient(cfg.Network.Proxy), Log: log}
	switch {
	case o.WebhookBackoff != nil:
		w.Backoff = o.WebhookBackoff
	case !retries:
		w.Backoff = []time.Duration{}
	}
	return w
}

// Notifier builds the engine's alert sink.
func (o *Ops) Notifier(cfg *config.Config, cfgPath string, log *slog.Logger) *notify.Dispatcher {
	return notify.NewDispatcher(o.Webhook(cfg, cfgPath, log, true), log)
}

// WebhookConfigured says whether an alert address is stored. The address
// itself is never returned.
func (o *Ops) WebhookConfigured() bool {
	cfg, path, err := o.LoadConfigOrDefault()
	if err != nil {
		return false
	}
	return o.webhookSource(cfg, path).Configured()
}

// ValidWebhook checks that raw looks like an http(s) address.
func ValidWebhook(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		// The text is not echoed: it may be a pasted address with a token.
		return errors.New("the webhook address must be an http:// or https:// URL")
	}
	return nil
}

// SetWebhook stores the alert address in the vault and removes a plaintext
// notify.webhook_url from config.toml. clearedPlain says whether it removed one.
func (o *Ops) SetWebhook(ctx context.Context, raw string) (clearedPlain bool, err error) {
	raw = strings.TrimSpace(raw)
	if err := ValidWebhook(raw); err != nil {
		return false, err
	}
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return false, err
	}
	v, err := o.OpenVault(cfg)
	if err != nil {
		return false, WithHint(err, "")
	}
	if err := v.Put(notify.VaultKey, []byte(raw)); err != nil {
		return false, WithHint(fmt.Errorf("store the webhook address: %w", err), "")
	}
	return o.clearPlainWebhook(ctx)
}

// clearPlainWebhook removes notify.webhook_url from the config file, if the file exists and has one.
func (o *Ops) clearPlainWebhook(ctx context.Context) (bool, error) {
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil || cfg.Notify.WebhookURL == "" {
		return false, err
	}
	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	cfg, path, err := o.LoadConfig()
	if err != nil {
		return false, err
	}
	if cfg.Notify.WebhookURL == "" {
		return false, nil
	}
	cfg.Notify.WebhookURL = ""
	return true, cfg.Save(path)
}

// ClearWebhook removes the stored alert address. It reports whether there was one.
func (o *Ops) ClearWebhook(ctx context.Context) (removed bool, err error) {
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return false, err
	}
	v, err := o.OpenVault(cfg)
	if err != nil {
		return false, err
	}
	if err := v.Delete(notify.VaultKey); err == nil {
		removed = true
	} else if !errors.Is(err, vault.ErrNotFound) {
		return false, err
	}
	cleared, err := o.clearPlainWebhook(ctx)
	if err != nil {
		return false, err
	}
	return removed || cleared, nil
}

// TestWebhook sends a test alert.
func (o *Ops) TestWebhook(ctx context.Context) error {
	cfg, path, err := o.LoadConfigOrDefault()
	if err != nil {
		return err
	}
	w := o.Webhook(cfg, path, nil, false)
	ctx, cancel := context.WithTimeout(ctx, 2*notify.DefaultTimeout)
	defer cancel()
	ev := notify.Event{Event: notify.Test, Message: "this is a test alert; alerts are working", Time: time.Now()}
	if err := w.Send(ctx, ev); err != nil {
		if errors.Is(err, notify.ErrNotConfigured) {
			return errors.New("no webhook is set; run: e5extend notify set-webhook <url>")
		}
		return WithHint(fmt.Errorf("the test alert was not delivered: %w", err), "")
	}
	return nil
}
