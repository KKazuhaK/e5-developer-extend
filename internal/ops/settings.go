package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
)

// ConfigTemplate is the commented config.toml that `config init` writes.
const ConfigTemplate = `# e5extend configuration. Secrets are never stored here; they live in the vault.
version = 1

[schedule]
interval_min = "40m"        # lower bound of the random pause between rounds
interval_max = "2h"         # upper bound
apis_per_round_min = 3
apis_per_round_max = 8
random_order = true
work_days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]   # every day; e.g. ["mon", "tue", "wed", "thu", "fri"] for weekdays only
work_start = "08:00"        # local time; start == end means all day
work_end = "22:00"
# period_start = 2026-12-01 # optional run period
# period_end = 2027-12-01

[network]
proxy = ""                  # empty uses HTTPS_PROXY and friends

[notify]
# Add destinations with: e5extend notify channel add <id> --type telegram|email|wecom|feishu|dingtalk|discord|slack|bark|ntfy|serverchan|webhook
# Their tokens and addresses are kept in the vault, not here.
webhook_url = ""            # legacy plaintext address; set-webhook moves it into the vault
stale_after = "72h"         # alert when an account has had no successful call for this long
events = []                 # empty means all: needs_attention, recovered, auto_paused, stale, secret_expiring, period_ended, failure_spike, all_failing,
                            # subscription_expiring, subscription_status, subscription_renewed, daily_summary
cooldown = "6h"             # the same alert for the same account is not repeated within this time (critical: half); empty means the default, only "0" turns it off
quiet_hours = ""            # for example "23:00-08:00": only critical alerts, the rest is sent as one summary afterwards
daily_summary_at = ""       # for example "21:00": send one message a day with the calls of each account; empty turns it off (inside the quiet hours: sent when they end)
spike_rounds = 3            # failure_spike: look at this many recent rounds ...
spike_rate = 0.5            # ... alert when this share of the calls failed ...
spike_min_failures = 5      # ... and at least this many calls failed (calls that found nothing do not count)

[subscription]
skus = []                   # SKU part numbers to watch (each matches by prefix); empty means DEVELOPERPACK, the E5 developer subscription

[vault]
backend = "auto"            # auto | keyring | file

# Add accounts with: e5extend account add <name> --auth ... --tenant ... --client-id ...
# If the subscription cannot be read from Microsoft (Organization.Read.All is missing), the end date can be
# written on one account of the tenant:  subscription_expires = "2027-02-14"
`

// ConfigPaths returns the config file and the data directory.
func (o *Ops) ConfigPaths() (file, dataDir string, err error) {
	if file, err = paths.ConfigFile(); err != nil {
		return "", "", err
	}
	if dataDir, err = paths.DataDir(); err != nil {
		return "", "", err
	}
	return file, dataDir, nil
}

// ConfigInit creates the default config.toml. An existing file is left alone
// and created is false.
func (o *Ops) ConfigInit(ctx context.Context) (path string, created bool, err error) {
	if path, err = paths.ConfigFile(); err != nil {
		return "", false, err
	}
	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return path, false, err
	}
	defer unlock()
	if _, err := os.Stat(path); err == nil {
		return path, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return path, false, err
	}
	if _, err := config.Parse([]byte(ConfigTemplate)); err != nil {
		return path, false, fmt.Errorf("built-in template is invalid: %w", err)
	}
	if err := paths.WriteFileAtomic(path, []byte(ConfigTemplate)); err != nil {
		return path, false, err
	}
	return path, true, nil
}

// ConfigReport is the result of checking the config file.
type ConfigReport struct {
	Path     string
	Accounts int
	// Problems lists every validation problem, one per line; empty when valid.
	Problems []string
}

// ValidateConfig checks the config file. An unreadable or missing file is an
// error; validation problems are reported in the result.
func (o *Ops) ValidateConfig() (ConfigReport, error) {
	path, err := paths.ConfigFile()
	if err != nil {
		return ConfigReport{}, err
	}
	rep := ConfigReport{Path: path}
	cfg, err := config.Load(path)
	if errors.Is(err, config.ErrNotFound) {
		return rep, fmt.Errorf("%w\nrun: e5extend config init", err)
	}
	if err != nil {
		if cfg == nil {
			return rep, err
		}
		for _, e := range splitErrors(err) {
			rep.Problems = append(rep.Problems, e.Error())
		}
		return rep, nil
	}
	rep.Accounts = len(cfg.Accounts)
	return rep, nil
}

func splitErrors(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}

// ScheduleSettings are the scheduling options. Durations are Go duration
// strings ("40m"), times are HH:MM and the period dates are YYYY-MM-DD ("" for none).
type ScheduleSettings struct {
	IntervalMin     string   `json:"intervalMin"`
	IntervalMax     string   `json:"intervalMax"`
	APIsPerRoundMin int      `json:"apisPerRoundMin"`
	APIsPerRoundMax int      `json:"apisPerRoundMax"`
	RandomOrder     bool     `json:"randomOrder"`
	WorkDays        []string `json:"workDays"`
	WorkStart       string   `json:"workStart"`
	WorkEnd         string   `json:"workEnd"`
	PeriodStart     string   `json:"periodStart"`
	PeriodEnd       string   `json:"periodEnd"`
}

// NetworkSettings are the outbound HTTP options.
type NetworkSettings struct {
	Proxy string `json:"proxy"`
}

// NotifySettings are the alert options. The address itself is never part of
// the settings; only whether one is stored.
type NotifySettings struct {
	StaleAfter string   `json:"staleAfter"`
	Events     []string `json:"events"`
	// WebhookConfigured says the channel "webhook" exists; Channels counts all of them.
	// Both are read-only: channels are edited with their own methods.
	WebhookConfigured bool `json:"webhookConfigured"`
	Channels          int  `json:"channels"`
	// Cooldown is a duration ("6h"; "0" turns it off), QuietHours a window such as
	// "23:00-08:00" (empty turns it off); the spike values define failure_spike.
	Cooldown   string `json:"cooldown"`
	QuietHours string `json:"quietHours"`
	// DailySummaryAt is the local time (HH:MM) of the daily summary message; empty turns it off.
	DailySummaryAt   string  `json:"dailySummaryAt"`
	SpikeRounds      int     `json:"spikeRounds"`
	SpikeRate        float64 `json:"spikeRate"`
	SpikeMinFailures int     `json:"spikeMinFailures"`
}

// Settings is the part of the configuration that is not about accounts.
type Settings struct {
	Schedule ScheduleSettings `json:"schedule"`
	Network  NetworkSettings  `json:"network"`
	Notify   NotifySettings   `json:"notify"`
}

// ValidationError is one problem with a saved value. Field is the camelCase
// path of the setting, for example "schedule.intervalMin".
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e ValidationError) Error() string { return e.Field + ": " + e.Message }

const dateLayout = "2006-01-02"

func dateString(d *toml.LocalDate) string {
	if d == nil {
		return ""
	}
	return d.String()
}

// LoadSettings returns the settings from the config file, or the defaults when
// there is no file yet.
func (o *Ops) LoadSettings() (Settings, error) {
	cfg, path, err := o.LoadConfigOrDefault()
	if err != nil {
		return Settings{}, err
	}
	return Settings{
		Schedule: ScheduleSettings{
			IntervalMin: cfg.Schedule.IntervalMin, IntervalMax: cfg.Schedule.IntervalMax,
			APIsPerRoundMin: cfg.Schedule.APIsPerRoundMin, APIsPerRoundMax: cfg.Schedule.APIsPerRoundMax,
			RandomOrder: cfg.Schedule.RandomOrder, WorkDays: nonNil(cfg.Schedule.WorkDays),
			WorkStart: cfg.Schedule.WorkStart, WorkEnd: cfg.Schedule.WorkEnd,
			PeriodStart: dateString(cfg.Schedule.PeriodStart), PeriodEnd: dateString(cfg.Schedule.PeriodEnd),
		},
		Network: NetworkSettings{Proxy: cfg.Network.Proxy},
		Notify:  o.notifySettings(cfg, path),
	}, nil
}

func (o *Ops) notifySettings(cfg *config.Config, path string) NotifySettings {
	rounds, rate, minFailures := cfg.Notify.SpikeSettings()
	n := NotifySettings{
		StaleAfter: cfg.Notify.StaleAfter, Events: nonNil(cfg.Notify.Events),
		Cooldown: orDefault(cfg.Notify.Cooldown, config.DefaultCooldown), QuietHours: cfg.Notify.QuietHours,
		DailySummaryAt: cfg.Notify.DailySummaryAt,
		SpikeRounds:    rounds, SpikeRate: rate, SpikeMinFailures: minFailures,
	}
	src := o.channelSource(cfg, path)
	src.mu.Lock()
	entries := src.entriesLocked()
	src.mu.Unlock()
	n.Channels = len(entries)
	for _, e := range entries {
		n.WebhookConfigured = n.WebhookConfigured || e.ID == notify.LegacyChannelID
	}
	return n
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func parseDate(field, s string) (*toml.LocalDate, *ValidationError) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return nil, &ValidationError{Field: field, Message: fmt.Sprintf("not a YYYY-MM-DD date: %q", s)}
	}
	return &toml.LocalDate{Year: t.Year(), Month: int(t.Month()), Day: t.Day()}, nil
}

// SaveSettings validates s and writes it into the config file, keeping the
// accounts, the vault choice and the legacy webhook entry. When s is not
// valid nothing is written and the problems are returned; err is for
// failures that are not about the values.
func (o *Ops) SaveSettings(ctx context.Context, s Settings) ([]ValidationError, error) {
	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	cfg, path, err := o.LoadConfigOrDefault()
	if err != nil {
		return nil, err
	}
	var bad []ValidationError
	ps, e1 := parseDate("schedule.periodStart", s.Schedule.PeriodStart)
	pe, e2 := parseDate("schedule.periodEnd", s.Schedule.PeriodEnd)
	for _, e := range []*ValidationError{e1, e2} {
		if e != nil {
			bad = append(bad, *e)
		}
	}
	cfg.Schedule = config.Schedule{
		IntervalMin: strings.TrimSpace(s.Schedule.IntervalMin), IntervalMax: strings.TrimSpace(s.Schedule.IntervalMax),
		APIsPerRoundMin: s.Schedule.APIsPerRoundMin, APIsPerRoundMax: s.Schedule.APIsPerRoundMax,
		RandomOrder: s.Schedule.RandomOrder, WorkDays: nonNil(s.Schedule.WorkDays),
		WorkStart: strings.TrimSpace(s.Schedule.WorkStart), WorkEnd: strings.TrimSpace(s.Schedule.WorkEnd),
		PeriodStart: ps, PeriodEnd: pe,
	}
	cfg.Network.Proxy = strings.TrimSpace(s.Network.Proxy)
	cfg.Notify.StaleAfter = strings.TrimSpace(s.Notify.StaleAfter)
	cfg.Notify.Events = nonNil(s.Notify.Events)
	cfg.Notify.Cooldown = strings.TrimSpace(orDefault(s.Notify.Cooldown, config.DefaultCooldown))
	cfg.Notify.QuietHours = strings.TrimSpace(s.Notify.QuietHours)
	cfg.Notify.DailySummaryAt = strings.TrimSpace(s.Notify.DailySummaryAt)
	cfg.Notify.SpikeRounds, cfg.Notify.SpikeRate, cfg.Notify.SpikeMinFailures = s.Notify.SpikeRounds, s.Notify.SpikeRate, s.Notify.SpikeMinFailures
	if cfg.Notify.SpikeRounds == 0 {
		cfg.Notify.SpikeRounds = config.DefaultSpikeRounds
	}
	if cfg.Notify.SpikeRate == 0 {
		cfg.Notify.SpikeRate = config.DefaultSpikeRate
	}
	if cfg.Notify.SpikeMinFailures == 0 {
		cfg.Notify.SpikeMinFailures = config.DefaultSpikeMinFailures
	}
	if verr := cfg.Validate(); verr != nil {
		for _, e := range splitErrors(verr) {
			bad = append(bad, toValidationError(e))
		}
	}
	if len(bad) > 0 {
		return bad, nil
	}
	if err := cfg.Save(path); err != nil {
		return nil, err
	}
	return nil, nil
}

// toValidationError turns "schedule.interval_min: message" into a ValidationError.
func toValidationError(err error) ValidationError {
	field, msg, ok := strings.Cut(err.Error(), ": ")
	if !ok {
		return ValidationError{Message: err.Error()}
	}
	return ValidationError{Field: camelPath(field), Message: msg}
}

// camelPath converts "schedule.work_days[1]" to "schedule.workDays".
func camelPath(p string) string {
	if i := strings.IndexByte(p, '['); i >= 0 && !strings.HasPrefix(p, "accounts[") {
		p = p[:i]
	}
	var sb strings.Builder
	up := false
	for _, r := range p {
		switch {
		case r == '_':
			up = true
		case up:
			sb.WriteRune(unicode.ToUpper(r))
			up = false
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// NotifyEvents lists the alert kinds that can be selected.
func NotifyEvents() []string { return append([]string(nil), config.NotifyEvents...) }
