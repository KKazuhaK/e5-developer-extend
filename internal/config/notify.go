package config

import (
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Defaults for the noise settings.
const (
	DefaultCooldown         = "6h"
	DefaultSpikeRounds      = 3
	DefaultSpikeRate        = 0.5
	DefaultSpikeMinFailures = 5
)

// Channel types.
const (
	ChannelTelegram   = "telegram"
	ChannelEmail      = "email"
	ChannelWeCom      = "wecom"
	ChannelFeishu     = "feishu"
	ChannelDingTalk   = "dingtalk"
	ChannelDiscord    = "discord"
	ChannelSlack      = "slack"
	ChannelBark       = "bark"
	ChannelNtfy       = "ntfy"
	ChannelServerChan = "serverchan"
	ChannelWebhook    = "webhook"
)

// ChannelTypes lists every channel type.
var ChannelTypes = []string{ChannelTelegram, ChannelEmail, ChannelWeCom, ChannelFeishu, ChannelDingTalk,
	ChannelDiscord, ChannelSlack, ChannelBark, ChannelNtfy, ChannelServerChan, ChannelWebhook}

// Alert levels, lowest first.
const (
	LevelInfo     = "info"
	LevelWarning  = "warning"
	LevelCritical = "critical"
)

// Levels lists the alert levels, lowest first.
var Levels = []string{LevelInfo, LevelWarning, LevelCritical}

// Email security modes.
const (
	SecurityStartTLS = "starttls"
	SecurityTLS      = "tls"
	SecurityNone     = "none"
)

// Webhook body formats.
const (
	FormatJSON     = "json"
	FormatTemplate = "template"
)

// Channel is one notification destination. Only non-secret fields are here;
// tokens, passwords, webhook addresses and sign secrets are in the vault
// under notify/<id>/<field>.
type Channel struct {
	ID       string   `toml:"id"`
	Type     string   `toml:"type"`
	MinLevel string   `toml:"min_level,omitempty"`
	Events   []string `toml:"events,omitempty"`
	Language string   `toml:"language,omitempty"` // zh or en; empty follows the interface
	Template string   `toml:"template,omitempty"` // empty: the built-in template

	ChatID   string   `toml:"chat_id,omitempty"`   // telegram
	APIBase  string   `toml:"api_base,omitempty"`  // telegram, behind a reverse proxy
	SMTPHost string   `toml:"smtp_host,omitempty"` // email
	SMTPPort int      `toml:"smtp_port,omitempty"`
	Security string   `toml:"security,omitempty"`
	Username string   `toml:"username,omitempty"`
	From     string   `toml:"from,omitempty"`
	To       []string `toml:"to,omitempty"`
	Server   string   `toml:"server,omitempty"` // bark, ntfy
	Topic    string   `toml:"topic,omitempty"`  // ntfy
	Format   string   `toml:"format,omitempty"` // webhook: json or template
	// AllowHTTP lets a webhook address be http (webhook only). It is set when a
	// webhook of an older version that used http is saved as a channel; the
	// doctor keeps warning about it.
	AllowHTTP bool `toml:"allow_http,omitempty"`
	// AllowPlainAuth lets an email channel with security none send the
	// password to a host that is not this machine (email only).
	AllowPlainAuth bool `toml:"allow_plain_auth,omitempty"`
}

// ChannelSecrets lists the vault fields of a channel type and whether each is required.
func ChannelSecrets(typ string) []SecretField {
	switch typ {
	case ChannelTelegram:
		return []SecretField{{"bot_token", true}}
	case ChannelEmail:
		return []SecretField{{"password", false}}
	case ChannelWeCom, ChannelDiscord, ChannelSlack, ChannelWebhook:
		return []SecretField{{"url", true}}
	case ChannelFeishu, ChannelDingTalk:
		return []SecretField{{"url", true}, {"secret", false}}
	case ChannelBark:
		return []SecretField{{"device_key", true}}
	case ChannelNtfy:
		return []SecretField{{"token", false}}
	case ChannelServerChan:
		return []SecretField{{"sendkey", true}}
	}
	return nil
}

// SecretField is one secret of a channel.
type SecretField struct {
	Name     string
	Required bool
}

// ValidChannelType reports whether typ is a known channel type.
func ValidChannelType(typ string) bool { return slices.Contains(ChannelTypes, typ) }

// LevelRank orders the levels; an unknown or empty level ranks as info.
func LevelRank(level string) int {
	switch level {
	case LevelCritical:
		return 2
	case LevelWarning:
		return 1
	}
	return 0
}

// SMTPPortOrDefault is the configured port, or the usual one for the security mode.
func (c Channel) SMTPPortOrDefault() int {
	if c.SMTPPort > 0 {
		return c.SMTPPort
	}
	switch c.SecurityOrDefault() {
	case SecurityTLS:
		return 465
	case SecurityNone:
		return 25
	}
	return 587
}

// SecurityOrDefault is the email security mode, starttls when unset.
func (c Channel) SecurityOrDefault() string {
	if c.Security == "" {
		return SecurityStartTLS
	}
	return c.Security
}

var channelIDRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// ValidChannelID reports whether s is an allowed channel id: 1-32 of a-z, 0-9 and -.
func ValidChannelID(s string) bool { return channelIDRe.MatchString(s) }

// CooldownDuration is the parsed Cooldown. Empty means the default; only a
// zero duration ("0") turns the cooldown off.
func (n Notify) CooldownDuration() time.Duration {
	s := strings.TrimSpace(n.Cooldown)
	if s == "" {
		s = DefaultCooldown
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// SpikeSettings returns the failure_spike thresholds, with defaults for unset values.
func (n Notify) SpikeSettings() (rounds int, rate float64, minFailures int) {
	rounds, rate, minFailures = n.SpikeRounds, n.SpikeRate, n.SpikeMinFailures
	if rounds <= 0 {
		rounds = DefaultSpikeRounds
	}
	if rate <= 0 || rate > 1 {
		rate = DefaultSpikeRate
	}
	if minFailures <= 0 {
		minFailures = DefaultSpikeMinFailures
	}
	return
}

// ParseQuietHours parses "HH:MM-HH:MM" into minutes after midnight. The
// window may cross midnight. An empty string is not an error: ok is false.
func ParseQuietHours(s string) (start, end int, ok bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false, nil
	}
	a, b, found := strings.Cut(s, "-")
	if !found {
		return 0, 0, false, fmt.Errorf("use the form 23:00-08:00")
	}
	ta, err1 := time.Parse("15:04", strings.TrimSpace(a))
	tb, err2 := time.Parse("15:04", strings.TrimSpace(b))
	if err1 != nil || err2 != nil {
		return 0, 0, false, fmt.Errorf("use the form 23:00-08:00")
	}
	start, end = ta.Hour()*60+ta.Minute(), tb.Hour()*60+tb.Minute()
	if start == end {
		return 0, 0, false, fmt.Errorf("start and end must differ")
	}
	return start, end, true, nil
}

// InQuietHours reports whether t falls into the quiet window.
func (n Notify) InQuietHours(t time.Time) bool {
	start, end, ok, err := ParseQuietHours(n.QuietHours)
	if !ok || err != nil {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// FindChannel returns the channel with the given id.
func (n Notify) FindChannel(id string) (*Channel, bool) {
	for i := range n.Channels {
		if n.Channels[i].ID == id {
			return &n.Channels[i], true
		}
	}
	return nil, false
}

func validateNotify(n *Notify, add func(path, format string, args ...any)) {
	if strings.TrimSpace(n.Cooldown) != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(n.Cooldown)); err != nil || d < 0 {
			add("notify.cooldown", "not a duration: %q", n.Cooldown)
		}
	}
	if _, _, _, err := ParseQuietHours(n.QuietHours); err != nil {
		add("notify.quiet_hours", "%v", err)
	}
	if n.SpikeRounds < 0 || n.SpikeRounds > 50 {
		add("notify.spike_rounds", "must be between 1 and 50")
	}
	if n.SpikeRate < 0 || n.SpikeRate > 1 {
		add("notify.spike_rate", "must be between 0 and 1")
	}
	if n.SpikeMinFailures < 0 {
		add("notify.spike_min_failures", "must not be negative")
	}
	seen := map[string]bool{}
	for i := range n.Channels {
		c := &n.Channels[i]
		p := fmt.Sprintf("notify.channels[%d]", i)
		switch {
		case !ValidChannelID(c.ID):
			add(p+".id", "must be 1-32 of a-z, 0-9 and -")
		case seen[c.ID]:
			add(p+".id", "duplicate channel id %q", c.ID)
		}
		seen[c.ID] = true
		validateChannel(c, p, add)
	}
}

func validateChannel(c *Channel, p string, add func(path, format string, args ...any)) {
	if !ValidChannelType(c.Type) {
		add(p+".type", "unknown type %q (use %s)", c.Type, strings.Join(ChannelTypes, ", "))
		return
	}
	if c.MinLevel != "" && !slices.Contains(Levels, c.MinLevel) {
		add(p+".min_level", "must be info, warning or critical")
	}
	seenEv := map[string]bool{}
	for i, ev := range c.Events {
		switch {
		case !slices.Contains(NotifyEvents, ev):
			add(fmt.Sprintf("%s.events[%d]", p, i), "unknown event %q", ev)
		case seenEv[ev]:
			add(fmt.Sprintf("%s.events[%d]", p, i), "duplicate event %q", ev)
		}
		seenEv[ev] = true
	}
	if c.Language != "" && c.Language != "zh" && c.Language != "en" {
		add(p+".language", "must be zh or en")
	}
	need := func(field, val string) {
		if strings.TrimSpace(val) == "" {
			add(p+"."+field, "is required for type %s", c.Type)
		}
	}
	switch c.Type {
	case ChannelTelegram:
		need("chat_id", c.ChatID)
		if c.APIBase != "" {
			checkURL(p+".api_base", c.APIBase, false, add)
		}
	case ChannelEmail:
		need("smtp_host", c.SMTPHost)
		need("from", c.From)
		switch c.Security {
		case "", SecurityStartTLS, SecurityTLS, SecurityNone:
		default:
			add(p+".security", "must be starttls, tls or none")
		}
		if c.SMTPPort < 0 || c.SMTPPort > 65535 {
			add(p+".smtp_port", "must be between 1 and 65535")
		}
		if c.From != "" {
			if _, err := mail.ParseAddress(c.From); err != nil {
				add(p+".from", "not an email address")
			}
		}
		if len(c.To) == 0 {
			add(p+".to", "is required for type email")
		}
		for i, a := range c.To {
			if _, err := mail.ParseAddress(a); err != nil {
				add(fmt.Sprintf("%s.to[%d]", p, i), "not an email address")
			}
		}
	case ChannelBark:
		if c.Server != "" {
			checkURL(p+".server", c.Server, true, add)
		}
	case ChannelNtfy:
		need("topic", c.Topic)
		if c.Topic != "" && !ntfyTopicRe.MatchString(c.Topic) {
			add(p+".topic", "may only contain letters, digits, _ and -")
		}
		if c.Server != "" {
			checkURL(p+".server", c.Server, true, add)
		}
	case ChannelWebhook:
		switch c.Format {
		case "", FormatJSON:
			if c.Template != "" {
				add(p+".template", "is only used when format is template")
			}
		case FormatTemplate:
			need("template", c.Template)
		default:
			add(p+".format", "must be json or template")
		}
	}
	if c.AllowHTTP && c.Type != ChannelWebhook {
		add(p+".allow_http", "is only for type webhook")
	}
	if c.AllowPlainAuth && c.Type != ChannelEmail {
		add(p+".allow_plain_auth", "is only for type email")
	}
	if c.Type != ChannelWebhook && c.Format != "" {
		add(p+".format", "is only for type webhook")
	}
}

var ntfyTopicRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// checkURL requires an https URL, or http as well when allowHTTP is set.
func checkURL(path, raw string, allowHTTP bool, add func(path, format string, args ...any)) {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "":
		add(path, "not a valid URL: %s", redactURL(raw))
	case u.Scheme == "https" || (allowHTTP && u.Scheme == "http"):
	case allowHTTP:
		add(path, "must be an http(s) URL")
	default:
		add(path, "must be an https URL")
	}
}
