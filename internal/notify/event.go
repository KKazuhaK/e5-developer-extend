// Package notify turns account state changes into alerts and delivers them to
// a webhook. Webhook addresses can carry tokens, so no function here ever
// puts one into an error, a log line or a message.
package notify

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

// Event kinds. The same names are accepted in the config's notify.events.
const (
	NeedsAttention = "needs_attention"
	Recovered      = "recovered"
	AutoPaused     = "auto_paused"
	Stale          = "stale"
	SecretExpiring = "secret_expiring"
	PeriodEnded    = "period_ended"
	FailureSpike   = "failure_spike"
	AllFailing     = "all_failing"

	SubscriptionExpiring = "subscription_expiring"
	SubscriptionStatus   = "subscription_status"
	SubscriptionRenewed  = "subscription_renewed"
	// UpdateAvailable says a newer release exists; sent once per version.
	UpdateAvailable = "update_available"
	// Test is only sent by `e5extend notify test`.
	Test = "test"
	// Digest is the summary of alerts that were held during the quiet hours.
	Digest = "digest"
)

// LevelOf is the level of an event kind: critical, warning or info.
func LevelOf(kind string) string {
	switch kind {
	case NeedsAttention, AutoPaused, AllFailing, SubscriptionStatus:
		return config.LevelCritical
	case Stale, SecretExpiring, FailureSpike, SubscriptionExpiring:
		return config.LevelWarning
	}
	return config.LevelInfo
}

// LevelOfKey is the level of one alert of a kind, given the key its decision
// was based on: a subscription expiring in 30 days is a warning, one expiring
// within 7 days is critical; a subscription falling into a worse status is
// critical, its recovery is info. The other kinds have one level.
func LevelOfKey(kind, key string) string {
	switch kind {
	case SubscriptionExpiring:
		if lv, _, _ := strings.Cut(key, "@"); lv == "30" {
			return config.LevelWarning
		}
		return config.LevelCritical
	case SubscriptionStatus:
		if key == StatusEnabled {
			return config.LevelInfo
		}
		return config.LevelCritical
	}
	return LevelOf(kind)
}

// StatusEnabled is the healthy status of a subscription.
const StatusEnabled = "Enabled"

// Event is one alert.
type Event struct {
	Event   string    // kind, one of the constants above
	Account string    // account name from the config; empty for global events
	Message string    // what happened and what to do, without secrets
	Time    time.Time // when the condition was detected

	// IsTest marks a sample sent by `notify test`: the message says so.
	IsTest bool
	// Attempt identifies this hand-out of the alert, for MarkEvent; Time stays
	// the moment the condition was first detected. Zero means Time.
	Attempt time.Time
	// Tenant is the tenant ID of the account; only custom templates can show it.
	Tenant string
	// Origin is, for Recovered, the event that ended (failure_spike, ...).
	Origin string
	// Failures, Rounds and FailRate describe a failure_spike.
	Failures, Rounds int
	FailRate         float64
	// Items are the held events of a Digest.
	Items []Event
	// Subscription is the ID of the subscription of a subscription_* event
	// ("manual" for the date entered by hand); Account is empty for them
	// because a subscription belongs to the tenant.
	Subscription string
	// SKU, SubStatus and EndDate (YYYY-MM-DD) describe that subscription.
	SKU, SubStatus, EndDate string
	// Severity overrides the level of the kind (see LevelOfKey).
	Severity string
}

// Level is the level of the event; a digest has the highest of its items.
func (e Event) Level() string {
	if e.Event != Digest {
		if e.Severity != "" {
			return e.Severity
		}
		return LevelOf(e.Event)
	}
	lv := config.LevelInfo
	for _, it := range e.Items {
		if config.LevelRank(it.Level()) > config.LevelRank(lv) {
			lv = it.Level()
		}
	}
	return lv
}

// Text is the one-line form used by chat-style webhooks.
func (e Event) Text() string {
	if e.Account == "" {
		return "[e5extend] " + e.Message
	}
	return "[e5extend] " + e.Account + ": " + e.Message
}

// payload is the JSON body. text suits Slack and WeCom/Feishu style bots,
// content suits Discord; the structured fields are for everything else.
type payload struct {
	Event   string `json:"event"`
	Account string `json:"account"`
	Message string `json:"message"`
	Time    string `json:"time"`
	Text    string `json:"text"`
	Content string `json:"content"`
	// Level, Origin and Items are only present when they apply.
	// Tenant and the subscription fields are only present for subscription events.
	Tenant       string    `json:"tenant,omitempty"`
	Subscription string    `json:"subscription,omitempty"`
	SKU          string    `json:"sku,omitempty"`
	SubStatus    string    `json:"status,omitempty"`
	EndDate      string    `json:"ends,omitempty"`
	Test         bool      `json:"test,omitempty"`
	Level        string    `json:"level,omitempty"`
	Origin       string    `json:"origin,omitempty"`
	Items        []payload `json:"items,omitempty"`
}

// MarshalJSON implements json.Marshaler.
func (e Event) MarshalJSON() ([]byte, error) { return json.Marshal(e.payload()) }

func (e Event) payload() payload {
	t := e.Time
	if t.IsZero() {
		t = time.Now()
	}
	p := payload{
		Event: e.Event, Account: e.Account, Message: e.Message,
		Time: t.Format(time.RFC3339), Text: e.Text(), Content: e.Text(),
		Level: e.Level(), Origin: e.Origin, Test: e.IsTest,
	}
	if e.Subscription != "" {
		p.Tenant, p.Subscription, p.SKU, p.SubStatus, p.EndDate = e.PublicTenant(), e.Subscription, e.SKU, e.SubStatus, e.EndDate
	}
	for _, it := range e.Items {
		p.Items = append(p.Items, it.payload())
	}
	return p
}

// VaultKey is where older versions stored the webhook address. It is still
// read, as the url of the channel "webhook"; new writes use ChannelSecretKey.
const VaultKey = "notify/webhook"

// LegacyChannelID is the id of the channel an old notify/webhook entry stands for.
const LegacyChannelID = "webhook"

// Level names, for convenience.
const (
	LevelInfo     = config.LevelInfo
	LevelWarning  = config.LevelWarning
	LevelCritical = config.LevelCritical
)

// ChannelSecretKey is the vault key of one secret field of a channel.
func ChannelSecretKey(channelID, field string) string {
	return "notify/" + channelID + "/" + field
}

// IsChannelSecretKey reports whether key is a secret of a channel of cfg
// (or of the legacy webhook channel).
func IsChannelSecretKey(cfg *config.Config, key string) bool {
	rest, ok := strings.CutPrefix(key, "notify/")
	if !ok {
		return false
	}
	id, field, ok := strings.Cut(rest, "/")
	if !ok {
		return false
	}
	typ := ""
	if c, found := cfg.Notify.FindChannel(id); found {
		typ = c.Type
	} else if id == LegacyChannelID {
		typ = config.ChannelWebhook
	}
	for _, f := range config.ChannelSecrets(typ) {
		if f.Name == field {
			return true
		}
	}
	return false
}

// PublicTenant is the tenant to show: empty while it is only an account that
// names no tenant (the state key "account:<name>"), a domain or GUID otherwise.
func (e Event) PublicTenant() string {
	if strings.HasPrefix(e.Tenant, "account:") {
		return ""
	}
	return e.Tenant
}
