package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const guid = "00000000-0000-0000-0000-000000000001"

func validAccount(name string) Account {
	return Account{Name: name, Auth: AuthDeviceCode, Tenant: "contoso.onmicrosoft.com", ClientID: guid}
}

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	d := Default()
	if len(d.Accounts) != 0 || d.Vault.Backend != "auto" || !d.Schedule.RandomOrder {
		t.Errorf("unexpected defaults: %+v", d)
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	cfg := Default()
	cfg.Accounts = []Account{
		validAccount("main"),
		{Name: "app", Auth: AuthClientSecret, Tenant: "t.example", ClientID: guid, Username: "u@t.example", SecretExpires: "2027-01-02", Categories: []string{"mail"}},
	}
	cfg.Network.Proxy = "http://127.0.0.1:8080"
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// nil and empty slices are equivalent after a round trip.
	got.Accounts[0].Categories = cfg.Accounts[0].Categories
	if !reflect.DeepEqual(got, cfg) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, cfg)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestLoadNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte("version = 1\n[schedule]\ninterval_min = \"10m\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Schedule.IntervalMin != "10m" || cfg.Schedule.IntervalMax != "2h" || cfg.Schedule.APIsPerRoundMax != 8 {
		t.Errorf("defaults not applied: %+v", cfg.Schedule)
	}
}

func TestPeriodDates(t *testing.T) {
	cfg, err := Parse([]byte("version = 1\n[schedule]\nperiod_start = 2026-12-01\nperiod_end = 2027-12-01\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Schedule.PeriodStart == nil || cfg.Schedule.PeriodStart.Year != 2026 {
		t.Errorf("period_start = %+v", cfg.Schedule.PeriodStart)
	}
	if _, err := Parse([]byte("version = 1\n[schedule]\nperiod_start = 2027-12-01\nperiod_end = 2026-12-01\n")); err == nil {
		t.Error("expected error for reversed period")
	}
}

func TestParseRejectsUnknownFieldsAndBadSyntax(t *testing.T) {
	for _, in := range []string{"version = 1\nbogus = true\n", "version = \n"} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestValidate(t *testing.T) {
	mod := func(f func(c *Config)) *Config {
		c := Default()
		c.Accounts = []Account{validAccount("ok")}
		f(c)
		return c
	}
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"version", mod(func(c *Config) { c.Version = 2 }), "version:"},
		{"interval_min parse", mod(func(c *Config) { c.Schedule.IntervalMin = "soon" }), "schedule.interval_min: not a duration"},
		{"interval_max parse", mod(func(c *Config) { c.Schedule.IntervalMax = "" }), "schedule.interval_max: not a duration"},
		{"interval too short", mod(func(c *Config) { c.Schedule.IntervalMin = "1m" }), "schedule.interval_min: must be at least 5m"},
		{"interval order", mod(func(c *Config) { c.Schedule.IntervalMin = "3h" }), "must not exceed interval_max"},
		{"apis min low", mod(func(c *Config) { c.Schedule.APIsPerRoundMin = 0 }), "schedule.apis_per_round_min"},
		{"apis min high", mod(func(c *Config) { c.Schedule.APIsPerRoundMin = 9 }), "schedule.apis_per_round_min"},
		{"apis max", mod(func(c *Config) { c.Schedule.APIsPerRoundMax = 0 }), "schedule.apis_per_round_max"},
		{"bad day", mod(func(c *Config) { c.Schedule.WorkDays = []string{"mon", "funday"} }), "schedule.work_days[1]"},
		{"dup day", mod(func(c *Config) { c.Schedule.WorkDays = []string{"mon", "mon"} }), "duplicate day"},
		{"bad start", mod(func(c *Config) { c.Schedule.WorkStart = "8am" }), "schedule.work_start"},
		{"bad end", mod(func(c *Config) { c.Schedule.WorkEnd = "25:00" }), "schedule.work_end"},
		{"end before start", mod(func(c *Config) { c.Schedule.WorkStart = "22:00"; c.Schedule.WorkEnd = "06:00" }), "schedule.work_end"},
		{"proxy", mod(func(c *Config) { c.Network.Proxy = "nonsense" }), "network.proxy"},
		{"webhook", mod(func(c *Config) { c.Notify.WebhookURL = "ftp://x" }), "notify.webhook_url"},
		{"stale parse", mod(func(c *Config) { c.Notify.StaleAfter = "soon" }), "notify.stale_after: not a duration"},
		{"stale short", mod(func(c *Config) { c.Notify.StaleAfter = "10m" }), "notify.stale_after: must be at least 1h"},
		{"event unknown", mod(func(c *Config) { c.Notify.Events = []string{"stale", "boom"} }), "notify.events[1]"},
		{"event dup", mod(func(c *Config) { c.Notify.Events = []string{"stale", "stale"} }), "duplicate event"},
		{"backend", mod(func(c *Config) { c.Vault.Backend = "cloud" }), "vault.backend"},
		{"name chars", mod(func(c *Config) { c.Accounts[0].Name = "a b" }), "accounts[0].name"},
		{"name uppercase", mod(func(c *Config) { c.Accounts[0].Name = "Main" }), "accounts[0].name"},
		{"name too long", mod(func(c *Config) { c.Accounts[0].Name = strings.Repeat("a", 65) }), "accounts[0].name"},
		{"name reserved", mod(func(c *Config) { c.Accounts[0].Name = "com1" }), "reserved"},
		{"name reserved nul", mod(func(c *Config) { c.Accounts[0].Name = "nul" }), "reserved"},
		{"name empty", mod(func(c *Config) { c.Accounts[0].Name = "" }), "accounts[0].name: required"},
		{"name dup", mod(func(c *Config) { c.Accounts = append(c.Accounts, validAccount("ok")) }), "accounts[1].name: duplicate"},
		{"auth", mod(func(c *Config) { c.Accounts[0].Auth = "magic" }), "accounts[0].auth"},
		{"client_id", mod(func(c *Config) { c.Accounts[0].ClientID = "abc" }), "accounts[0].client_id: not a GUID"},
		{"tenant empty", mod(func(c *Config) { c.Accounts[0].Tenant = "" }), "accounts[0].tenant: required"},
		{"secret common", mod(func(c *Config) { c.Accounts[0].Auth = AuthClientSecret; c.Accounts[0].Tenant = "common" }), "accounts[0].tenant"},
		{"secret organizations", mod(func(c *Config) { c.Accounts[0].Auth = AuthClientSecret; c.Accounts[0].Tenant = "Organizations" }), "accounts[0].tenant"},
		{"password username", mod(func(c *Config) { c.Accounts[0].Auth = AuthPassword }), "accounts[0].username"},
		{"secret_expires", mod(func(c *Config) { c.Accounts[0].SecretExpires = "next year" }), "accounts[0].secret_expires"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	c := Default()
	c.Schedule.WorkStart = "x"
	c.Accounts = []Account{{Name: "bad name", Auth: "x", ClientID: "y"}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	n := len(strings.Split(strings.TrimSpace(err.Error()), "\n"))
	if n < 5 {
		t.Fatalf("expected several problems, got %d: %v", n, err)
	}
}

func TestOpenTenantAllowedForDelegated(t *testing.T) {
	c := Default()
	a := validAccount("u")
	a.Tenant = "organizations"
	c.Accounts = []Account{a}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFindRemove(t *testing.T) {
	c := Default()
	c.Accounts = []Account{validAccount("a"), validAccount("b")}
	if _, ok := c.Find("b"); !ok {
		t.Fatal("find b")
	}
	if !c.Remove("a") || c.Remove("a") || len(c.Accounts) != 1 {
		t.Fatal("remove")
	}
}

func TestNewerVersionRejectedFirst(t *testing.T) {
	_, err := Parse([]byte("version = 2\nfuture_field = 1\n"))
	if err == nil || !strings.Contains(err.Error(), "newer than this e5extend supports") {
		t.Fatalf("got %v", err)
	}
}

func TestSaveWritesHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Default().Save(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "# Managed by e5extend") || !strings.Contains(string(data), "--print-template") {
		t.Fatalf("header missing:\n%s", data)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestProxyUserinfoRedacted(t *testing.T) {
	c := Default()
	c.Network.Proxy = "http://user:hunter2@:bad"
	err := c.Validate()
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("got %v", err)
	}
	c.Network.Proxy = "user:hunter2@host"
	if err := c.Validate(); err != nil && strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("leak: %v", err)
	}
	if got := redactURL("http://u:p4ss@host:1"); strings.Contains(got, "p4ss") {
		t.Fatalf("redactURL = %q", got)
	}
}

func TestNotifyDefaultsAndRoundTrip(t *testing.T) {
	cfg, err := Parse([]byte("version = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Notify.StaleAfter != "72h" || len(cfg.Notify.Events) != 0 || cfg.Notify.StaleDuration().Hours() != 72 {
		t.Errorf("defaults: %+v", cfg.Notify)
	}
	cfg.Notify.Events = []string{"stale", "recovered"}
	cfg.Notify.StaleAfter = "96h"
	path := filepath.Join(t.TempDir(), "c.toml")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil || back.Notify.StaleAfter != "96h" || len(back.Notify.Events) != 2 {
		t.Fatalf("round trip: %v %+v", err, back.Notify)
	}
}
