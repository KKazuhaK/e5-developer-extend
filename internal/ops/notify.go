package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
)

func fileSig(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", fi.ModTime().UnixNano(), fi.Size())
}

// channelEntry is a channel together with where its settings came from.
type channelEntry struct {
	config.Channel
	// legacy marks the channel older versions had: a webhook address in the
	// vault entry notify/webhook or in config.toml, not yet saved as a channel.
	legacy bool
}

// channelSource looks the channels up on every call: the settings from the
// config file (re-read when it changes) and the secrets from the vault
// (decrypted values are cached until their file changes), so a change made
// in the app or with the CLI reaches a running engine without a restart.
type channelSource struct {
	o       *Ops
	cfg     *config.Config
	cfgPath string

	mu        sync.Mutex
	v         *vault.Vault
	cfgSig    string
	channels  []config.Channel
	legacyURL string // plaintext notify.webhook_url
	cache     map[string]cachedSecret
}

type cachedSecret struct{ sig, val string }

func (o *Ops) channelSource(cfg *config.Config, cfgPath string) *channelSource {
	return &channelSource{o: o, cfg: cfg, cfgPath: cfgPath, channels: cfg.Notify.Channels,
		legacyURL: cfg.Notify.WebhookURL, cache: map[string]cachedSecret{}}
}

func (s *channelSource) vaultLocked() (*vault.Vault, error) {
	if s.v == nil {
		v, err := s.o.OpenVault(s.cfg)
		if err != nil {
			return nil, err
		}
		s.v = v
	}
	return s.v, nil
}

// refreshLocked re-reads the config file when it changed.
func (s *channelSource) refreshLocked() {
	if s.cfgPath == "" {
		return
	}
	sig := fileSig(s.cfgPath)
	if sig == s.cfgSig {
		return
	}
	s.cfgSig = sig
	if c, err := config.Load(s.cfgPath); c != nil && err == nil {
		s.channels, s.legacyURL = c.Notify.Channels, c.Notify.WebhookURL
	}
}

// entriesLocked lists the channels, including the legacy webhook when one exists.
func (s *channelSource) entriesLocked() []channelEntry {
	s.refreshLocked()
	var out []channelEntry
	hasWebhook := false
	for _, c := range s.channels {
		out = append(out, channelEntry{Channel: c})
		hasWebhook = hasWebhook || c.ID == notify.LegacyChannelID
	}
	if !hasWebhook {
		legacy := s.legacyURL != ""
		if !legacy {
			if v, err := s.vaultLocked(); err == nil {
				legacy, _ = v.Has(notify.VaultKey)
			}
		}
		if legacy {
			out = append(out, channelEntry{Channel: config.Channel{ID: notify.LegacyChannelID, Type: config.ChannelWebhook}, legacy: true})
		}
	}
	return out
}

// secretLocked returns the stored value of a vault key; ok is false when none is stored.
func (s *channelSource) secretLocked(key string) (val string, ok bool, err error) {
	v, err := s.vaultLocked()
	if err != nil {
		return "", false, err
	}
	has, err := v.Has(key)
	if err != nil || !has {
		return "", false, err
	}
	vdir, err := paths.VaultDir()
	if err != nil {
		return "", false, err
	}
	sig := fileSig(filepath.Join(vdir, filepath.FromSlash(key)+".bin"))
	if c, hit := s.cache[key]; hit && c.sig == sig && c.val != "" {
		return c.val, true, nil
	}
	b, err := v.Get(key)
	if err != nil {
		return "", false, err
	}
	val = strings.TrimSpace(string(b))
	s.cache[key] = cachedSecret{sig, val}
	return val, true, nil
}

// Configured says whether any channel exists, without decrypting anything.
func (s *channelSource) Configured() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entriesLocked()) > 0
}

// Targets returns every channel with its secrets.
func (s *channelSource) Targets() ([]notify.Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.entriesLocked()
	if len(entries) > 0 {
		if _, err := s.vaultLocked(); err != nil {
			return nil, err
		}
	}
	var out []notify.Target
	for _, e := range entries {
		t := notify.Target{Channel: e.Channel, Secrets: map[string]string{}, AllowHTTP: e.legacy || (e.Type == config.ChannelWebhook && e.Channel.AllowHTTP)}
		for _, f := range config.ChannelSecrets(e.Type) {
			val, ok, err := s.secretLocked(notify.ChannelSecretKey(e.ID, f.Name))
			if err != nil {
				t.Err = err
				break
			}
			if !ok && e.ID == notify.LegacyChannelID && e.Type == config.ChannelWebhook && f.Name == "url" {
				// Older versions kept the address under notify/webhook, or in the config file.
				if val, ok, err = s.secretLocked(notify.VaultKey); err != nil {
					t.Err = err
					break
				}
				if !ok {
					val, ok = s.legacyURL, s.legacyURL != ""
				}
				t.AllowHTTP = t.AllowHTTP || ok
			}
			if ok {
				t.Secrets[f.Name] = val
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// ---- last results ----

// ChannelResult is how the last delivery of a channel went.
type ChannelResult = notify.ChannelStatus

func channelDataDir() string {
	dir, _ := paths.DataDir()
	return dir
}

func readChannelStatus() map[string]ChannelResult { return notify.ReadStatus(channelDataDir()) }

// recordChannelResult remembers the outcome of a delivery for the app's channel list.
func recordChannelResult(id string, err error) { notify.RecordStatus(channelDataDir(), id, err) }

func forgetChannelResult(id string) { notify.ForgetStatus(channelDataDir(), id) }

// defaultLanguage is the interface language the app saved, for channels that follow it.
func defaultLanguage() string {
	dir, err := paths.ConfigDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "gui.json"))
	if err != nil {
		return ""
	}
	var f struct {
		Language string `json:"language"`
	}
	if json.Unmarshal(b, &f) != nil {
		return ""
	}
	return f.Language
}

// Router builds the alert sender. With retries false a failed delivery is not retried.
func (o *Ops) Router(cfg *config.Config, cfgPath string, log *slog.Logger, retries bool) *notify.Router {
	src := o.channelSource(cfg, cfgPath)
	d := &notify.Deliverer{HTTP: auth.NewHTTPClient(cfg.Network.Proxy), Log: log, DefaultLanguage: defaultLanguage()}
	switch {
	case o.WebhookBackoff != nil:
		d.Backoff = o.WebhookBackoff
	case !retries:
		d.Backoff = []time.Duration{}
	}
	return &notify.Router{Targets: src.Targets, Configured: src.Configured, Deliverer: d, Record: recordChannelResult}
}

// Notifier builds the engine's alert sink.
func (o *Ops) Notifier(cfg *config.Config, cfgPath string, log *slog.Logger) *notify.Dispatcher {
	return notify.NewDispatcher(o.Router(cfg, cfgPath, log, true), log)
}

// ---- channel management ----

// SecretStatus says whether one secret field of a channel is stored. The
// value itself never leaves the vault.
type SecretStatus struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Set      bool   `json:"set"`
}

// ChannelInput is the settings of a channel as the app and the CLI edit them.
// Secrets holds new secret values by field; a field left out or empty keeps
// what is stored.
type ChannelInput struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	MinLevel string   `json:"minLevel"`
	Events   []string `json:"events"`
	Language string   `json:"language"`
	Template string   `json:"template"`
	ChatID   string   `json:"chatId"`
	APIBase  string   `json:"apiBase"`
	SMTPHost string   `json:"smtpHost"`
	SMTPPort int      `json:"smtpPort"`
	Security string   `json:"security"`
	Username string   `json:"username"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	Server   string   `json:"server"`
	Topic    string   `json:"topic"`
	Format   string   `json:"format"`
	// AllowHTTP (webhook) and AllowPlainAuth (email) are explicit exceptions kept
	// from earlier setups; the app shows them but does not offer to set them.
	AllowHTTP      bool              `json:"allowHttp"`
	AllowPlainAuth bool              `json:"allowPlainAuth"`
	Secrets        map[string]string `json:"secrets"`
	ClearSecrets   []string          `json:"clearSecrets"`
}

func (in ChannelInput) channel() config.Channel {
	return config.Channel{ID: in.ID, Type: in.Type, MinLevel: in.MinLevel, Events: slices.Clone(in.Events),
		Language: in.Language, Template: in.Template, ChatID: strings.TrimSpace(in.ChatID), APIBase: strings.TrimSpace(in.APIBase),
		SMTPHost: strings.TrimSpace(in.SMTPHost), SMTPPort: in.SMTPPort, Security: in.Security, Username: strings.TrimSpace(in.Username),
		From: strings.TrimSpace(in.From), To: slices.Clone(in.To), Server: strings.TrimSpace(in.Server),
		Topic: strings.TrimSpace(in.Topic), Format: in.Format,
		AllowHTTP: in.AllowHTTP && in.Type == config.ChannelWebhook, AllowPlainAuth: in.AllowPlainAuth && in.Type == config.ChannelEmail}
}

func inputOf(c config.Channel) ChannelInput {
	return ChannelInput{ID: c.ID, Type: c.Type, MinLevel: c.MinLevel, Events: nonNil(c.Events), Language: c.Language,
		Template: c.Template, ChatID: c.ChatID, APIBase: c.APIBase, SMTPHost: c.SMTPHost, SMTPPort: c.SMTPPort,
		Security: c.Security, Username: c.Username, From: c.From, To: nonNil(c.To), Server: c.Server, Topic: c.Topic, Format: c.Format,
		AllowHTTP: c.AllowHTTP, AllowPlainAuth: c.AllowPlainAuth}
}

// ChannelView is a channel for display: its settings, which secrets are
// stored, and how the last delivery went.
type ChannelView struct {
	ChannelInput
	SecretFields []SecretStatus `json:"secretFields"`
	// Legacy means the channel is the webhook of older versions; saving it
	// moves it to the current format.
	Legacy     bool           `json:"legacy"`
	LastResult *ChannelResult `json:"lastResult"`
	// Warnings are problems that do not stop the channel, such as an http server address.
	Warnings []string `json:"warnings"`
}

// ChannelWarnings lists what is risky about a channel's settings.
func ChannelWarnings(c config.Channel, legacy bool) []string {
	var w []string
	switch c.Type {
	case config.ChannelBark, config.ChannelNtfy, config.ChannelTelegram:
		srv := c.Server
		if c.Type == config.ChannelTelegram {
			srv = c.APIBase
		}
		if notify.IsPlainHTTP(srv) {
			w = append(w, "server_http")
		}
	case config.ChannelEmail:
		if c.SecurityOrDefault() == config.SecurityNone {
			w = append(w, "email_plain")
		}
	case config.ChannelWebhook:
		if c.AllowHTTP {
			w = append(w, "allow_http")
		}
	}
	if legacy {
		w = append(w, "legacy")
	}
	return w
}

// ListChannels returns every channel, with secrets shown only as set or not.
func (o *Ops) ListChannels() ([]ChannelView, error) {
	cfg, path, err := o.LoadConfigOrDefault()
	if err != nil {
		return nil, err
	}
	src := o.channelSource(cfg, path)
	src.mu.Lock()
	entries := src.entriesLocked()
	src.mu.Unlock()
	var v *vault.Vault
	if len(entries) > 0 {
		v, err = o.OpenVault(cfg)
		if err != nil {
			return nil, err
		}
	}
	status := readChannelStatus()
	out := []ChannelView{}
	for _, e := range entries {
		view := ChannelView{ChannelInput: inputOf(e.Channel), Legacy: e.legacy, SecretFields: []SecretStatus{}}
		for _, f := range config.ChannelSecrets(e.Type) {
			set, _ := v.Has(notify.ChannelSecretKey(e.ID, f.Name))
			if !set && e.ID == notify.LegacyChannelID && e.Type == config.ChannelWebhook && f.Name == "url" {
				legacySet, _ := v.Has(notify.VaultKey)
				set = legacySet || cfg.Notify.WebhookURL != ""
			}
			view.SecretFields = append(view.SecretFields, SecretStatus{Name: f.Name, Required: f.Required, Set: set})
		}
		if r, ok := status[e.ID]; ok {
			r := r
			view.LastResult = &r
		}
		view.Warnings = ChannelWarnings(e.Channel, e.legacy)
		if view.Warnings == nil {
			view.Warnings = []string{}
		}
		out = append(out, view)
	}
	return out, nil
}

// GetChannel returns the settings of one channel (no secrets) for editing.
func (o *Ops) GetChannel(id string) (ChannelInput, error) {
	chs, err := o.ListChannels()
	if err != nil {
		return ChannelInput{}, err
	}
	for _, c := range chs {
		if c.ID == id {
			return c.ChannelInput, nil
		}
	}
	return ChannelInput{}, fmt.Errorf("no channel named %q\nrun: e5extend notify channel list", id)
}

// channelFieldNames maps config field paths to the camelCase names the app uses.
var channelFieldNames = map[string]string{
	"id": "id", "type": "type", "min_level": "minLevel", "events": "events", "language": "language", "template": "template",
	"chat_id": "chatId", "api_base": "apiBase", "smtp_host": "smtpHost", "smtp_port": "smtpPort", "security": "security",
	"username": "username", "from": "from", "to": "to", "server": "server", "topic": "topic", "format": "format",
}

// validateChannel checks one channel on its own: the settings, the template
// and the secret values that are about to be stored.
func validateChannel(in ChannelInput) []ValidationError {
	var bad []ValidationError
	cfg := config.Default()
	cfg.Notify.Channels = []config.Channel{in.channel()}
	if err := cfg.Validate(); err != nil {
		for _, e := range splitErrors(err) {
			msg := e.Error()
			rest, ok := strings.CutPrefix(msg, "notify.channels[0].")
			if !ok {
				continue
			}
			field, text, _ := strings.Cut(rest, ": ")
			field = strings.SplitN(field, "[", 2)[0]
			name := channelFieldNames[field]
			if name == "" {
				name = field
			}
			bad = append(bad, ValidationError{Field: name, Message: text})
		}
	}
	if len(bad) > 0 || !config.ValidChannelType(in.Type) {
		return bad
	}
	if in.Template != "" {
		if err := notify.CheckTemplate(in.Type, in.Template, in.Type == config.ChannelWebhook && in.Format == config.FormatTemplate); err != nil {
			bad = append(bad, ValidationError{Field: "template", Message: err.Error()})
		}
	}
	for name, val := range in.Secrets {
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		if !slices.ContainsFunc(config.ChannelSecrets(in.Type), func(f config.SecretField) bool { return f.Name == name }) {
			bad = append(bad, ValidationError{Field: "secrets." + name, Message: "this channel type has no such secret"})
			continue
		}
		if name == "url" {
			// The text is not echoed: it may be a pasted address with a token.
			if u, err := url.Parse(val); err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && (notify.IsLoopback(u.Hostname()) || (in.AllowHTTP && in.Type == config.ChannelWebhook)))) {
				bad = append(bad, ValidationError{Field: "secrets.url", Message: "the address must be an https:// URL"})
			}
		}
		if strings.ContainsAny(val, "\r\n") {
			bad = append(bad, ValidationError{Field: "secrets." + name, Message: "must be a single line"})
		}
	}
	return bad
}

// SaveChannel creates (create true) or updates a channel. Secrets in in.Secrets
// are stored in the vault; the rest goes to config.toml. It returns the
// problems with the values, nothing being written then.
func (o *Ops) SaveChannel(ctx context.Context, in ChannelInput, create bool) ([]ValidationError, error) {
	in.ID = strings.TrimSpace(in.ID)
	if bad := validateChannel(in); len(bad) > 0 {
		return bad, nil
	}
	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	cfg, path, err := o.LoadConfigOrDefault()
	if err != nil {
		return nil, err
	}
	existing, found := cfg.Notify.FindChannel(in.ID)
	legacy := false
	if !found && in.ID == notify.LegacyChannelID {
		// The webhook of older versions counts as an existing channel.
		src := o.channelSource(cfg, path)
		src.mu.Lock()
		for _, e := range src.entriesLocked() {
			legacy = legacy || (e.legacy && e.ID == in.ID)
		}
		src.mu.Unlock()
		found = legacy
	}
	switch {
	case create && found:
		return []ValidationError{{Field: "id", Message: "a channel with this id already exists"}}, nil
	case !create && !found:
		return nil, fmt.Errorf("no channel named %q\nrun: e5extend notify channel list", in.ID)
	case !create && existing != nil && existing.Type != in.Type:
		return []ValidationError{{Field: "type", Message: "the type of a channel cannot change; remove it and add it again"}}, nil
	}
	v, err := o.OpenVault(cfg)
	if err != nil {
		return nil, WithHint(err, "")
	}
	// Required secrets must be there once the channel is saved.
	var missing []ValidationError
	for _, f := range config.ChannelSecrets(in.Type) {
		if !f.Required || strings.TrimSpace(in.Secrets[f.Name]) != "" {
			continue
		}
		has, _ := v.Has(notify.ChannelSecretKey(in.ID, f.Name))
		if !has && legacy && f.Name == "url" {
			has, _ = v.Has(notify.VaultKey)
			has = has || cfg.Notify.WebhookURL != ""
		}
		if !has || slices.Contains(in.ClearSecrets, f.Name) {
			missing = append(missing, ValidationError{Field: "secrets." + f.Name, Message: "is required"})
		}
	}
	if len(missing) > 0 {
		return missing, nil
	}
	migratedHTTP := false
	for _, f := range config.ChannelSecrets(in.Type) {
		key := notify.ChannelSecretKey(in.ID, f.Name)
		if val := strings.TrimSpace(in.Secrets[f.Name]); val != "" {
			if err := v.Put(key, []byte(val)); err != nil {
				return nil, WithHint(fmt.Errorf("store the %s of channel %s: %w", f.Name, in.ID, err), "")
			}
		} else if legacy && f.Name == "url" {
			// Saving the legacy webhook moves its address to the new key.
			if old, err := v.Get(notify.VaultKey); err == nil {
				migratedHTTP = isPlainHTTP(string(old))
				if err := v.Put(key, old); err != nil {
					return nil, WithHint(fmt.Errorf("store the url of channel %s: %w", in.ID, err), "")
				}
			} else if cfg.Notify.WebhookURL != "" {
				migratedHTTP = isPlainHTTP(cfg.Notify.WebhookURL)
				if err := v.Put(key, []byte(cfg.Notify.WebhookURL)); err != nil {
					return nil, WithHint(fmt.Errorf("store the url of channel %s: %w", in.ID, err), "")
				}
			}
		}
		if !f.Required && slices.Contains(in.ClearSecrets, f.Name) && strings.TrimSpace(in.Secrets[f.Name]) == "" {
			if err := v.Delete(key); err != nil && !errors.Is(err, vault.ErrNotFound) {
				return nil, err
			}
		}
	}
	ch := in.channel()
	if migratedHTTP && ch.Type == config.ChannelWebhook {
		// The old address was http, which older versions accepted: keep it working,
		// as an explicit exception that the doctor keeps warning about.
		ch.AllowHTTP = true
	}
	if found && existing != nil {
		*existing = ch
	} else {
		cfg.Notify.Channels = append(cfg.Notify.Channels, ch)
	}
	if legacy {
		_ = v.Delete(notify.VaultKey)
		cfg.Notify.WebhookURL = ""
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("the config would be invalid: %w", err)
	}
	if err := cfg.Save(path); err != nil {
		return nil, err
	}
	return nil, nil
}

// RemoveChannel deletes a channel and its secrets. It reports whether there was one.
func (o *Ops) RemoveChannel(ctx context.Context, id string) (bool, error) {
	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	cfg, path, err := o.LoadConfigOrDefault()
	if err != nil {
		return false, err
	}
	removed := false
	typ := ""
	for i, c := range cfg.Notify.Channels {
		if c.ID == id {
			typ = c.Type
			cfg.Notify.Channels = append(cfg.Notify.Channels[:i], cfg.Notify.Channels[i+1:]...)
			removed = true
			break
		}
	}
	v, err := o.OpenVault(cfg)
	if err != nil {
		return false, err
	}
	if id == notify.LegacyChannelID {
		typ = config.ChannelWebhook
		if err := v.Delete(notify.VaultKey); err == nil {
			removed = true
		} else if !errors.Is(err, vault.ErrNotFound) {
			return false, err
		}
		if cfg.Notify.WebhookURL != "" {
			cfg.Notify.WebhookURL = ""
			removed = true
		}
	}
	if !removed {
		return false, nil
	}
	for _, f := range config.ChannelSecrets(typ) {
		if err := v.Delete(notify.ChannelSecretKey(id, f.Name)); err != nil && !errors.Is(err, vault.ErrNotFound) {
			return false, err
		}
	}
	if err := cfg.Save(path); err != nil {
		return false, err
	}
	forgetChannelResult(id)
	return true, nil
}

// TestResult is how a test message went through one channel.
type TestResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// TestChannels sends a test message through the channel with the given id, or
// through every channel when id is empty. A channel that fails does not stop
// the others.
func (o *Ops) TestChannels(ctx context.Context, id string) ([]TestResult, error) {
	return o.TestChannelsWith(ctx, id, TestOptions{})
}

// TestOptions choose what a test sends: the plain test message, a sample of
// one event kind, or a sample quiet-hours digest of Digest items.
type TestOptions struct {
	Event  string
	Digest int
}

// TestChannelsWith is TestChannels with a sample event or digest. It goes
// through the real send path (splitting, escaping) and touches no alert state.
func (o *Ops) TestChannelsWith(ctx context.Context, id string, opt TestOptions) ([]TestResult, error) {
	if opt.Event != "" && opt.Digest > 0 {
		return nil, errors.New("use either --event or --digest, not both")
	}
	if opt.Event != "" && !slices.Contains(notify.SampleKinds(), opt.Event) {
		return nil, fmt.Errorf("unknown event %q (use %s)", opt.Event, strings.Join(notify.SampleKinds(), ", "))
	}
	if opt.Digest < 0 || opt.Digest > 500 {
		return nil, errors.New("--digest takes a number from 1 to 500")
	}
	cfg, path, err := o.LoadConfigOrDefault()
	if err != nil {
		return nil, err
	}
	r := o.Router(cfg, path, nil, false)
	targets, err := r.Targets()
	if err != nil {
		return nil, WithHint(err, "")
	}
	var ids []string
	for _, t := range targets {
		if id == "" || t.Channel.ID == id {
			ids = append(ids, t.Channel.ID)
		}
	}
	if len(ids) == 0 {
		if id != "" {
			return nil, fmt.Errorf("no channel named %q\nrun: e5extend notify channel list", id)
		}
		return nil, errors.New("no notification channel is set; run: e5extend notify channel add <id> --type <type>")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*notify.DefaultTimeout)
	defer cancel()
	ev := notify.Event{Event: notify.Test, Message: "this is a test alert; alerts are working", Time: time.Now(), IsTest: true}
	switch {
	case opt.Digest > 0:
		ev = notify.SampleDigest(opt.Digest, time.Now())
	case opt.Event == notify.Digest:
		ev = notify.SampleDigest(2, time.Now())
	case opt.Event != "":
		ev = notify.SampleEvent(opt.Event, time.Now())
	}
	ev.IsTest = true
	out := make([]TestResult, len(ids))
	var wg sync.WaitGroup
	for i, cid := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := r.SendTo(ctx, cid, ev)
			out[i] = TestResult{ID: cid, OK: err == nil}
			if err != nil {
				out[i].Error = firstLine(err.Error())
			}
		}()
	}
	wg.Wait()
	return out, nil
}

// Preview is a message rendered with example data.
type Preview struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	HTML  string `json:"html"`
	// Format names what Body is: html, markdown, text, json.
	Format string `json:"format"`
}

// PreviewFormat is the format of the message body of a channel type.
func PreviewFormat(typ string) string {
	switch typ {
	case config.ChannelTelegram:
		return "html"
	case config.ChannelWeCom, config.ChannelDingTalk, config.ChannelDiscord, config.ChannelSlack, config.ChannelServerChan:
		return "markdown"
	case config.ChannelWebhook:
		return "json"
	}
	return "text"
}

// PreviewMessage renders an example event for a channel type and language
// without sending anything. A custom template is checked first. An empty
// language means the one the app uses.
func (o *Ops) PreviewMessage(typ, lang, template, format, event string) (Preview, error) {
	if !config.ValidChannelType(typ) {
		return Preview{}, fmt.Errorf("unknown channel type %q (use %s)", typ, strings.Join(config.ChannelTypes, ", "))
	}
	if !slices.Contains(notify.SampleKinds(), event) {
		return Preview{}, fmt.Errorf("unknown event %q (use %s)", event, strings.Join(notify.SampleKinds(), ", "))
	}
	isTemplateHook := typ == config.ChannelWebhook && format == config.FormatTemplate
	if err := notify.CheckTemplate(typ, template, isTemplateHook); err != nil {
		return Preview{}, err
	}
	lang = notify.Language(lang, defaultLanguage())
	ev := notify.SampleEvent(event, time.Now())
	host, _ := os.Hostname()
	if typ == config.ChannelWebhook && !isTemplateHook {
		b, err := json.MarshalIndent(ev, "", "  ")
		if err != nil {
			return Preview{}, err
		}
		return Preview{Body: string(b), Format: "json"}, nil
	}
	r, err := notify.Render(typ, lang, template, notify.NewData(ev, lang, host, nil))
	if err != nil {
		return Preview{}, err
	}
	return Preview{Title: r.Title, Body: r.Body, HTML: r.HTML, Format: PreviewFormat(typ)}, nil
}

func isPlainHTTP(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Scheme == "http" && !notify.IsLoopback(u.Hostname())
}

// ---- the single webhook of older versions ----

// ValidWebhook checks that raw looks like an https address (or a local http one).
func ValidWebhook(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && notify.IsLoopback(u.Hostname()))) {
		// The text is not echoed: it may be a pasted address with a token.
		return errors.New("the webhook address must be an https:// URL")
	}
	return nil
}

// WebhookConfigured says whether the channel "webhook" exists. The address
// itself is never returned.
func (o *Ops) WebhookConfigured() bool {
	chs, err := o.ListChannels()
	if err != nil {
		return false
	}
	return slices.ContainsFunc(chs, func(c ChannelView) bool { return c.ID == notify.LegacyChannelID })
}

// SetWebhook stores the address as the url of the channel "webhook", creating
// the channel when needed, and removes the older storage. clearedPlain says
// whether a plaintext notify.webhook_url was removed from config.toml.
func (o *Ops) SetWebhook(ctx context.Context, raw string) (clearedPlain bool, err error) {
	raw = strings.TrimSpace(raw)
	if err := ValidWebhook(raw); err != nil {
		return false, err
	}
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return false, err
	}
	clearedPlain = cfg.Notify.WebhookURL != ""
	in := ChannelInput{ID: notify.LegacyChannelID, Type: config.ChannelWebhook, Secrets: map[string]string{"url": raw}}
	if cur, ok := cfg.Notify.FindChannel(notify.LegacyChannelID); ok {
		in = inputOf(*cur)
		in.Secrets = map[string]string{"url": raw}
	}
	_, exists := cfg.Notify.FindChannel(notify.LegacyChannelID)
	if !exists {
		// An older entry counts as existing: saving replaces it.
		exists = o.WebhookConfigured()
	}
	bad, err := o.SaveChannel(ctx, in, !exists)
	if err != nil {
		return false, err
	}
	if len(bad) > 0 {
		return false, errors.New(bad[0].Error())
	}
	// Whatever the old storage held is gone now.
	if err := o.dropLegacyWebhook(ctx); err != nil {
		return false, err
	}
	return clearedPlain, nil
}

// dropLegacyWebhook removes notify/webhook and a plaintext notify.webhook_url.
func (o *Ops) dropLegacyWebhook(ctx context.Context) error {
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return err
	}
	if v, err := o.OpenVault(cfg); err == nil {
		if err := v.Delete(notify.VaultKey); err != nil && !errors.Is(err, vault.ErrNotFound) {
			return err
		}
	}
	_, err = o.clearPlainWebhook(ctx)
	return err
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

// ClearWebhook removes the channel "webhook". It reports whether there was one.
func (o *Ops) ClearWebhook(ctx context.Context) (removed bool, err error) {
	return o.RemoveChannel(ctx, notify.LegacyChannelID)
}

// TestWebhook sends a test alert through the channel "webhook".
func (o *Ops) TestWebhook(ctx context.Context) error {
	if !o.WebhookConfigured() {
		return errors.New("no webhook is set; run: e5extend notify set-webhook <url>")
	}
	res, err := o.TestChannels(ctx, notify.LegacyChannelID)
	if err != nil {
		return err
	}
	if len(res) == 1 && !res[0].OK {
		return WithHint(fmt.Errorf("the test alert was not delivered: %s", res[0].Error), "")
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
