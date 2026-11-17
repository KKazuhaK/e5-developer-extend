package subscription

import (
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

var at = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

func gs(status string, next string) graph.Subscription {
	t, _ := time.Parse(time.RFC3339, next)
	return graph.Subscription{ID: "s1", SKUPartNumber: "DEVELOPERPACK_E5", Status: status, NextLifecycleDateTime: t}
}

func TestOnlyAnEnabledSubscriptionIsRenewed(t *testing.T) {
	ts := &store.TenantState{}
	Apply(ts, []graph.Subscription{gs("Enabled", "2027-02-14T12:00:00Z")}, at)
	// Worse status, later date (the end of the grace period): not a renewal.
	if n := Apply(ts, []graph.Subscription{gs("Warning", "2027-03-16T12:00:00Z")}, at.Add(24*time.Hour)); n != 0 || len(ts.Subscriptions["s1"].History) != 0 {
		t.Fatalf("worsening recorded as a renewal: %d %+v", n, ts.Subscriptions["s1"].History)
	}
	// Enabled again and later still: renewed, from the date seen last.
	if n := Apply(ts, []graph.Subscription{gs("Enabled", "2027-06-16T12:00:00Z")}, at.Add(48*time.Hour)); n != 1 {
		t.Fatalf("renewals = %d", n)
	}
	h := ts.Subscriptions["s1"].History
	if len(h) != 1 || h[0].From.Format("2006-01-02") != "2027-03-16" || h[0].To.Format("2006-01-02") != "2027-06-16" {
		t.Errorf("history = %+v", h)
	}
	// Earlier or equal dates are not renewals; the history is capped.
	Apply(ts, []graph.Subscription{gs("Enabled", "2027-06-16T12:00:00Z")}, at.Add(72*time.Hour))
	for i := 0; i < 30; i++ {
		Apply(ts, []graph.Subscription{gs("Enabled", time.Date(2027, 7, 1+i, 12, 0, 0, 0, time.UTC).Format(time.RFC3339))}, at.Add(time.Duration(100+i)*time.Hour))
	}
	if n := len(ts.Subscriptions["s1"].History); n != store.MaxRenewals {
		t.Errorf("history has %d entries", n)
	}
}

func TestNextCheckSchedule(t *testing.T) {
	if !NextCheck(nil).IsZero() || !NextCheck(&store.TenantState{}).IsZero() {
		t.Error("a tenant never tried is due at once")
	}
	ts := &store.TenantState{Access: store.AccessOK, Attempted: at}
	if got := NextCheck(ts).Sub(at); got != 24*time.Hour {
		t.Errorf("ok: %s", got)
	}
	NoPermission(ts, at)
	if got := NextCheck(ts).Sub(at); got != time.Hour {
		t.Errorf("no permission: %s", got)
	}
	Failed(ts, at, "x")
	if got := NextCheck(ts).Sub(at); got != 5*time.Minute {
		t.Errorf("failed: %s", got)
	}
}

func TestMissingPermission(t *testing.T) {
	for scopes, want := range map[string]bool{"": false, "User.Read Mail.Read": true, "User.Read organization.read.all": false, "Directory.Read.All": false} {
		var list []string
		if scopes != "" {
			list = splitFields(scopes)
		}
		if got := MissingPermission(list); got != want {
			t.Errorf("%q = %v, want %v", scopes, got, want)
		}
	}
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			out, cur = append(out, cur), ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}

func TestGroupMergesAliasesAndPicksALabel(t *testing.T) {
	const guid = "7d3f2a10-55c1-4c88-b1d4-0a3e9c61f2b8"
	acct := func(name, tenant string) config.Account {
		return config.Account{Name: name, Auth: config.AuthDeviceCode, Tenant: tenant}
	}
	cfg := &config.Config{Accounts: []config.Account{acct("a", guid), acct("b", "Contoso.onmicrosoft.com"), acct("c", "organizations"), acct("d", "other.example")}}
	st := store.NewState()
	g := Group(cfg, st)
	if len(g) != 4 {
		t.Fatalf("without aliases: %+v", g)
	}
	st.TenantAliases = map[string]string{"contoso.onmicrosoft.com": guid, "account:c": guid}
	g = Group(cfg, st)
	if len(g) != 2 || g[0].Key != guid || len(g[0].Accounts) != 3 || g[0].Label != "Contoso.onmicrosoft.com" || g[1].Label != "other.example" {
		t.Errorf("groups = %+v", g)
	}
	// All GUIDs: the GUID; an unnamed tenant alone: the account name.
	if l := Group(&config.Config{Accounts: []config.Account{acct("a", guid)}}, nil)[0].Label; l != guid {
		t.Errorf("label = %q", l)
	}
	if l := Group(&config.Config{Accounts: []config.Account{acct("solo", "common")}}, nil)[0].Label; l != "solo" {
		t.Errorf("label = %q", l)
	}
}

func TestMergeUnitesTheStateOfTwoKeys(t *testing.T) {
	st := store.NewState()
	st.Tenants = map[string]*store.TenantState{
		"contoso.onmicrosoft.com": {Access: store.AccessOK, Attempted: at.Add(time.Hour), Subscriptions: map[string]*store.SubscriptionState{"s1": {SKU: "A", CheckedAt: at.Add(time.Hour)}}},
		"guid":                    {Access: store.AccessNoPermission, Attempted: at, Subscriptions: map[string]*store.SubscriptionState{"s1": {SKU: "OLD", CheckedAt: at}, "s2": {SKU: "B"}}},
	}
	Merge(st, "contoso.onmicrosoft.com", "guid")
	if len(st.Tenants) != 1 || st.TenantAliases["contoso.onmicrosoft.com"] != "guid" {
		t.Fatalf("state = %+v / %v", st.Tenants, st.TenantAliases)
	}
	ts := st.Tenants["guid"]
	if ts.Access != store.AccessOK || ts.Subscriptions["s1"].SKU != "A" || ts.Subscriptions["s2"] == nil {
		t.Errorf("merged = %+v", ts)
	}
	Merge(st, "guid", "guid") // no-op
}

func TestUsableAccounts(t *testing.T) {
	st := store.NewState()
	st.Accounts["att"] = &store.AccountState{Status: store.StatusNeedsAttention}
	st.Accounts["pau"] = &store.AccountState{Status: store.StatusPaused}
	st.Accounts["ok"] = &store.AccountState{Status: store.StatusOK}
	if Usable(st, config.Account{Name: "att"}, false) || !Usable(st, config.Account{Name: "att"}, true) {
		t.Error("an account that needs attention is tried only when forced")
	}
	if Usable(st, config.Account{Name: "pau"}, true) || Usable(st, config.Account{Name: "ok", Paused: true}, true) || !Usable(st, config.Account{Name: "ok"}, false) {
		t.Error("paused accounts are never used")
	}
}

func TestViewShowsLocalDatesAndFixCommands(t *testing.T) {
	loc := time.FixedZone("UTC+9", 9*3600)
	now := time.Date(2026, 11, 17, 8, 0, 0, 0, loc)
	cfg := &config.Config{Accounts: []config.Account{{Name: "main", Auth: config.AuthDeviceCode, Tenant: "contoso.onmicrosoft.com", ClientID: "cid"}}}
	st := store.NewState()
	// 2026-11-18T20:00Z is the 19th in UTC+9: two days from the 17th there.
	st.Tenants = map[string]*store.TenantState{"contoso.onmicrosoft.com": {Access: store.AccessOK, Subscriptions: map[string]*store.SubscriptionState{
		"s1": {SKU: "DEVELOPERPACK_E5", Status: "Enabled", NextLifecycle: time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC),
			History: []store.Renewal{{From: time.Date(2026, 8, 18, 20, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC), SeenAt: now}}}}}}
	v := Build(cfg, st, now)[0]
	s := v.Subscriptions[0]
	if s.Expires != "2026-11-19" || s.DaysLeft == nil || *s.DaysLeft != 2 || s.Renewals[0].From != "2026-08-19" {
		t.Errorf("dates: %+v", s)
	}
	if v.FixAccount != "main" || v.FixKind != "delegated" || v.FixCommand != "e5extend app setup --tenant contoso.onmicrosoft.com --adopt cid" {
		t.Errorf("fix: %+v", v)
	}
	_, _, cmd := FixFor(config.Account{Name: "app1", Auth: config.AuthClientSecret})
	if cmd != "e5extend app setup --app-only --account app1 --no-new-secret" {
		t.Errorf("app fix: %s", cmd)
	}
	if _, _, cmd := FixFor(config.Account{Name: "x", Auth: config.AuthBrowser, Tenant: "organizations", ClientID: "c"}); cmd != "e5extend app setup --adopt c" {
		t.Errorf("unnamed tenant fix: %s", cmd)
	}
}
