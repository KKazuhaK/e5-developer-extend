package engine

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
	"github.com/KKazuhaK/e5-developer-extend/internal/subscription"
)

const subTenantName = "contoso.onmicrosoft.com"

func tenantAcct(name, tenant string) config.Account {
	return config.Account{Name: name, Auth: config.AuthDeviceCode, Tenant: tenant, ClientID: testGUID}
}

var healthySubs = subsBody(fakeSub{"sub-1", "DEVELOPERPACK_E5", "Enabled", "2027-02-14T00:00:00Z", false})

type fakeSub struct {
	id, sku, status, next string
	trial                 bool
}

// subsBody renders a /directory/subscriptions answer.
func subsBody(items ...fakeSub) string {
	var parts []string
	for _, s := range items {
		parts = append(parts, fmt.Sprintf(`{"id":%q,"skuPartNumber":%q,"status":%q,"nextLifecycleDateTime":%q,"isTrial":%v,"totalLicenses":25,"ownerTenantId":"tid-1"}`,
			s.id, s.sku, s.status, s.next, s.trial))
	}
	return `{"value":[` + strings.Join(parts, ",") + `]}`
}

// subFixture runs with the subscription check on and one answer that tests can change.
type subFixture struct {
	*fixture
	n    *fakeNotifier
	mu   sync.Mutex
	code int
	body string
}

func newSubFixture(t *testing.T, accts ...config.Account) *subFixture {
	f := &subFixture{fixture: newFixture(t, accts...), n: &fakeNotifier{}, code: 200}
	f.opts.NoSubscriptionCheck = false
	f.opts.Notifier = f.n
	f.body = healthySubs
	f.h.subs = func(r *http.Request) (int, string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") == "" {
			return 401, ""
		}
		return f.code, f.body
	}
	return f
}

func (f *subFixture) answer(code int, body string) {
	f.mu.Lock()
	f.code, f.body = code, body
	f.mu.Unlock()
}

func (f *subFixture) hits() int {
	f.h.mu.Lock()
	defer f.h.mu.Unlock()
	return f.h.subsHits
}

func (f *subFixture) tenant(key string) store.TenantState {
	f.e.mu.Lock()
	defer f.e.mu.Unlock()
	ts := f.e.state.Tenants[key]
	if ts == nil {
		f.t.Fatalf("no state for tenant %q", key)
	}
	return *ts
}

// subEvents are the subscription alerts sent so far.
func (f *subFixture) subEvents() []notify.Event {
	f.n.mu.Lock()
	defer f.n.mu.Unlock()
	var out []notify.Event
	for _, e := range f.n.events {
		if strings.HasPrefix(e.Event, "subscription_") {
			out = append(out, e)
		}
	}
	return out
}

// round runs a round and the tick after it, which is where the check starts
// once the round has finished.
func (f *subFixture) round(name string) {
	f.t.Helper()
	f.fixture.round(name)
	f.clock.Advance(time.Second)
	f.tickWait()
}

// day moves a day and a minute on and lets the engine tick (and check).
func (f *subFixture) day() { f.jump(24*time.Hour + time.Minute) }

func TestSubscriptionCheckWaitsForFirstRoundThenReadsOnce(t *testing.T) {
	f := newSubFixture(t, tenantAcct("main", subTenantName))
	f.start()
	f.tickWait() // nothing is due yet: no round has run
	if f.hits() != 0 {
		t.Fatalf("checked before the first round: %d", f.hits())
	}
	f.round("main")
	if f.hits() != 1 {
		t.Fatalf("hits after the first round = %d", f.hits())
	}
	ts := f.tenant(subTenantName)
	sub := ts.Subscriptions["sub-1"]
	if ts.Access != store.AccessOK || ts.TenantID != "tid-1" || sub == nil || sub.SKU != "DEVELOPERPACK_E5" ||
		sub.Status != "Enabled" || sub.NextLifecycle.Format("2006-01-02") != "2027-02-14" || sub.CheckedAt.IsZero() || sub.TotalLicenses != 25 {
		t.Fatalf("state = %+v / %+v", ts, sub)
	}
	// Once per 24 hours, whatever the tick rate.
	for i := 0; i < 20; i++ {
		f.clock.Advance(time.Hour)
		f.tickWait()
	}
	if f.hits() != 1 {
		t.Errorf("checked again within a day: %d", f.hits())
	}
	f.clock.Advance(5 * time.Hour)
	f.tickWait()
	if f.hits() != 2 {
		t.Errorf("not checked after a day: %d", f.hits())
	}
	if ev := f.subEvents(); len(ev) != 0 {
		t.Errorf("a healthy far subscription alerted: %+v", ev)
	}
	if !f.hasEvent("subscription_check") {
		t.Error("the check is not in the call log")
	}
}

func TestSubscriptionNextCheckSurvivesRestart(t *testing.T) {
	f := newSubFixture(t, tenantAcct("main", subTenantName))
	f.start()
	f.round("main")
	f.start() // a new process reads the same state
	f.round("main")
	if f.hits() != 1 {
		t.Errorf("a restart checked again: %d", f.hits())
	}
}

func TestSubscriptionExpiringAlertsOncePerThreshold(t *testing.T) {
	f := newSubFixture(t, tenantAcct("main", subTenantName))
	f.answer(200, subsBody(fakeSub{"sub-1", "DEVELOPERPACK_E5", "Enabled", "2026-12-25T00:00:00Z", false}))
	f.start()
	got := map[string]int{}
	for d := 0; d < 45; d++ {
		f.round("main")
		f.day()
		for _, e := range f.subEvents() {
			got[e.Event+"/"+e.Level()+"/"+e.Message[strings.Index(e.Message, "expires"):strings.Index(e.Message, "(")]]++
		}
		f.n.mu.Lock()
		f.n.events = nil
		f.n.mu.Unlock()
	}
	for k, n := range got {
		if n != 1 {
			t.Errorf("%s sent %d times", k, n)
		}
	}
	if len(got) < 4 {
		t.Errorf("alerts = %v", got)
	}
	var warn, crit int
	for k := range got {
		switch {
		case strings.Contains(k, "/warning/"):
			warn++
		case strings.Contains(k, "/critical/"):
			crit++
		}
	}
	if warn != 1 || crit != 3 { // 30 days warning; 7 days, 1 day and the day itself critical
		t.Errorf("warning=%d critical=%d in %v", warn, crit, got)
	}
}

func TestSubscriptionStatusWarningThenRecovered(t *testing.T) {
	f := newSubFixture(t, tenantAcct("main", subTenantName))
	f.start()
	f.round("main")
	f.answer(200, subsBody(fakeSub{"sub-1", "DEVELOPERPACK_E5", "Warning", "2027-02-14T00:00:00Z", false}))
	f.day()
	ev := f.subEvents()
	if len(ev) != 1 || ev[0].Event != "subscription_status" || ev[0].Level() != config.LevelCritical || !strings.Contains(ev[0].Message, "Enabled to Warning") {
		t.Fatalf("worse: %+v", ev)
	}
	f.day() // still Warning: not repeated
	if len(f.subEvents()) != 1 {
		t.Fatalf("repeated: %+v", f.subEvents())
	}
	f.answer(200, subsBody(fakeSub{"sub-1", "DEVELOPERPACK_E5", "Enabled", "2027-02-14T00:00:00Z", false}))
	f.day()
	ev = f.subEvents()
	if len(ev) != 2 || ev[1].Level() != config.LevelInfo || !strings.Contains(ev[1].Message, "Enabled again") {
		t.Fatalf("recovered: %+v", ev)
	}
}

func TestSubscriptionRenewalRecordedAndAnnounced(t *testing.T) {
	f := newSubFixture(t, tenantAcct("main", subTenantName))
	f.answer(200, subsBody(fakeSub{"sub-1", "DEVELOPERPACK_E5", "Enabled", "2027-02-14T00:00:00Z", false}))
	f.start()
	f.round("main")
	f.answer(200, subsBody(fakeSub{"sub-1", "DEVELOPERPACK_E5", "Enabled", "2027-05-14T00:00:00Z", false}))
	f.day()
	sub := f.tenant(subTenantName).Subscriptions["sub-1"]
	if len(sub.History) != 1 || sub.History[0].From.Format("2006-01-02") != "2027-02-14" || sub.History[0].To.Format("2006-01-02") != "2027-05-14" || sub.History[0].SeenAt.IsZero() {
		t.Fatalf("history = %+v", sub.History)
	}
	ev := f.subEvents()
	if len(ev) != 1 || ev[0].Event != "subscription_renewed" || ev[0].Level() != config.LevelInfo ||
		!strings.Contains(ev[0].Message, "from 2027-02-14 to 2027-05-14") {
		t.Fatalf("events = %+v", ev)
	}
	// The same date again is no renewal; an earlier date is not one either.
	f.day()
	f.answer(200, subsBody(fakeSub{"sub-1", "DEVELOPERPACK_E5", "Enabled", "2027-04-01T00:00:00Z", false}))
	f.day()
	if sub := f.tenant(subTenantName).Subscriptions["sub-1"]; len(sub.History) != 1 || len(f.subEvents()) != 1 {
		t.Errorf("history %+v events %+v", sub.History, f.subEvents())
	}
}

func TestSubscriptionHistoryKeepsTwentyAndSurvivesRestart(t *testing.T) {
	f := newSubFixture(t, tenantAcct("main", subTenantName))
	f.start()
	f.round("main")
	for i := 1; i <= 25; i++ {
		f.answer(200, subsBody(fakeSub{"sub-1", "DEVELOPERPACK_E5", "Enabled", time.Date(2027, 2, 14+i, 0, 0, 0, 0, time.UTC).Format(time.RFC3339), false}))
		f.day()
	}
	f.start()
	sub := f.tenant(subTenantName).Subscriptions["sub-1"]
	if len(sub.History) != store.MaxRenewals || sub.History[len(sub.History)-1].To.Format("2006-01-02") != "2027-03-11" {
		t.Errorf("history has %d entries, last %+v", len(sub.History), sub.History[len(sub.History)-1])
	}
}

func TestSubscriptionForbiddenIsAStateNotAnError(t *testing.T) {
	f := newSubFixture(t, tenantAcct("main", subTenantName))
	f.answer(403, `{"error":{"code":"Authorization_RequestDenied"}}`)
	f.start()
	f.round("main")
	ts := f.tenant(subTenantName)
	if ts.Access != store.AccessNoPermission || ts.Failures != 0 || ts.Detail != "" || len(ts.Subscriptions) != 0 {
		t.Fatalf("state = %+v", ts)
	}
	if k := f.n.kinds(""); len(k) != 0 {
		t.Errorf("a missing permission alerted: %v", k)
	}
	if as := f.acct("main"); as.Status != store.StatusOK {
		t.Errorf("the account was blamed: %+v", as)
	}
	f.clock.Advance(2 * time.Hour) // not retried before the next day
	f.tickWait()
	if f.hits() != 1 {
		t.Errorf("hits = %d", f.hits())
	}
	// Granting the permission shows up at the next check.
	f.answer(200, healthySubs)
	f.day()
	if ts := f.tenant(subTenantName); ts.Access != store.AccessOK || len(ts.Subscriptions) != 1 {
		t.Errorf("after the grant: %+v", ts)
	}
}

func TestSubscriptionManualDateAlertsWithoutPermission(t *testing.T) {
	a := tenantAcct("main", subTenantName)
	a.SubscriptionExpires = "2026-11-22" // 5 days from the fixture's day
	f := newSubFixture(t, a)
	f.answer(403, "")
	f.start()
	f.round("main")
	ev := f.subEvents()
	if len(ev) != 1 || ev[0].Subscription != "manual" || ev[0].Event != "subscription_expiring" || !strings.Contains(ev[0].Message, "in 5 days (2026-11-22)") {
		t.Fatalf("manual alert: %+v", ev)
	}
	f.round("main")
	if len(f.subEvents()) != 1 {
		t.Errorf("repeated: %+v", f.subEvents())
	}
	// Real data takes over: the manual record goes, nothing new is raised.
	f.answer(200, healthySubs)
	f.day()
	if ts := f.tenant(subTenantName); len(ts.Notified) != 0 || len(f.subEvents()) != 1 {
		t.Errorf("manual record kept: %+v / %d events", ts.Notified, len(f.subEvents()))
	}
}

func TestSubscriptionOneCheckPerTenantWithFirstUsableAccount(t *testing.T) {
	paused := tenantAcct("first", subTenantName)
	paused.Paused = true
	f := newSubFixture(t, paused, tenantAcct("second", "Contoso.OnMicrosoft.com"), tenantAcct("third", subTenantName), tenantAcct("other", "fabrikam.onmicrosoft.com"))
	f.start()
	f.round("second")
	f.round("third")
	f.round("other")
	if f.hits() != 2 { // one per tenant, not per account
		t.Fatalf("hits = %d, want 2", f.hits())
	}
	if f.auths["first"].calls != 0 || f.auths["third"].calls > 1 {
		// the paused account is never used; the third account only runs its own round
		t.Errorf("token calls: first %d third %d", f.auths["first"].calls, f.auths["third"].calls)
	}
	if len(f.e.state.Tenants) != 2 {
		t.Errorf("tenants = %v", f.e.state.Tenants)
	}
}

func TestSubscriptionTriesNextAccountWhenRefused(t *testing.T) {
	f := newSubFixture(t, tenantAcct("plain", subTenantName), tenantAcct("admin", subTenantName))
	f.h.mu.Lock()
	f.h.subs = func(r *http.Request) (int, string) { return 403, "" }
	f.h.mu.Unlock()
	f.start()
	f.round("plain")
	if ts := f.tenant(subTenantName); ts.Access != store.AccessNoPermission || f.hits() != 2 {
		t.Fatalf("both refused: %+v hits %d", ts, f.hits())
	}
	// Only the second account (an administrator) may read them.
	f.auths["admin"].tok.AccessToken = "admin-token"
	f.h.mu.Lock()
	f.h.subs = func(r *http.Request) (int, string) {
		if r.Header.Get("Authorization") == "Bearer admin-token" {
			return 200, healthySubs
		}
		return 403, ""
	}
	f.h.mu.Unlock()
	f.day()
	if ts := f.tenant(subTenantName); ts.Access != store.AccessOK || len(ts.Subscriptions) != 1 {
		t.Errorf("state = %+v", ts)
	}
}

func TestSubscriptionFailuresBackOffUpToSixHours(t *testing.T) {
	f := newSubFixture(t, tenantAcct("main", subTenantName))
	f.answer(500, "")
	f.start()
	f.round("main")
	if ts := f.tenant(subTenantName); ts.Access != store.AccessError || ts.Failures != 1 || !strings.Contains(ts.Detail, "HTTP 500") {
		t.Fatalf("after one failure: %+v", ts)
	}
	if k := f.n.kinds(""); len(k) != 0 {
		t.Errorf("a failed check alerted: %v", k)
	}
	want := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute, 160 * time.Minute, 320 * time.Minute, 6 * time.Hour, 6 * time.Hour}
	for i, w := range want {
		if got := subscription.Backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
	// Follow the real schedule: each retry waits the backoff, and not a minute less.
	for fails := 1; fails < 6; fails++ {
		wait := subscription.Backoff(fails)
		before := f.hits()
		f.clock.Advance(wait - time.Minute)
		f.tickWait()
		if f.hits() != before {
			t.Fatalf("retry %d came before %s", fails, wait)
		}
		f.clock.Advance(time.Minute)
		f.tickWait()
		if f.hits() != before+1 {
			t.Fatalf("retry %d did not come after %s", fails, wait)
		}
	}
	f.answer(200, healthySubs)
	f.clock.Advance(subscription.Backoff(6))
	f.tickWait()
	if ts := f.tenant(subTenantName); ts.Access != store.AccessOK || ts.Failures != 0 {
		t.Errorf("recovery: %+v", ts)
	}
	// And now it is back to daily.
	before := f.hits()
	f.clock.Advance(23 * time.Hour)
	f.tickWait()
	if f.hits() != before {
		t.Error("still on the backoff schedule after a success")
	}
}

func TestSubscriptionSignInFailureCountsAsFailure(t *testing.T) {
	f := newSubFixture(t, tenantAcct("main", subTenantName))
	f.start()
	f.round("main")
	f.auths["main"].setErr(fmt.Errorf("offline"))
	f.day()
	ts := f.tenant(subTenantName)
	if ts.Access != store.AccessError || ts.Failures != 1 || strings.Contains(ts.Detail, "offline") {
		t.Errorf("state = %+v", ts)
	}
}

func TestSubscriptionTriggerChecksNow(t *testing.T) {
	f := newSubFixture(t, tenantAcct("main", subTenantName))
	f.start()
	f.round("main")
	f.answer(200, subsBody(fakeSub{"sub-1", "DEVELOPERPACK_E5", "Suspended", "2027-02-14T00:00:00Z", false}))
	if err := TriggerSubscription(f.dir, "main"); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Minute)
	f.tickWait()
	if f.hits() != 2 || f.tenant(subTenantName).Subscriptions["sub-1"].Status != "Suspended" {
		t.Errorf("hits %d state %+v", f.hits(), f.tenant(subTenantName))
	}
	if ev := f.subEvents(); len(ev) != 1 || ev[0].Event != "subscription_status" {
		t.Errorf("events = %+v", ev)
	}
}

func TestCheckSubscriptionsOnceWithoutAnEngine(t *testing.T) {
	f := newSubFixture(t, tenantAcct("main", subTenantName), tenantAcct("other", "fabrikam.onmicrosoft.com"))
	if err := CheckSubscriptions(t.Context(), f.opts, []string{"main"}); err != nil {
		t.Fatal(err)
	}
	if f.hits() != 1 || f.n.closed != 1 {
		t.Errorf("hits %d, notifier closed %d", f.hits(), f.n.closed)
	}
	e, _ := New(f.opts)
	e.init()
	if e.state.Tenants[subTenantName] == nil || e.state.Tenants["fabrikam.onmicrosoft.com"] != nil {
		t.Errorf("tenants = %v", e.state.Tenants)
	}
}

func TestSubscriptionStateDroppedWithItsTenant(t *testing.T) {
	f := newSubFixture(t, tenantAcct("main", subTenantName))
	f.start()
	f.round("main")
	f.cfg.Accounts = []config.Account{tenantAcct("main", "fabrikam.onmicrosoft.com")}
	f.e.mu.Lock()
	f.e.cfg = f.cfg
	f.e.pruneTenantsLocked()
	f.e.mu.Unlock()
	if len(f.e.state.Tenants) != 0 {
		t.Errorf("tenants = %v", f.e.state.Tenants)
	}
}
