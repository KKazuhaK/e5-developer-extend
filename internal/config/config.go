// Package config defines the on-disk configuration (config.toml). The file
// never contains secrets; those live in the vault.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
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
	Version  int      `toml:"version"`
	Schedule Schedule `toml:"schedule"`
	Network  Network  `toml:"network"`
	Notify   Notify   `toml:"notify"`
	// Subscription selects which subscriptions the expiry check watches.
	Subscription Subscription `toml:"subscription"`
	Vault        Vault        `toml:"vault"`
	Accounts     []Account    `toml:"accounts"`
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
	// WebhookURL is the legacy plaintext location; `e5extend notify set-webhook`
	// stores the address in the vault instead.
	WebhookURL string `toml:"webhook_url"`
	// StaleAfter is how long an account may go without a successful call
	// before a "stale" alert is sent.
	StaleAfter string `toml:"stale_after"`
	// Events lists the alert kinds to send; empty means all of them.
	Events []string `toml:"events"`
	// Cooldown is how long the same alert for the same account is not sent
	// again; critical alerts use half of it. "0" turns it off.
	Cooldown string `toml:"cooldown"`
	// QuietHours is a local time window such as "23:00-08:00"; during it only
	// critical alerts are sent and the rest is sent as one summary afterwards.
	// Empty turns it off.
	QuietHours string `toml:"quiet_hours"`
	// SpikeRounds, SpikeRate and SpikeMinFailures define a failure_spike:
	// at least SpikeRate of the calls and at least SpikeMinFailures calls
	// failed over the last SpikeRounds rounds.
	SpikeRounds      int     `toml:"spike_rounds"`
	SpikeRate        float64 `toml:"spike_rate"`
	SpikeMinFailures int     `toml:"spike_min_failures"`
	// Channels are the destinations. Their secrets live in the vault.
	Channels []Channel `toml:"channels"`
}

// NotifyEvents are the alert kinds accepted in Notify.Events.
var NotifyEvents = []string{"needs_attention", "recovered", "auto_paused", "stale", "secret_expiring", "period_ended", "failure_spike", "all_failing",
	"subscription_expiring", "subscription_status", "subscription_renewed"}

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
	// SubscriptionExpires (YYYY-MM-DD) is the manual fallback for the expiry of
	// the tenant's E5 subscription, used while it cannot be read from Graph.
	// It belongs to the tenant: it is written on one account of the tenant.
	SubscriptionExpires string `toml:"subscription_expires"`
}

// Subscription holds the subscription expiry check settings.
type Subscription struct {
	// Skus lists the SKU part numbers to watch; an entry matches every SKU
	// that starts with it (case-insensitive). Empty means DefaultSubscriptionSKU.
	Skus []string `toml:"skus"`
}

// DefaultSubscriptionSKU is the SKU prefix of E5 developer subscriptions.
const DefaultSubscriptionSKU = "DEVELOPERPACK"

// SKUPrefixes returns the watched SKU prefixes, upper-cased.
func (s Subscription) SKUPrefixes() []string {
	var out []string
	for _, p := range s.Skus {
		if p = strings.ToUpper(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{DefaultSubscriptionSKU}
	}
	return out
}

// Watches reports whether a subscription with this SKU part number is watched.
func (s Subscription) Watches(sku string) bool {
	sku = strings.ToUpper(sku)
	for _, p := range s.SKUPrefixes() {
		if strings.HasPrefix(sku, p) {
			return true
		}
	}
	return false
}

// TenantKey names the tenant of an account for grouping: the configured
// tenant, lower-cased. The multi-tenant endpoints (organizations, common,
// consumers) do not name one tenant, so such an account stands alone.
func (a Account) TenantKey() string {
	t := strings.ToLower(strings.TrimSpace(a.Tenant))
	switch t {
	case "", "common", "organizations", "consumers":
		return "account:" + a.Name
	}
	return t
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
			WorkDays:        []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"},
			WorkStart:       "00:00",
			WorkEnd:         "00:00", // equal to WorkStart: all day
		},
		Notify: Notify{
			StaleAfter: DefaultStaleAfter, Events: []string{}, Cooldown: DefaultCooldown,
			SpikeRounds: DefaultSpikeRounds, SpikeRate: DefaultSpikeRate, SpikeMinFailures: DefaultSpikeMinFailures,
			Channels: []Channel{},
		},
		Subscription: Subscription{Skus: []string{}},
		Vault:        Vault{Backend: BackendAuto},
		Accounts:     []Account{},
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
func Parse(data []byte) (*Config, error) { return parse(data, nil) }

// ParseWithVault is Parse for a file whose own [vault] section is not wanted:
// v is used in its place, before validation, so the file's backend is neither
// checked nor kept.
func ParseWithVault(data []byte, v Vault) (*Config, error) { return parse(data, &v) }

// VaultSetting reads only the [vault] section of config text, however invalid
// the rest of it is. ok is false when it cannot be read or names an unknown backend.
func VaultSetting(data []byte) (v Vault, ok bool) {
	var raw struct {
		Vault Vault `toml:"vault"`
	}
	raw.Vault = Vault{Backend: BackendAuto}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return Vault{}, false
	}
	switch raw.Vault.Backend {
	case BackendAuto, BackendKeyring, BackendFile:
		return raw.Vault, true
	}
	return Vault{}, false
}

func parse(data []byte, override *Vault) (*Config, error) {
	// Read only the version first so a newer file gets a clear message
	// instead of unknown-field errors.
	var head struct {
		Version *int `toml:"version"`
	}
	if err := toml.Unmarshal(data, &head); err != nil {
		return nil, decodeError(err)
	}
	if head.Version != nil && *head.Version > Version {
		return nil, fmt.Errorf("config version %d is newer than this e5extend supports (%d); upgrade e5extend", *head.Version, Version)
	}

	cfg := Default()
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, decodeError(err)
	}
	if override != nil {
		cfg.Vault = *override
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
const Header = "# Managed by e5extend: this file is rewritten when accounts change, so comments are not kept.\n" +
	"# Print the commented template with: e5extend config init --print-template\n\n"

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
