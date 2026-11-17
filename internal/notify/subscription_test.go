package notify

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

const subTenant = "contoso.onmicrosoft.com"

type subRig struct {
	*rig
	sub *store.SubscriptionState
}

func newSubRig() *subRig {
	r := newRig()
	r.acct = config.Account{Name: "main", Auth: config.AuthDeviceCode, Tenant: subTenant}
	sub := &store.SubscriptionState{SKU: "DEVELOPERPACK_E5", Status: StatusEnabled, CheckedAt: t0,
		NextLifecycle: time.Date(2027, 2, 14, 0, 0, 0, 0, time.UTC)}
	r.st.Tenants = map[string]*store.TenantState{subTenant: {Access: store.AccessOK, Checked: t0,
		Subscriptions: map[string]*store.SubscriptionState{"sub-1": sub}}}
	return &subRig{rig: r, sub: sub}
}

func (r *subRig) at(date string) {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic(err)
	}
	r.sub.NextLifecycle = d
}

func (r *subRig) eval() []Event {
	r.as.LastSuccess = r.now // the account itself stays healthy
	evs, _ := Evaluate(Input{Now: r.now, Start: r.start, Config: r.cfg, Accounts: []config.Account{r.acct}, State: r.st})
	for _, e := range evs {
		MarkEvent(r.st, e)
	}
	return evs
}

func TestSubscriptionExpiringThresholdsOnceEach(t *testing.T) {
	r := newSubRig()
	r.at("2027-01-20")
	r.now = time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)
	var got []string
	levels := map[string]string{}
	for d := 0; d <= 60; d++ {
		for _, e := range r.eval() {
			got = append(got, e.Event+"@"+r.now.Format("01-02"))
			levels[r.now.Format("01-02")] = e.Level()
			if e.Account != "" || e.Tenant != subTenant || e.Subscription != "sub-1" {
				t.Errorf("event scope: %+v", e)
			}
		}
		r.now = r.now.Add(24 * time.Hour)
	}
	// 30 days before Jan 20 is Dec 21, 7 days Jan 13, 1 day Jan 19.
	want := []string{"subscription_expiring@12-21", "subscription_expiring@01-13", "subscription_expiring@01-19", "subscription_expiring@01-20"}
	if !slices.Equal(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	if levels["12-21"] != config.LevelWarning || levels["01-13"] != config.LevelCritical || levels["01-19"] != config.LevelCritical {
		t.Errorf("levels = %v", levels)
	}
}

func TestSubscriptionExpiringStartsLateAndNotRepeatedAfterRestart(t *testing.T) {
	r := newSubRig()
	r.at("2026-11-19")
	evs := r.eval()
	if len(evs) != 1 || !strings.Contains(evs[0].Message, "in 2 days (2026-11-19)") || !strings.Contains(evs[0].Message, "DEVELOPERPACK_E5") {
		t.Fatalf("late start: %+v", evs)
	}
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("repeat: %+v", evs)
	}
	// An unconfirmed alert is handed out again only after RetryAfter.
	r2 := newSubRig()
	r2.at("2026-11-19")
	first, _ := Evaluate(Input{Now: r2.now, Start: r2.start, Config: r2.cfg, Accounts: []config.Account{r2.acct}, State: r2.st})
	again, _ := Evaluate(Input{Now: r2.now.Add(10 * time.Minute), Start: r2.start, Config: r2.cfg, Accounts: []config.Account{r2.acct}, State: r2.st})
	later, _ := Evaluate(Input{Now: r2.now.Add(RetryAfter + time.Minute), Start: r2.start, Config: r2.cfg, Accounts: []config.Account{r2.acct}, State: r2.st})
	if len(first) != 1 || len(again) != 0 || len(later) != 1 || later[0].Subscription != "sub-1" {
		t.Errorf("retry: %d %d %d", len(first), len(again), len(later))
	}
	if !MarkEvent(r2.st, later[0]) || r2.sub.Notified[SubscriptionExpiring].Pending {
		t.Error("delivery not recorded on the subscription")
	}
}

func TestSubscriptionOnlyWatchedSKUs(t *testing.T) {
	r := newSubRig()
	r.at("2026-11-18")
	r.sub.SKU = "ENTERPRISEPACK"
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("an unwatched SKU alerted: %+v", evs)
	}
	r.cfg = config.Default().Notify
	r.cfg.Cooldown = "0"
	evs, _ := Evaluate(Input{Now: r.now, Start: r.start, Config: r.cfg, Accounts: []config.Account{r.acct}, State: r.st,
		Subscription: config.Subscription{Skus: []string{"ENTERPRISE"}}})
	if len(evs) != 1 {
		t.Fatalf("configured SKU not watched: %+v", evs)
	}
}

func TestSubscriptionStatusChangeAndRecovery(t *testing.T) {
	r := newSubRig()
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("first sight of an Enabled subscription: %+v", evs)
	}
	r.sub.Status = "Warning"
	evs := r.eval()
	if kinds(evs) != "subscription_status" || evs[0].Level() != config.LevelCritical ||
		!strings.Contains(evs[0].Message, "from Enabled to Warning") {
		t.Fatalf("worse: %+v", evs)
	}
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("repeat: %+v", evs)
	}
	r.sub.Status = StatusEnabled
	evs = r.eval()
	if kinds(evs) != "subscription_status" || evs[0].Level() != config.LevelInfo || !strings.Contains(evs[0].Message, "Enabled again (was Warning)") {
		t.Fatalf("recovered: %+v", evs)
	}
}

func TestSubscriptionFirstSightOfBadStatus(t *testing.T) {
	r := newSubRig()
	r.sub.Status = "Suspended"
	evs := r.eval()
	if kinds(evs) != "subscription_status" || !strings.Contains(evs[0].Message, "is Suspended") {
		t.Fatalf("%+v", evs)
	}
}

func TestSubscriptionDeletedDoesNotAlsoExpire(t *testing.T) {
	r := newSubRig()
	r.at("2026-11-18")
	r.sub.Status = "Deleted"
	if k := kinds(r.eval()); k != "subscription_status" {
		t.Errorf("deleted: %q", k)
	}
}

func TestSubscriptionRenewedStartsOverAndOldNewsIsDropped(t *testing.T) {
	r := newSubRig()
	r.at("2026-11-20")
	if k := kinds(r.eval()); k != "subscription_expiring" {
		t.Fatalf("approach: %q", k)
	}
	// Renewed by three months.
	r.sub.History = []store.Renewal{{From: r.sub.NextLifecycle, To: time.Date(2027, 2, 20, 0, 0, 0, 0, time.UTC), SeenAt: r.now}}
	r.at("2027-02-20")
	evs := r.eval()
	if kinds(evs) != "subscription_renewed" || evs[0].Level() != config.LevelInfo ||
		!strings.Contains(evs[0].Message, "from 2026-11-20 to 2027-02-20") {
		t.Fatalf("renewed: %+v", evs)
	}
	if _, ok := r.sub.Notified[SubscriptionExpiring]; ok {
		t.Error("the old approach is still on record")
	}
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("repeat: %+v", evs)
	}
	// The next approach is announced again.
	r.now = time.Date(2027, 2, 1, 10, 0, 0, 0, time.UTC)
	if k := kinds(r.eval()); k != "subscription_expiring" {
		t.Errorf("next approach: %q", k)
	}
	// A renewal seen long ago (no destination at that time) is not news.
	old := newSubRig()
	old.sub.History = []store.Renewal{{From: old.sub.NextLifecycle.AddDate(0, -3, 0), To: old.sub.NextLifecycle, SeenAt: old.now.Add(-30 * 24 * time.Hour)}}
	if evs := old.eval(); len(evs) != 0 {
		t.Errorf("old renewal announced: %+v", evs)
	}
}

func TestSubscriptionEventFilterStillTracksState(t *testing.T) {
	r := newSubRig()
	r.cfg.Events = []string{"subscription_renewed"}
	r.sub.Status = "Warning"
	r.at("2026-11-18")
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("filtered events sent: %q", k)
	}
	if r.sub.AnnouncedStatus != "Warning" || r.sub.Notified[SubscriptionStatus].Pending {
		t.Errorf("filtered status not tracked as settled: %+v", r.sub)
	}
	r.cfg.Events = nil
	if k := kinds(r.eval()); k != "" {
		t.Errorf("a filtered event came back later: %q", k)
	}
}

func TestSubscriptionQuietHoursHoldInfoNotCritical(t *testing.T) {
	r := newSubRig()
	r.cfg.QuietHours = "23:00-08:00"
	r.now = time.Date(2026, 11, 17, 2, 0, 0, 0, time.UTC)
	r.sub.Status = "Warning"                                                                                               // critical: goes out at night
	r.sub.History = []store.Renewal{{From: r.sub.NextLifecycle.AddDate(0, -3, 0), To: r.sub.NextLifecycle, SeenAt: r.now}} // info: held
	evs := r.eval()
	if kinds(evs) != "subscription_status" {
		t.Fatalf("night: %q", kinds(evs))
	}
	r.now = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	evs = r.eval()
	if kinds(evs) != "subscription_renewed" || evs[0].Subscription != "sub-1" {
		t.Fatalf("morning: %+v", evs)
	}
	if r.sub.Notified[SubscriptionRenewed].Pending {
		t.Error("the held renewal was not settled after delivery")
	}
	// Two held together become one digest that settles both.
	r2 := newSubRig()
	r2.cfg.QuietHours = "23:00-08:00"
	r2.now = time.Date(2026, 11, 17, 2, 0, 0, 0, time.UTC)
	r2.sub.History = []store.Renewal{{From: r2.sub.NextLifecycle.AddDate(0, -3, 0), To: r2.sub.NextLifecycle, SeenAt: r2.now}}
	r2.at("2026-12-10") // 23 days left: the 30-day threshold is a warning, held too
	if evs := r2.eval(); len(evs) != 0 {
		t.Fatalf("held at night: %+v", evs)
	}
	r2.now = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	evs = r2.eval()
	if len(evs) != 1 || evs[0].Event != Digest || len(evs[0].Items) != 2 {
		t.Fatalf("digest: %+v", evs)
	}
	if r2.sub.Notified[SubscriptionExpiring].Pending || r2.sub.Notified[SubscriptionRenewed].Pending {
		t.Error("digest did not settle the subscription alerts")
	}
}

func TestSubscriptionStatusCooldown(t *testing.T) {
	r := newSubRig()
	r.cfg.Cooldown = "6h"
	r.eval()
	r.sub.Status = "Warning"
	if k := kinds(r.eval()); k != "subscription_status" {
		t.Fatalf("first: %q", k)
	}
	r.now = r.now.Add(time.Hour)
	r.sub.Status = StatusEnabled // flaps back within the cooldown: kept back
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("inside the cooldown: %q", k)
	}
	r.now = r.now.Add(3 * time.Hour) // critical halves the cooldown; this one is info, so 6h from the first
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("still cooling: %q", k)
	}
	r.now = r.now.Add(3 * time.Hour)
	if k := kinds(r.eval()); k != "subscription_status" {
		t.Fatalf("after the cooldown: %q", k)
	}
}

func TestSubscriptionManualDateAlertsUntilRealDataArrives(t *testing.T) {
	r := newRig()
	r.acct = config.Account{Name: "main", Auth: config.AuthDeviceCode, Tenant: subTenant, SubscriptionExpires: "2026-11-20"}
	eval := func() []Event {
		evs, _ := Evaluate(Input{Now: r.now, Start: r.start, Config: r.cfg, Accounts: []config.Account{r.acct}, State: r.st})
		for _, e := range evs {
			MarkEvent(r.st, e)
		}
		return evs
	}
	evs := eval()
	if len(evs) != 1 || evs[0].Event != SubscriptionExpiring || evs[0].Subscription != "manual" || !strings.Contains(evs[0].Message, "in 3 days (2026-11-20)") {
		t.Fatalf("manual: %+v", evs)
	}
	if evs := eval(); len(evs) != 0 {
		t.Fatalf("manual repeated: %+v", evs)
	}
	if _, ok := r.st.Tenants[subTenant].Notified[SubscriptionExpiring]; !ok {
		t.Fatal("manual alert not recorded on the tenant")
	}
	// Real data arrives: it decides, and the manual record is dropped.
	r.st.Tenants[subTenant].Subscriptions = map[string]*store.SubscriptionState{"sub-1": {
		SKU: "DEVELOPERPACK_E5", Status: StatusEnabled, CheckedAt: r.now, AnnouncedStatus: StatusEnabled,
		NextLifecycle: time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)}}
	if evs := eval(); len(evs) != 0 {
		t.Fatalf("real data far away: %+v", evs)
	}
	if _, ok := r.st.Tenants[subTenant].Notified[SubscriptionExpiring]; ok {
		t.Error("manual record kept next to real data")
	}
}

func TestSubscriptionLevelsAndLabels(t *testing.T) {
	for key, want := range map[string]string{"30@2027-01-01": config.LevelWarning, "7@x": config.LevelCritical, "1@x": config.LevelCritical, "0@x": config.LevelCritical} {
		if got := LevelOfKey(SubscriptionExpiring, key); got != want {
			t.Errorf("expiring %s = %s, want %s", key, got, want)
		}
	}
	if LevelOfKey(SubscriptionStatus, "Enabled") != config.LevelInfo || LevelOfKey(SubscriptionStatus, "LockedOut") != config.LevelCritical || LevelOf(SubscriptionRenewed) != config.LevelInfo {
		t.Error("status and renewed levels")
	}
	for _, k := range []string{SubscriptionExpiring, SubscriptionStatus, SubscriptionRenewed} {
		for _, lang := range []string{"zh", "en"} {
			if EventLabel(k, lang) == k {
				t.Errorf("no %s label for %s", lang, k)
			}
		}
		if ev := SampleEvent(k, t0); ev.Message == "" || ev.Subscription == "" || ev.Account != "" {
			t.Errorf("sample %s: %+v", k, ev)
		}
	}
}

func TestSubscriptionEventsCarryTenantAndSubscriptionFields(t *testing.T) {
	r := newSubRig()
	r.at("2026-11-19")
	r.acct.Tenant = "Contoso.OnMicrosoft.Com"
	r.st.TenantAliases = map[string]string{"contoso.onmicrosoft.com": "0b3c0b0e-1111-4222-8333-444455556666"}
	r.st.Tenants = map[string]*store.TenantState{"0b3c0b0e-1111-4222-8333-444455556666": r.st.Tenants[subTenant]}
	evs := r.eval()
	if len(evs) != 1 {
		t.Fatalf("events = %+v", evs)
	}
	e := evs[0]
	if e.Tenant != "0b3c0b0e-1111-4222-8333-444455556666" || e.SKU != "DEVELOPERPACK_E5" || e.SubStatus != "Enabled" || e.EndDate != "2026-11-19" {
		t.Fatalf("event = %+v", e)
	}
	if !strings.Contains(e.Message, "of tenant Contoso.OnMicrosoft.Com") {
		t.Errorf("the message names the tenant the way the configuration does: %s", e.Message)
	}
	b, _ := json.Marshal(e)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["tenant"] != e.Tenant || got["subscription"] != "sub-1" || got["sku"] != "DEVELOPERPACK_E5" || got["status"] != "Enabled" || got["ends"] != "2026-11-19" || got["event"] != "subscription_expiring" {
		t.Errorf("payload = %v", got)
	}
	// Other events keep their six-plus fields without the new ones.
	b, _ = json.Marshal(SampleEvent(Stale, t0))
	if strings.Contains(string(b), `"tenant"`) || strings.Contains(string(b), `"sku"`) {
		t.Errorf("a plain event grew subscription fields: %s", b)
	}
	// The built-in template shows the tenant ID of a subscription event.
	for _, lang := range []string{"zh", "en"} {
		d := NewData(e, lang, "box", time.UTC)
		rd, err := Render("telegram", lang, "", d)
		if err != nil || !strings.Contains(rd.Body, "0b3c0b0e-1111-4222-8333-444455556666") {
			t.Errorf("%s body lacks the tenant ID: %v\n%s", lang, err, rd.Body)
		}
		if d.SKU != "DEVELOPERPACK_E5" || d.EndDate != "2026-11-19" || d.Tenant == "" {
			t.Errorf("template data: %+v", d)
		}
	}
	// And a custom template can use the fields.
	rd, err := Render("slack", "en", "{{define \"title\"}}t{{end}}{{define \"body\"}}{{.Tenant}} {{.SKU}} {{.SubStatus}} {{.EndDate}}{{end}}", NewData(e, "en", "box", time.UTC))
	if err != nil || !strings.Contains(rd.Body, "0b3c0b0e-1111-4222-8333-444455556666 DEVELOPERPACK_E5 Enabled 2026-11-19") {
		t.Errorf("custom template: %v %q", err, rd.Body)
	}
}
