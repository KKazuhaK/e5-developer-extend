// Package config defines the on-disk configuration (config.toml). The file
// never contains secrets; those live in the vault.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/KKazuhaK/e5-renew/internal/paths"
)

// Version is the current configuration format version.
const Version = 1

// ErrNotFound is returned by Load when the file does not exist.
var ErrNotFound = errors.New("config file not found")

// Authentication modes.
const (
	AuthDeviceCode   = "device_code"
	AuthBrowser      = "browser"
	AuthClientSecret = "client_secret"
	AuthPassword     = "password"
)

// Vault backends.
const (
	BackendAuto    = "auto"
	BackendKeyring = "keyring"
	BackendFile    = "file"
)

// Config is the root of config.toml.
type Config struct {
	Version  int       `toml:"version"`
	Schedule Schedule  `toml:"schedule"`
	Network  Network   `toml:"network"`
	Notify   Notify    `toml:"notify"`
	Vault    Vault     `toml:"vault"`
	Accounts []Account `toml:"accounts"`
}

// Schedule controls when and how much the engine runs.
type Schedule struct {
	IntervalMin     string          `toml:"interval_min"`
	IntervalMax     string          `toml:"interval_max"`
	APIsPerRoundMin int             `toml:"apis_per_round_min"`
	APIsPerRoundMax int             `toml:"apis_per_round_max"`
	RandomOrder     bool            `toml:"random_order"`
	WorkDays        []string        `toml:"work_days"`
	WorkStart       string          `toml:"work_start"`
	WorkEnd         string          `toml:"work_end"`
	PeriodStart     *toml.LocalDate `toml:"period_start,omitempty"`
	PeriodEnd       *toml.LocalDate `toml:"period_end,omitempty"`
}

// Network holds outbound HTTP settings.
type Network struct {
	Proxy string `toml:"proxy"`
}

// Notify holds notification settings.
type Notify struct {
	// WebhookURL is the legacy plaintext location; `e5renew notify set-webhook`
	// stores the address in the vault instead.
	WebhookURL string `toml:"webhook_url"`
	// StaleAfter is how long an account may go without a successful call
	// before a "stale" alert is sent.
	StaleAfter string `toml:"stale_after"`
	// Events lists the alert kinds to send; empty means all of them.
	Events []string `toml:"events"`
}

// NotifyEvents are the alert kinds accepted in Notify.Events.
var NotifyEvents = []string{"needs_attention", "recovered", "auto_paused", "stale", "secret_expiring", "period_ended"}

// DefaultStaleAfter is the default for Notify.StaleAfter.
const DefaultStaleAfter = "72h"

// Vault selects where the vault master key comes from.
type Vault struct {
	Backend string `toml:"backend"`
}

// Account is one Microsoft 365 identity.
type Account struct {
	Name          string   `toml:"name"`
	Auth          string   `toml:"auth"`
	Tenant        string   `toml:"tenant"`
	ClientID      string   `toml:"client_id"`
	Username      string   `toml:"username"`
	Categories    []string `toml:"categories"`
	Paused        bool     `toml:"paused"`
	SecretExpires string   `toml:"secret_expires"`
	HomeAccountID string   `toml:"home_account_id"`
}

// Delegated reports whether the account signs in as a user.
func (a Account) Delegated() bool { return a.Auth != AuthClientSecret }

// NeedsSecret reports whether a secret or password is stored in the vault.
func (a Account) NeedsSecret() bool {
	return a.Auth == AuthClientSecret || a.Auth == AuthPassword
}

// Default returns a configuration with default values and no accounts.
func Default() *Config {
	return &Config{
		Version: Version,
		Schedule: Schedule{
			IntervalMin:     "40m",
			IntervalMax:     "2h",
			APIsPerRoundMin: 3,
			APIsPerRoundMax: 8,
			RandomOrder:     true,
			WorkDays:        []string{"mon", "tue", "wed", "thu", "fri"},
			WorkStart:       "08:00",
			WorkEnd:         "22:00",
		},
		Notify:   Notify{StaleAfter: DefaultStaleAfter, Events: []string{}},
		Vault:    Vault{Backend: BackendAuto},
		Accounts: []Account{},
	}
}

// Find returns the account with the given name.
func (c *Config) Find(name string) (*Account, bool) {
	for i := range c.Accounts {
		if c.Accounts[i].Name == name {
			return &c.Accounts[i], true
		}
	}
	return nil, false
}

// Remove deletes the named account and reports whether it existed.
func (c *Config) Remove(name string) bool {
	for i := range c.Accounts {
		if c.Accounts[i].Name == name {
			c.Accounts = append(c.Accounts[:i], c.Accounts[i+1:]...)
			return true
		}
	}
	return false
}

// Parse decodes TOML over the defaults and validates the result.
func Parse(data []byte) (*Config, error) {
	// Read only the version first so a newer file gets a clear message
	// instead of unknown-field errors.
	var head struct {
		Version *int `toml:"version"`
	}
	if err := toml.Unmarshal(data, &head); err != nil {
		return nil, decodeError(err)
	}
	if head.Version != nil && *head.Version > Version {
		return nil, fmt.Errorf("config version %d is newer than this e5renew supports (%d); upgrade e5renew", *head.Version, Version)
	}

	cfg := Default()
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, decodeError(err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func decodeError(err error) error {
	var derr *toml.DecodeError
	if errors.As(err, &derr) {
		row, col := derr.Position()
		return fmt.Errorf("line %d, column %d: %w", row, col, err)
	}
	var serr *toml.StrictMissingError
	if errors.As(err, &serr) {
		return fmt.Errorf("unknown field: %s", serr.String())
	}
	return err
}

// Load reads, defaults and validates the file at path. If the file parses but
// fails validation, the parsed config is returned together with the joined
// validation errors.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil && cfg == nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// Validation errors are returned as is, one line per problem.
	return cfg, err
}

// Header is written at the top of every saved file.
const Header = "# Managed by e5renew: this file is rewritten when accounts change, so comments are not kept.\n" +
	"# Print the commented template with: e5renew config init --print-template\n\n"

// Save writes the configuration atomically with mode 0600, preceded by Header.
func (c *Config) Save(path string) error {
	data, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return paths.WriteFileAtomic(path, append([]byte(Header), data...))
}

// Intervals returns the parsed interval bounds. Valid after Validate.
func (s Schedule) Intervals() (min, max time.Duration, err error) {
	if min, err = time.ParseDuration(s.IntervalMin); err != nil {
		return 0, 0, err
	}
	max, err = time.ParseDuration(s.IntervalMax)
	return min, max, err
}

// StaleDuration returns the parsed Notify.StaleAfter. Valid after Validate.
func (n Notify) StaleDuration() time.Duration {
	d, err := time.ParseDuration(n.StaleAfter)
	if err != nil || d <= 0 {
		d, _ = time.ParseDuration(DefaultStaleAfter)
	}
	return d
}
