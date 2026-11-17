package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// summaryRig is a rig with the daily summary on at 21:00 and a day of calls.
func summaryRig() *spikeRig {
	r := newSpikeRig()
	r.cfg.DailySummaryAt = "21:00"
	r.now = time.Date(2026, 11, 17, 21, 0, 30, 0, time.UTC)
	r.as.LastSuccess = r.now.Add(-30 * time.Minute)
	call := func(ago time.Duration, class string, status int) store.Entry {
		return store.Entry{TS: r.now.Add(-ago), Account: "main", Round: "r1", API: "user.me", Class: class, Status: status}
	}
	r.calls["main"] = []store.Entry{
		call(time.Hour, "ok", 200), call(2*time.Hour, "ok", 200), call(3*time.Hour, "ok", 200),
		call(4*time.Hour, "server_error", 500), call(5*time.Hour, "throttled", 429),
		call(6*time.Hour, "not_found", 404), call(7*time.Hour, "forbidden", 403), // in none of the counts
		call(30*time.Hour, "ok", 200), // too old
	}
	return r
}

func summaryOf(t *testing.T, evs []Event) Event {
	t.Helper()
	for _, e := range evs {
		if e.Event == DailySummary {
			return e
		}
	}
	t.Fatalf("no daily_summary among %v", kinds(evs))
	return Event{}
}

func TestDailySummaryIsOffByDefault(t *testing.T) {
	r := summaryRig()
	r.cfg.DailySummaryAt = ""
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("got %v", kinds(evs))
	}
	// Events filtered: no summary even with a time set.
	r = summaryRig()
	r.cfg.Events = []string{NeedsAttention}
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("got %v", kinds(evs))
	}
}

func TestDailySummaryContentAndLevel(t *testing.T) {
	r := summaryRig()
	r.as.APISkipUntil = map[string]time.Time{"mail.list": r.now.Add(time.Hour), "old": r.now.Add(-time.Hour)}
	ev := summaryOf(t, r.eval())
	if ev.Level() != config.LevelInfo || ev.Account != "" || len(ev.Summary) != 1 {
		t.Fatalf("%+v", ev)
	}
	row := ev.Summary[0]
	if row.Account != "main" || row.OK != 3 || row.Failed != 1 || row.Throttled != 1 || row.State != SumOK || row.PausedAPIs != 1 ||
		!row.LastSuccess.Equal(r.as.LastSuccess) || row.HasSubDays {
		t.Errorf("row %+v", row)
	}
	if !strings.Contains(ev.Message, "3 calls ok, 1 failed, 1 throttled") {
		t.Errorf("message %q", ev.Message)
	}
	// The JSON body carries the rows.
	b, err := json.Marshal(ev)
	if err != nil || !strings.Contains(string(b), `"summary":[{"account":"main","ok":3,"failed":1,"throttled":1`) {
		t.Errorf("json %s %v", b, err)
	}
}

func TestDailySummaryStatesAndSubscriptionDays(t *testing.T) {
	r := summaryRig()
	r.as.Status = store.StatusPaused
	r.acct.Tenant = "contoso.onmicrosoft.com"
	r.st.Tenants = map[string]*store.TenantState{"contoso.onmicrosoft.com": {Subscriptions: map[string]*store.SubscriptionState{
		"s1": {SKU: "DEVELOPERPACK_E5", Status: "Enabled", NextLifecycle: r.now.AddDate(0, 0, 40)},
		"s2": {SKU: "DEVELOPERPACK_E5", Status: "Enabled", NextLifecycle: r.now.AddDate(0, 0, 12)},
	}}}
	ev := summaryOf(t, r.eval())
	row := ev.Summary[0]
	if row.State != SumPaused || !row.HasSubDays || row.SubDays != 12 {
		t.Errorf("row %+v", row)
	}
	r2 := summaryRig()
	r2.acct.Paused = true
	if got := summaryOf(t, r2.eval()).Summary[0].State; got != SumPausedByUser {
		t.Errorf("manual pause shows as %s", got)
	}
	r3 := summaryRig()
	r3.as.Status = store.StatusNeedsAttention
	r3.as.Reason = "login required"
	evs := r3.eval()
	if got := summaryOf(t, evs).Summary[0].State; got != SumNeedsAttention {
		t.Errorf("attention shows as %s", got)
	}
}

func TestDailySummaryScheduling(t *testing.T) {
	r := summaryRig()
	r.now = time.Date(2026, 11, 17, 20, 59, 0, 0, time.UTC)
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("sent before its time: %v", kinds(evs))
	}
	r.now = time.Date(2026, 11, 17, 21, 0, 0, 0, time.UTC)
	if kinds(r.eval()) != "daily_summary" {
		t.Fatal("not sent at its time")
	}
	// Once a day: not again, also not after a restart (the state holds it).
	r.now = r.now.Add(time.Hour)
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("sent twice: %v", kinds(evs))
	}
	// The next day it is due again.
	r.now = time.Date(2026, 11, 18, 21, 5, 0, 0, time.UTC)
	if kinds(r.eval()) != "daily_summary" {
		t.Fatal("not sent the next day")
	}
}

func TestDailySummaryNotSentLongAfterItsTime(t *testing.T) {
	r := summaryRig()
	// Switched on in the morning: today's time is ahead and yesterday's is long gone.
	r.now = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("stale summary sent: %v", kinds(evs))
	}
	r.now = time.Date(2026, 11, 17, 21, 1, 0, 0, time.UTC)
	if kinds(r.eval()) != "daily_summary" {
		t.Fatal("first summary missing")
	}
}

func TestDailySummaryRetriesUntilDelivered(t *testing.T) {
	r := summaryRig()
	if kinds(r.evalNoAck()) != "daily_summary" {
		t.Fatal("first try")
	}
	r.now = r.now.Add(10 * time.Minute)
	if evs := r.evalNoAck(); len(evs) != 0 {
		t.Fatalf("retried too soon: %v", kinds(evs))
	}
	r.now = r.now.Add(RetryAfter)
	evs := r.evalNoAck()
	if kinds(evs) != "daily_summary" || len(evs[0].Summary) != 1 {
		t.Fatalf("retry: %v", kinds(evs))
	}
	MarkEvent(r.st, evs[0])
	r.now = r.now.Add(RetryAfter)
	if got := r.eval(); len(got) != 0 {
		t.Fatalf("sent after delivery: %v", kinds(got))
	}
}

func TestDailySummaryWaitsForQuietHoursToEnd(t *testing.T) {
	r := summaryRig()
	r.cfg.QuietHours = "20:00-07:00"
	r.now = time.Date(2026, 11, 17, 21, 0, 0, 0, time.UTC)
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("sent inside the quiet hours: %v", kinds(evs))
	}
	r.now = time.Date(2026, 11, 18, 6, 59, 0, 0, time.UTC)
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("sent inside the quiet hours: %v", kinds(evs))
	}
	r.now = time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC)
	ev := summaryOf(t, r.eval())
	if ev.Level() != config.LevelInfo {
		t.Errorf("level %s", ev.Level())
	}
	// Not twice, and the 21:00 of that day is a new one.
	r.now = r.now.Add(time.Hour)
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("sent twice: %v", kinds(evs))
	}
	// The next evening it waits again.
	r.now = time.Date(2026, 11, 18, 21, 0, 0, 0, time.UTC)
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("sent inside the quiet hours: %v", kinds(evs))
	}
}

func TestDailySummaryRendersForEveryChannel(t *testing.T) {
	ev := SampleEvent(DailySummary, tNow)
	for _, typ := range BuiltinTypes {
		for _, lang := range []string{"zh", "en"} {
			r, err := Render(typ, lang, "", NewData(ev, lang, "dev-box", time.UTC))
			if err != nil {
				t.Fatalf("%s/%s: %v", typ, lang, err)
			}
			wantLabel := map[string]string{"zh": "每日摘要", "en": "Daily summary"}[lang]
			if !strings.Contains(r.Title, wantLabel) {
				t.Errorf("%s/%s: title %q", typ, lang, r.Title)
			}
			text := r.Body
			if typ == config.ChannelEmail {
				text += r.HTML
			}
			wants := []string{"demo", "demo-2", "29", "87"}
			if lang == "zh" {
				wants = append(wants, "成功", "失败", "限流", "还剩 87 天", "需要处理", "2 个 API 已暂停 24 小时")
			} else {
				wants = append(wants, "ok", "failed", "throttled", "87 days left", "needs attention", "2 APIs paused for 24 h")
			}
			for _, w := range wants {
				// Markdown channels escape or replace characters; look at a bare word instead.
				if !strings.Contains(text, w) && !strings.Contains(strings.NewReplacer("\\", "").Replace(text), w) {
					t.Errorf("%s/%s: body lacks %q:\n%s", typ, lang, w, text)
				}
			}
		}
	}
}

func TestDailySummaryKeepsAccountNamesSafeInMarkup(t *testing.T) {
	ev := SampleEvent(DailySummary, tNow)
	ev.Summary[0].Account = "<b>x</b>"
	tg, _ := Render("telegram", "en", "", NewData(ev, "en", "h", time.UTC))
	if strings.Contains(tg.Body, "<b>x</b>") || !strings.Contains(tg.Body, "&lt;b&gt;x") {
		t.Errorf("telegram body not escaped:\n%s", tg.Body)
	}
	em, _ := Render("email", "en", "", NewData(ev, "en", "h", time.UTC))
	if strings.Contains(em.HTML, "<b>x</b>") {
		t.Errorf("email html not escaped:\n%s", em.HTML)
	}
}

func TestDailySummaryAtIsValidated(t *testing.T) {
	for _, s := range []string{"", "21:00", "07:05"} {
		if _, _, err := (config.Notify{DailySummaryAt: s}).ParseDailySummaryAt(); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"9pm", "25:00", "21:00-22:00", "21"} {
		if _, _, err := (config.Notify{DailySummaryAt: s}).ParseDailySummaryAt(); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
	c := config.Default()
	c.Notify.DailySummaryAt = "late"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "notify.daily_summary_at") {
		t.Errorf("Validate: %v", err)
	}
}
