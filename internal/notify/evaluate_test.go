package notify

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/store"
)

var t0 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

func TestEventNamesMatchConfig(t *testing.T) {
	kinds := []string{NeedsAttention, Recovered, AutoPaused, Stale, SecretExpiring, PeriodEnded}
	if !slices.Equal(kinds, config.NotifyEvents) {
		t.Errorf("config.NotifyEvents = %v, notify kinds = %v", config.NotifyEvents, kinds)
	}
}

type rig struct {
	now   time.Time
	start time.Time
	cfg   config.Notify
	acct  config.Account
	st    *store.State
	as    *store.AccountState
}

func newRig() *rig {
	r := &rig{now: t0, start: t0.Add(-100 * time.Hour), cfg: config.Default().Notify,
		acct: config.Account{Name: "main", Auth: config.AuthClientSecret}, st: store.NewState()}
	r.as = &store.AccountState{Status: store.StatusOK, LastSuccess: t0.Add(-time.Hour)}
	r.st.Accounts["main"] = r.as
	return r
}

// evalNoAck evaluates without confirming delivery.
func (r *rig) evalNoAck() []Event {
	evs, _ := Evaluate(Input{Now: r.now, Start: r.start, Config: r.cfg, Accounts: []config.Account{r.acct}, State: r.st})
	return evs
}

// eval evaluates and confirms delivery of everything it returns.
func (r *rig) eval() []Event {
	evs := r.evalNoAck()
	for _, e := range evs {
		MarkDelivered(r.st, e.Account, e.Event, e.Time)
	}
	return evs
}

func kinds(evs []Event) string {
	var k []string
	for _, e := range evs {
		k = append(k, e.Event)
	}
	return strings.Join(k, ",")
}

func TestNeedsAttentionAnnouncedOnceThenRecovered(t *testing.T) {
	r := newRig()
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("healthy account: %v", kinds(evs))
	}
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "login required; run: e5extend account login main"
	evs := r.eval()
	if kinds(evs) != "needs_attention" || !strings.Contains(evs[0].Message, "login required") || evs[0].Account != "main" {
		t.Fatalf("first: %+v", evs)
	}
	for i := 0; i < 5; i++ { // same state, later ticks
		r.now = r.now.Add(10 * time.Minute)
		if evs := r.eval(); len(evs) != 0 {
			t.Fatalf("repeated: %v", kinds(evs))
		}
	}
	r.as.Status, r.as.Reason = store.StatusOK, ""
	if k := kinds(r.eval()); k != "recovered" {
		t.Fatalf("after recovery: %q", k)
	}
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("recovered twice: %q", k)
	}
	// A new episode is announced again.
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "login required"
	if k := kinds(r.eval()); k != "needs_attention" {
		t.Fatalf("second episode: %q", k)
	}
}

func TestAutoPausedAndRecoveredFromPause(t *testing.T) {
	r := newRig()
	r.as.Status, r.as.Reason = store.StatusPaused, "3 rounds in a row failed"
	if k := kinds(r.eval()); k != "auto_paused" {
		t.Fatalf("got %q", k)
	}
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("repeat %q", k)
	}
	r.as.Status = store.StatusOK
	if k := kinds(r.eval()); k != "recovered" {
		t.Fatalf("got %q", k)
	}
}

func TestPausedByConfigDoesNotRecoverOrGoStale(t *testing.T) {
	r := newRig()
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "x"
	r.eval()
	r.as.Status = store.StatusOK
	r.acct.Paused = true
	r.as.LastSuccess = r.now.Add(-30 * 24 * time.Hour)
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("paused account: %q", k)
	}
	r.acct.Paused = false
	r.as.LastSuccess = r.now.Add(-time.Hour)
	if k := kinds(r.eval()); k != "recovered" {
		t.Fatalf("after unpausing: %q", k)
	}
}

func TestStaleRepeatsAtMostDaily(t *testing.T) {
	r := newRig()
	r.as.LastSuccess = r.now.Add(-73 * time.Hour)
	if k := kinds(r.eval()); k != "stale" {
		t.Fatalf("first: %q", k)
	}
	var sent int
	// Two more days of ticks every 30 minutes.
	for i := 0; i < 96; i++ {
		r.now = r.now.Add(30 * time.Minute)
		sent += len(r.eval())
	}
	if sent != 2 {
		t.Errorf("stale reminders over 48h = %d, want 2", sent)
	}
	r.as.LastSuccess = r.now
	if k := kinds(r.eval()); k != "recovered" {
		t.Fatalf("after success: %q", k)
	}
}

func TestStaleNeedsGraceAfterStart(t *testing.T) {
	r := newRig()
	r.as.LastSuccess = r.now.Add(-100 * time.Hour)
	r.start = r.now.Add(-10 * time.Minute) // engine just started after a long outage
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("within grace: %q", k)
	}
	r.now = r.now.Add(2 * time.Hour)
	if k := kinds(r.eval()); k != "stale" {
		t.Fatalf("after grace: %q", k)
	}
}

func TestStaleIgnoresNeverSucceededAndPeriodEnded(t *testing.T) {
	r := newRig()
	r.as.LastSuccess = time.Time{}
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("never succeeded: %q", k)
	}
	r.as.LastSuccess = r.now.Add(-200 * time.Hour)
	r.as.Status = store.StatusPeriodEnded
	if k := kinds(r.eval()); k != "period_ended" {
		t.Fatalf("period ended: %q", k)
	}
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("period ended twice: %q", k)
	}
}

func TestSecretExpiryThresholds(t *testing.T) {
	r := newRig()
	r.acct.SecretExpires = "2026-12-20" // 33 days from t0
	var got []string
	for d := 0; d <= 40; d++ {
		r.as.LastSuccess = r.now
		for _, e := range r.eval() {
			got = append(got, e.Event+"@"+r.now.Format("01-02")+":"+strings.Fields(e.Message)[3])
		}
		r.now = r.now.Add(24 * time.Hour)
	}
	// 30 days before is Nov 20, 7 days Dec 13, 1 day Dec 19, the day itself Dec 20.
	want := []string{"secret_expiring@11-20:in", "secret_expiring@12-13:in", "secret_expiring@12-19:tomorrow", "secret_expiring@12-20:today"}
	if !slices.Equal(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func TestSecretExpiryStartingLateAndReset(t *testing.T) {
	r := newRig()
	r.acct.SecretExpires = "2026-11-19" // 2 days left: the 7-day threshold is the first one reached
	if evs := r.eval(); len(evs) != 1 || !strings.Contains(evs[0].Message, "in 2 days") {
		t.Fatalf("late start: %+v", evs)
	}
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("repeat: %+v", evs)
	}
	// Already expired when first seen: one message only.
	r2 := newRig()
	r2.acct.SecretExpires = "2026-11-01"
	if evs := r2.eval(); len(evs) != 1 || !strings.Contains(evs[0].Message, "expired on 2026-11-01") {
		t.Fatalf("expired: %+v", evs)
	}
	if evs := r2.eval(); len(evs) != 0 {
		t.Fatalf("expired repeat: %+v", evs)
	}
	// A renewed secret (later date) clears the record, so the next approach is announced again.
	r2.acct.SecretExpires = "2027-11-01"
	r2.eval()
	if _, ok := r2.as.Notified[SecretExpiring]; ok {
		t.Error("record not cleared by a later expiry date")
	}
	r2.acct.SecretExpires = "2026-11-20"
	if len(r2.eval()) != 1 {
		t.Error("new approach not announced")
	}
}

func TestSecretExpiryOnlyForAccountsWithSecrets(t *testing.T) {
	r := newRig()
	r.acct.Auth = config.AuthDeviceCode
	r.acct.SecretExpires = "2026-11-18"
	if evs := r.eval(); len(evs) != 0 {
		t.Errorf("device_code account: %+v", evs)
	}
}

func TestEventFilterStillTracksState(t *testing.T) {
	r := newRig()
	r.cfg.Events = []string{"recovered"}
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "x"
	if k := kinds(r.eval()); k != "" {
		t.Fatalf("filtered event sent: %q", k)
	}
	r.as.Status = store.StatusOK
	if k := kinds(r.eval()); k != "recovered" {
		t.Fatalf("recovered must still follow: %q", k)
	}
}

func TestEvaluateReportsChangeAndBoundsState(t *testing.T) {
	r := newRig()
	_, changed := Evaluate(Input{Now: r.now, Start: r.start, Config: r.cfg, Accounts: []config.Account{r.acct}, State: r.st})
	if changed {
		t.Error("healthy account changed state")
	}
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "x"
	_, changed = Evaluate(Input{Now: r.now, Start: r.start, Config: r.cfg, Accounts: []config.Account{r.acct}, State: r.st})
	if !changed {
		t.Error("announcement not reported as a change")
	}
	for i := 0; i < 1000; i++ { // flapping must not grow the record set
		r.as.Status = store.StatusOK
		r.eval()
		r.as.Status = store.StatusNeedsAttention
		r.eval()
	}
	if len(r.as.Notified) > 7 {
		t.Errorf("notified grew to %d records", len(r.as.Notified))
	}
}

func TestMissingAccountStateIsIgnored(t *testing.T) {
	r := newRig()
	r.acct.Name = "other"
	if evs := r.eval(); len(evs) != 0 {
		t.Errorf("%+v", evs)
	}
}

// A restart must not turn an announced stale condition into "recovered" or
// announce it again.
func TestStaleSurvivesRestartGrace(t *testing.T) {
	r := newRig()
	r.as.LastSuccess = r.now.Add(-80 * time.Hour)
	if k := kinds(r.eval()); k != "stale" {
		t.Fatalf("first: %q", k)
	}
	// The engine restarts 5 hours later; the account has not succeeded yet.
	r.now = r.now.Add(5 * time.Hour)
	r.start = r.now
	for i := 0; i < 5; i++ {
		if evs := r.eval(); len(evs) != 0 {
			t.Fatalf("during the grace period: %v", kinds(evs))
		}
		r.now = r.now.Add(10 * time.Minute)
	}
	if _, ok := r.as.Notified[Stale]; !ok {
		t.Fatal("stale record dropped during the grace period")
	}
	if _, ok := r.as.Notified[recordUnhealthy]; !ok {
		t.Fatal("episode marker dropped during the grace period")
	}
	// After the grace period it is still the same episode: no immediate repeat, no recovered.
	r.now = r.now.Add(30 * time.Minute)
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("after the grace period: %v", kinds(evs))
	}
	// A real success newer than the threshold ends it, even inside a grace period.
	r.start = r.now
	r.as.LastSuccess = r.now.Add(-time.Minute)
	if k := kinds(r.eval()); k != "recovered" {
		t.Fatalf("after success: %q", k)
	}
}

func TestUndeliveredAlertIsRetriedOncePerHour(t *testing.T) {
	r := newRig()
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "login required"
	if k := kinds(r.evalNoAck()); k != "needs_attention" {
		t.Fatalf("first: %q", k)
	}
	sent := 0
	for i := 0; i < 24*60/5; i++ { // a day of ticks every 5 minutes with the webhook down
		r.now = r.now.Add(5 * time.Minute)
		for _, e := range r.evalNoAck() {
			sent++
			if e.Message == "" || e.Event != "needs_attention" {
				t.Fatalf("retry lost its content: %+v", e)
			}
		}
	}
	if sent < 22 || sent > 24 {
		t.Errorf("%d retries in a day, want about one per hour", sent)
	}
	// Back up: one delivery, then silence.
	r.now = r.now.Add(time.Hour)
	if k := kinds(r.evalNoAck()); k != "needs_attention" {
		t.Fatalf("retry: %q", k)
	}
	if !MarkDelivered(r.st, "main", "needs_attention", r.now) {
		t.Fatal("not pending")
	}
	for i := 0; i < 50; i++ {
		r.now = r.now.Add(5 * time.Minute)
		if evs := r.evalNoAck(); len(evs) != 0 {
			t.Fatalf("after delivery: %v", kinds(evs))
		}
	}
}

func TestSeveralPendingAlertsEachRetriedOnce(t *testing.T) {
	r := newRig()
	r.acct.SecretExpires = "2026-11-19"
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "x"
	if k := kinds(r.evalNoAck()); k != "needs_attention,secret_expiring" {
		t.Fatalf("first: %q", k)
	}
	r.now = r.now.Add(61 * time.Minute)
	if k := kinds(r.evalNoAck()); k != "needs_attention,secret_expiring" {
		t.Fatalf("retry: %q", k)
	}
	r.now = r.now.Add(10 * time.Minute)
	if k := kinds(r.evalNoAck()); k != "" {
		t.Fatalf("flood: %q", k)
	}
}

func TestUndeliveredRecoveredIsRetriedAndDropsOnNewProblem(t *testing.T) {
	r := newRig()
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "x"
	r.eval()
	r.as.Status = store.StatusOK
	if k := kinds(r.evalNoAck()); k != "recovered" {
		t.Fatalf("got %q", k)
	}
	r.now = r.now.Add(2 * time.Hour)
	if k := kinds(r.evalNoAck()); k != "recovered" {
		t.Fatalf("retry: %q", k)
	}
	// A new problem makes the old "recovered" obsolete.
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "again"
	if k := kinds(r.evalNoAck()); k != "needs_attention" {
		t.Fatalf("new problem: %q", k)
	}
	if _, ok := r.as.Notified[Recovered]; ok {
		t.Error("obsolete recovered still pending")
	}
	r.as.Status = store.StatusOK
	r.evalNoAck()
	if !MarkDelivered(r.st, "main", Recovered, r.now) {
		t.Fatal("recovered not pending")
	}
	if _, ok := r.as.Notified[Recovered]; ok {
		t.Error("delivered recovered not forgotten")
	}
}

func TestRenewedSecretStartsOver(t *testing.T) {
	r := newRig()
	r.acct.SecretExpires = "2026-11-19"
	if k := kinds(r.eval()); k != "secret_expiring" {
		t.Fatalf("first: %q", k)
	}
	// Renewed, but the new secret also runs out soon: it must be announced again
	// even though its level is not lower than the recorded one.
	r.acct.SecretExpires = "2026-11-24"
	evs := r.eval()
	if len(evs) != 1 || !strings.Contains(evs[0].Message, "2026-11-24") {
		t.Fatalf("renewed secret: %+v", evs)
	}
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("repeat: %+v", evs)
	}
}

func TestLateDeliveryDoesNotSettleNewerAttempt(t *testing.T) {
	st := store.NewState()
	first := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	st.Accounts["main"] = &store.AccountState{Notified: map[string]store.Notice{
		"needs_attention": {Pending: true, Tried: first.Add(time.Hour), Msg: "again"},
	}}
	if MarkDelivered(st, "main", "needs_attention", first) {
		t.Fatal("a callback for an earlier attempt settled the newer one")
	}
	if !MarkDelivered(st, "main", "needs_attention", first.Add(time.Hour)) {
		t.Fatal("the matching attempt was not settled")
	}
}
