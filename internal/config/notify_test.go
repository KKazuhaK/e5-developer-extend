package config

import (
	"strings"
	"testing"
	"time"
)

func TestNotifyChannelsRoundTrip(t *testing.T) {
	cfg := Default()
	cfg.Notify.QuietHours = "23:00-08:00"
	cfg.Notify.Channels = []Channel{
		{ID: "tg", Type: ChannelTelegram, MinLevel: LevelWarning, ChatID: "123", Language: "zh"},
		{ID: "mail", Type: ChannelEmail, SMTPHost: "smtp.example.com", SMTPPort: 587, From: "a@example.com",
			To: []string{"b@example.com", "c@example.com"}},
	}
	path := t.TempDir() + "/config.toml"
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Notify.Channels) != 2 || back.Notify.Channels[0].ChatID != "123" || len(back.Notify.Channels[1].To) != 2 {
		t.Fatalf("channels: %+v", back.Notify.Channels)
	}
	if back.Notify.QuietHours != "23:00-08:00" || back.Notify.Cooldown != "6h" {
		t.Errorf("noise settings: %+v", back.Notify)
	}
}

func TestNotifyDefaultsForOldFiles(t *testing.T) {
	cfg, err := Parse([]byte("version = 1\n[notify]\nstale_after = \"72h\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, rate, mf := cfg.Notify.SpikeSettings()
	if r != 3 || rate != 0.5 || mf != 5 || cfg.Notify.CooldownDuration() != 6*time.Hour {
		t.Errorf("defaults: %d %v %d %v", r, rate, mf, cfg.Notify.CooldownDuration())
	}
}

func TestChannelValidation(t *testing.T) {
	base := func(c Channel) *Config {
		cfg := Default()
		cfg.Notify.Channels = []Channel{c}
		return cfg
	}
	cases := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"ok", base(Channel{ID: "tg", Type: ChannelTelegram, ChatID: "1"}), ""},
		{"id", base(Channel{ID: "Bad_ID", Type: ChannelTelegram, ChatID: "1"}), "notify.channels[0].id"},
		{"type", base(Channel{ID: "x", Type: "sms"}), "unknown type"},
		{"chat", base(Channel{ID: "x", Type: ChannelTelegram}), "chat_id"},
		{"level", base(Channel{ID: "x", Type: ChannelTelegram, ChatID: "1", MinLevel: "loud"}), "min_level"},
		{"event", base(Channel{ID: "x", Type: ChannelTelegram, ChatID: "1", Events: []string{"boom"}}), "unknown event"},
		{"lang", base(Channel{ID: "x", Type: ChannelTelegram, ChatID: "1", Language: "fr"}), "language"},
		{"api base http", base(Channel{ID: "x", Type: ChannelTelegram, ChatID: "1", APIBase: "http://proxy.example"}), "https"},
		{"email to", base(Channel{ID: "x", Type: ChannelEmail, SMTPHost: "h", From: "a@b.co"}), "to"},
		{"email addr", base(Channel{ID: "x", Type: ChannelEmail, SMTPHost: "h", From: "a@b.co", To: []string{"nope"}}), "not an email address"},
		{"ntfy http ok", base(Channel{ID: "x", Type: ChannelNtfy, Topic: "t", Server: "http://ntfy.lan"}), ""},
		{"bark ftp", base(Channel{ID: "x", Type: ChannelBark, Server: "ftp://x"}), "http(s)"},
		{"webhook template needs template", base(Channel{ID: "x", Type: ChannelWebhook, Format: FormatTemplate}), "template"},
		{"dup", func() *Config {
			c := base(Channel{ID: "x", Type: ChannelSlack})
			c.Notify.Channels = append(c.Notify.Channels, Channel{ID: "x", Type: ChannelSlack})
			return c
		}(), "duplicate channel id"},
		{"quiet", func() *Config { c := Default(); c.Notify.QuietHours = "late"; return c }(), "notify.quiet_hours"},
		{"cooldown", func() *Config { c := Default(); c.Notify.Cooldown = "soon"; return c }(), "notify.cooldown"},
		{"rate", func() *Config { c := Default(); c.Notify.SpikeRate = 1.5; return c }(), "notify.spike_rate"},
	}
	for _, tc := range cases {
		err := tc.cfg.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: got %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestQuietHours(t *testing.T) {
	n := Notify{QuietHours: "23:00-08:00"}
	at := func(h, m int) time.Time { return time.Date(2026, 1, 5, h, m, 0, 0, time.UTC) }
	for _, tc := range []struct {
		h, m int
		want bool
	}{{22, 59, false}, {23, 0, true}, {2, 30, true}, {7, 59, true}, {8, 0, false}, {12, 0, false}} {
		if got := n.InQuietHours(at(tc.h, tc.m)); got != tc.want {
			t.Errorf("%02d:%02d: got %v", tc.h, tc.m, got)
		}
	}
	day := Notify{QuietHours: "12:00-14:00"}
	if !day.InQuietHours(at(13, 0)) || day.InQuietHours(at(14, 0)) || (Notify{}).InQuietHours(at(1, 0)) {
		t.Error("same-day window or empty window wrong")
	}
	if _, _, _, err := ParseQuietHours("08:00-08:00"); err == nil {
		t.Error("equal bounds accepted")
	}
}
