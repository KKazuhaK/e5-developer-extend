package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// minInterval is the shortest allowed interval_min; tests may lower it.
var minInterval = 5 * time.Minute

var (
	nameRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	guidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

	weekdays = map[string]bool{"mon": true, "tue": true, "wed": true, "thu": true, "fri": true, "sat": true, "sun": true}
)

// reservedNames are device names Windows refuses as file names; account names
// become vault file names, so they are rejected on every platform.
var reservedNames = func() map[string]bool {
	m := map[string]bool{"con": true, "prn": true, "aux": true, "nul": true}
	for i := '1'; i <= '9'; i++ {
		m["com"+string(i)] = true
		m["lpt"+string(i)] = true
	}
	return m
}()

// ValidName reports whether s is an allowed account name: 1-64 lowercase
// letters, digits, - or _, and not a reserved Windows device name.
func ValidName(s string) bool { return nameRe.MatchString(s) && !reservedNames[s] }

// redactURL hides credentials in a URL for use in messages.
func redactURL(s string) string {
	// Anything that does not parse as scheme://host may hide credentials in odd places.
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "<not shown: not a URL with scheme and host>"
	}
	return u.Redacted()
}

// ValidGUID reports whether s is a GUID such as an application (client) ID.
func ValidGUID(s string) bool { return guidRe.MatchString(s) }

// Validate checks the whole configuration and returns every problem at once,
// each prefixed with its field path.
func (c *Config) Validate() error {
	var errs []error
	add := func(path, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...)))
	}

	if c.Version != Version {
		add("version", "unsupported version %d (expected %d)", c.Version, Version)
	}

	validateSchedule(&c.Schedule, add)

	if p := c.Network.Proxy; p != "" {
		if u, err := url.Parse(p); err != nil || u.Scheme == "" || u.Host == "" {
			add("network.proxy", "not a valid URL: %s", redactURL(p))
		}
	}
	if w := c.Notify.WebhookURL; w != "" {
		if u, err := url.Parse(w); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("notify.webhook_url", "must be an http(s) URL")
		}
	}
	if d, err := time.ParseDuration(c.Notify.StaleAfter); err != nil {
		add("notify.stale_after", "not a duration: %q", c.Notify.StaleAfter)
	} else if d < time.Hour {
		add("notify.stale_after", "must be at least 1h")
	}
	seenEv := map[string]bool{}
	for i, ev := range c.Notify.Events {
		switch {
		case !slices.Contains(NotifyEvents, ev):
			add(fmt.Sprintf("notify.events[%d]", i), "unknown event %q (use %s)", ev, strings.Join(NotifyEvents, ", "))
		case seenEv[ev]:
			add(fmt.Sprintf("notify.events[%d]", i), "duplicate event %q", ev)
		}
		seenEv[ev] = true
	}
	validateNotify(&c.Notify, add)
	switch c.Vault.Backend {
	case BackendAuto, BackendKeyring, BackendFile:
	default:
		add("vault.backend", "must be auto, keyring or file, got %q", c.Vault.Backend)
	}

	seen := map[string]bool{}
	for i := range c.Accounts {
		validateAccount(&c.Accounts[i], i, seen, add)
	}
	return errors.Join(errs...)
}

func validateSchedule(s *Schedule, add func(path, format string, args ...any)) {
	dmin, err1 := time.ParseDuration(s.IntervalMin)
	if err1 != nil {
		add("schedule.interval_min", "not a duration: %q", s.IntervalMin)
	} else if dmin < minInterval {
		add("schedule.interval_min", "must be at least %s", minInterval)
	}
	dmax, err2 := time.ParseDuration(s.IntervalMax)
	if err2 != nil {
		add("schedule.interval_max", "not a duration: %q", s.IntervalMax)
	} else if dmax <= 0 {
		add("schedule.interval_max", "must be positive")
	}
	if err1 == nil && err2 == nil && dmin > dmax {
		add("schedule.interval_min", "must not exceed interval_max")
	}

	if s.APIsPerRoundMax < 1 {
		add("schedule.apis_per_round_max", "must be at least 1")
	}
	if s.APIsPerRoundMin < 1 || s.APIsPerRoundMin > s.APIsPerRoundMax {
		add("schedule.apis_per_round_min", "must be between 1 and apis_per_round_max (%d)", s.APIsPerRoundMax)
	}

	days := map[string]bool{}
	for i, d := range s.WorkDays {
		switch {
		case !weekdays[d]:
			add(fmt.Sprintf("schedule.work_days[%d]", i), "invalid day %q (use mon..sun)", d)
		case days[d]:
			add(fmt.Sprintf("schedule.work_days[%d]", i), "duplicate day %q", d)
		}
		days[d] = true
	}
	var hm [2]time.Time
	for i, f := range []struct{ name, val string }{{"work_start", s.WorkStart}, {"work_end", s.WorkEnd}} {
		t, err := time.Parse("15:04", f.val)
		if err != nil {
			add("schedule."+f.name, "not a HH:MM time: %q", f.val)
		}
		hm[i] = t
	}
	if !hm[0].IsZero() && !hm[1].IsZero() && hm[1].Before(hm[0]) {
		add("schedule.work_end", "must not be before work_start (windows across midnight are not supported); use equal values for all day")
	}
	if s.PeriodStart != nil && s.PeriodEnd != nil {
		a := time.Date(s.PeriodStart.Year, time.Month(s.PeriodStart.Month), s.PeriodStart.Day, 0, 0, 0, 0, time.UTC)
		b := time.Date(s.PeriodEnd.Year, time.Month(s.PeriodEnd.Month), s.PeriodEnd.Day, 0, 0, 0, 0, time.UTC)
		if b.Before(a) {
			add("schedule.period_end", "must not be before period_start")
		}
	}
}

func validateAccount(a *Account, i int, seen map[string]bool, add func(path, format string, args ...any)) {
	p := func(field string) string { return fmt.Sprintf("accounts[%d].%s", i, field) }

	switch {
	case a.Name == "":
		add(p("name"), "required")
	case !nameRe.MatchString(a.Name):
		add(p("name"), "must be 1-64 lowercase letters, digits, - or _: %q", a.Name)
	case reservedNames[a.Name]:
		add(p("name"), "%q is a reserved name", a.Name)
	case seen[a.Name]:
		add(p("name"), "duplicate account name %q", a.Name)
	}
	seen[a.Name] = true

	switch a.Auth {
	case AuthDeviceCode, AuthBrowser, AuthClientSecret, AuthPassword:
	default:
		add(p("auth"), "must be device_code, browser, client_secret or password, got %q", a.Auth)
	}
	if !guidRe.MatchString(a.ClientID) {
		add(p("client_id"), "not a GUID")
	}

	tenant := strings.ToLower(a.Tenant)
	switch {
	case a.Tenant == "":
		add(p("tenant"), "required")
	case a.Auth == AuthClientSecret && (tenant == "common" || tenant == "organizations" || tenant == "consumers"):
		add(p("tenant"), "client_secret needs a specific tenant ID or domain, not %q", a.Tenant)
	}
	if a.Auth == AuthPassword && a.Username == "" {
		add(p("username"), "required for password auth")
	}
	if a.SecretExpires != "" {
		if _, err := time.Parse("2006-01-02", a.SecretExpires); err != nil {
			add(p("secret_expires"), "not a YYYY-MM-DD date: %q", a.SecretExpires)
		}
	}
}
