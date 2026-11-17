package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
)

// SettingsService reads and writes the settings and the alert webhook.
type SettingsService struct {
	b *Backend
	// uiMu serializes gui.json updates.
	uiMu sync.Mutex
}

// NewSettingsService returns the service.
func NewSettingsService(b *Backend) *SettingsService { return &SettingsService{b: b} }

// uiFile is the name of the interface settings file in the config directory.
const uiFile = "gui.json"

type uiFileData struct {
	Language string `json:"language"`
	Theme    string `json:"theme"`
}

func (s *SettingsService) uiPath() (string, error) {
	dir := s.b.ConfigDir
	if dir == "" {
		d, err := paths.ConfigDir()
		if err != nil {
			return "", err
		}
		dir = d
	}
	return filepath.Join(dir, uiFile), nil
}

// ReadUI returns the interface settings of gui.json; it is also what the tray reads.
func ReadUI(path string) UISettings {
	ui := UISettings{Language: "auto", Theme: "system"}
	b, err := os.ReadFile(path)
	if err != nil {
		return ui
	}
	var f uiFileData
	if json.Unmarshal(b, &f) != nil {
		return ui
	}
	if validLang(f.Language) {
		ui.Language = f.Language
	}
	if validTheme(f.Theme) {
		ui.Theme = f.Theme
	}
	return ui
}

func validLang(s string) bool  { return s == "auto" || s == "zh" || s == "en" }
func validTheme(s string) bool { return s == "system" || s == "light" || s == "dark" }

// Get returns every setting.
func (s *SettingsService) Get() (Settings, error) {
	base, err := s.b.Ops.LoadSettings()
	if err != nil {
		return Settings{}, err
	}
	p, err := s.uiPath()
	if err != nil {
		return Settings{}, err
	}
	ui := ReadUI(p)
	if s.b.Autostart != nil {
		if on, err := s.b.Autostart.Enabled(); err == nil {
			ui.StartAtLogin = on
		}
	}
	base.Network.Proxy = redactProxy(base.Network.Proxy)
	return Settings{Schedule: base.Schedule, Network: base.Network, Notify: base.Notify, UI: ui}, nil
}

// SetStartAtLogin turns the login entry of the tray app on or off at once.
// Save leaves it alone: the setting lives in the system, not in the files.
func (s *SettingsService) SetStartAtLogin(on bool) error {
	if s.b.Autostart == nil {
		return nil
	}
	cur, err := s.b.Autostart.Enabled()
	if err == nil && cur != on {
		err = s.b.Autostart.Set(on)
	}
	if err != nil {
		return fmt.Errorf("start at login: %w", err)
	}
	return nil
}

// Save checks and stores the settings. When a value is not valid nothing is
// saved and the problems are returned, one per field. The webhook is not
// part of it; it has its own methods. Start at login is not either: it is
// applied at once by SetStartAtLogin. A proxy address that still has the
// hidden password Get showed is taken to mean "unchanged".
func (s *SettingsService) Save(in Settings) ([]ValidationError, error) {
	var bad []ValidationError
	if !validLang(in.UI.Language) {
		bad = append(bad, ValidationError{Field: "ui.language", Message: "must be auto, zh or en"})
	}
	if !validTheme(in.UI.Theme) {
		bad = append(bad, ValidationError{Field: "ui.theme", Message: "must be system, light or dark"})
	}
	if len(bad) > 0 {
		return bad, nil
	}
	if stored, err := s.b.Ops.LoadSettings(); err == nil && in.Network.Proxy != "" &&
		in.Network.Proxy == redactProxy(stored.Network.Proxy) {
		in.Network.Proxy = stored.Network.Proxy
	}
	bad, err := s.b.Ops.SaveSettings(context.Background(), opsSettings(in))
	if err != nil {
		return nil, err
	}
	if len(bad) > 0 {
		return bad, nil
	}

	s.b.Ops.ReloadEngine()
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	p, err := s.uiPath()
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(uiFileData{Language: in.UI.Language, Theme: in.UI.Theme}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := paths.EnsureDir(filepath.Dir(p)); err != nil {
		return nil, err
	}
	if err := paths.WriteFileAtomic(p, append(data, '\n')); err != nil {
		return nil, err
	}
	return nil, nil
}

// redactProxy hides the password of the user info in a proxy address.
func redactProxy(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	return u.Redacted()
}

// ListChannels returns the notification channels. Secrets are shown only as set or not set.
func (s *SettingsService) ListChannels() ([]ChannelView, error) { return s.b.Ops.ListChannels() }

// SaveChannel creates (create true) or updates a channel. The secrets in
// in.Secrets are stored in the vault and never returned; a missing or empty
// one keeps what is stored. When a value is not valid nothing is saved and
// the problems are returned, one per field.
func (s *SettingsService) SaveChannel(in ChannelInput, create bool) ([]ValidationError, error) {
	bad, err := s.b.Ops.SaveChannel(context.Background(), in, create)
	if err != nil || len(bad) > 0 {
		return bad, err
	}
	s.b.Ops.ReloadEngine()
	return nil, nil
}

// RemoveChannel deletes a channel together with its stored secrets.
func (s *SettingsService) RemoveChannel(id string) error {
	if _, err := s.b.Ops.RemoveChannel(context.Background(), id); err != nil {
		return err
	}
	s.b.Ops.ReloadEngine()
	return nil
}

// TestChannel sends a test message through one channel, or through all of
// them when id is empty, and reports each result.
func (s *SettingsService) TestChannel(id string) ([]ChannelTest, error) {
	return s.b.Ops.TestChannels(context.Background(), id)
}

// PreviewMessage renders an example event for a channel type, with an
// optional custom template, without sending anything.
func (s *SettingsService) PreviewMessage(typ, language, template, format, event string) (MessagePreview, error) {
	return s.b.Ops.PreviewMessage(typ, language, template, format, event)
}

// EmailPresets lists the mail providers whose server settings can be filled in.
func (s *SettingsService) EmailPresets() []EmailPreset { return notify.EmailPresets() }

// SetWebhook stores the alert address as the channel "webhook". It is not shown again.
func (s *SettingsService) SetWebhook(url string) error {
	_, err := s.b.Ops.SetWebhook(context.Background(), url)
	return err
}

// ClearWebhook removes the stored alert address.
func (s *SettingsService) ClearWebhook() error {
	_, err := s.b.Ops.ClearWebhook(context.Background())
	return err
}

// TestWebhook sends a test alert.
func (s *SettingsService) TestWebhook() error { return s.b.Ops.TestWebhook(context.Background()) }

func opsSettings(in Settings) ops.Settings {
	return ops.Settings{Schedule: in.Schedule, Network: in.Network, Notify: in.Notify}
}
