// Package notify turns account state changes into alerts and delivers them to
// a webhook. Webhook addresses can carry tokens, so no function here ever
// puts one into an error, a log line or a message.
package notify

import (
	"encoding/json"
	"time"
)

// Event kinds. The same names are accepted in the config's notify.events.
const (
	NeedsAttention = "needs_attention"
	Recovered      = "recovered"
	AutoPaused     = "auto_paused"
	Stale          = "stale"
	SecretExpiring = "secret_expiring"
	PeriodEnded    = "period_ended"
	// Test is only sent by `e5renew notify test`.
	Test = "test"
)

// Event is one alert.
type Event struct {
	Event   string    // kind, one of the constants above
	Account string    // account name from the config; empty for global events
	Message string    // what happened and what to do, without secrets
	Time    time.Time // when the condition was detected
}

// Text is the one-line form used by chat-style webhooks.
func (e Event) Text() string {
	if e.Account == "" {
		return "[e5renew] " + e.Message
	}
	return "[e5renew] " + e.Account + ": " + e.Message
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
}

// MarshalJSON implements json.Marshaler.
func (e Event) MarshalJSON() ([]byte, error) {
	t := e.Time
	if t.IsZero() {
		t = time.Now()
	}
	return json.Marshal(payload{
		Event: e.Event, Account: e.Account, Message: e.Message,
		Time: t.Format(time.RFC3339), Text: e.Text(), Content: e.Text(),
	})
}

// VaultKey is where the webhook address is stored.
const VaultKey = "notify/webhook"
