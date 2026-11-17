package notify

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"os"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"
	"unicode"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

// AppName is the product name shown in messages.
const AppName = "E5 Developer Extend"

//go:embed templates/*.tmpl
var builtinFS embed.FS

// Data is what a template sees. It never holds a secret: tokens, passwords
// and webhook addresses are not available to templates at all.
type Data struct {
	App        string // product name
	Host       string // machine name
	Event      string // event kind, e.g. failure_spike
	EventLabel string // short name of the event in the template's language
	Level      string // info, warning or critical
	LevelLabel string
	Account    string // account name; empty for global events
	Tenant     string // tenant ID (or configured tenant) of the account or subscription; built-in templates show it only for subscription events, inside Message
	Message    string // what happened, without secrets
	Time       string // local time of the event
	Hint       string // a suggested next step
	// FailRate is the failure rate of a failure_spike, such as "62%"; Failures and Rounds are its counts.
	FailRate string
	Failures int
	Rounds   int
	// SKU, SubStatus and EndDate (YYYY-MM-DD) describe the subscription of a subscription event.
	SKU, SubStatus, EndDate string
	// Origin is, for a recovery, the kind of the event that ended; OriginLabel is its name.
	Origin      string
	OriginLabel string
	// Digest lists the held events when the message is a summary; empty otherwise.
	Digest []DigestItem
	// Summary has one row per account for a daily_summary; empty otherwise.
	Summary []SummaryData
}

// SummaryData is one account of a daily summary, ready to show. Line is the
// whole row as one sentence in the template's language.
type SummaryData struct {
	Account               string
	OK, Failed, Throttled int
	LastSuccess           string // local time, or "-" when there has been none
	State, StateLabel     string
	PausedAPIs            int
	SubDays               string // days left of the E5 subscription; empty when unknown
	Line                  string
}

// DigestItem is one event of a summary.
type DigestItem struct {
	Event, EventLabel, Level, LevelLabel, Account, Tenant, Message, Time, Hint string
}

// Rendered is a finished message. Title is a subject line; Body is in the
// format the channel type expects (HTML for Telegram, Markdown for WeCom, ...);
// HTML is the HTML part of an email.
type Rendered struct {
	Title, Body, HTML string
}

// Language resolves a channel's language: its own, else the default, else
// the one the environment suggests.
func Language(channel, def string) string {
	for _, l := range []string{channel, def} {
		if l == "zh" || l == "en" {
			return l
		}
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			if strings.HasPrefix(strings.ToLower(v), "zh") {
				return "zh"
			}
			return "en"
		}
	}
	return "en"
}

var labels = map[string]map[string]string{
	"zh": {
		NeedsAttention: "需要处理", Recovered: "已恢复", AutoPaused: "已自动暂停", Stale: "长时间没有成功调用",
		SecretExpiring: "密钥即将过期", PeriodEnded: "运行周期已结束", FailureSpike: "调用失败率过高",
		AllFailing: "所有账号同时异常", SubscriptionExpiring: "E5 订阅即将到期", SubscriptionStatus: "E5 订阅状态变化",
		SubscriptionRenewed: "E5 订阅已续期", Test: "测试消息", Digest: "免打扰期间的告警汇总", DailySummary: "每日摘要",
		"state.ok": "正常", "state.needs_attention": "需要处理", "state.paused": "已自动暂停", "state.paused_by_user": "已手动暂停", "state.period_ended": "运行周期已结束",
		"level.info": "提示", "level.warning": "警告", "level.critical": "严重",
	},
	"en": {
		NeedsAttention: "Needs attention", Recovered: "Recovered", AutoPaused: "Paused automatically",
		Stale: "No successful call for a long time", SecretExpiring: "Client secret expiring",
		PeriodEnded: "Run period ended", FailureSpike: "High failure rate", AllFailing: "All accounts failing",
		SubscriptionExpiring: "E5 subscription expiring", SubscriptionStatus: "E5 subscription status changed",
		SubscriptionRenewed: "E5 subscription renewed", Test: "Test message", Digest: "Alerts held during quiet hours", DailySummary: "Daily summary",
		"state.ok": "OK", "state.needs_attention": "needs attention", "state.paused": "paused automatically", "state.paused_by_user": "paused by you", "state.period_ended": "run period ended",
		"level.info": "Info", "level.warning": "Warning", "level.critical": "Critical",
	},
}

// EventLabel is the short name of an event kind in lang.
func EventLabel(kind, lang string) string {
	if l, ok := labels[lang][kind]; ok {
		return l
	}
	return kind
}

func levelLabel(level, lang string) string { return labels[lang]["level."+level] }

// Hint suggests a next step for an event, in lang.
func Hint(kind, account, lang string) string {
	zh := lang == "zh"
	pick := func(z, e string) string {
		if zh {
			return z
		}
		return e
	}
	acct := account
	if acct == "" {
		acct = "<name>"
	}
	switch kind {
	case NeedsAttention:
		return pick("运行 e5extend doctor 查看原因，按提示处理后会自动恢复。", "Run e5extend doctor to see why; it resumes by itself once fixed.")
	case AutoPaused:
		return pick("先修复原因，再运行 e5extend account resume "+acct+"。", "Fix the cause, then run: e5extend account resume "+acct)
	case Stale:
		return pick("运行 e5extend status 和 e5extend doctor，检查网络、代理和账号状态。", "Run e5extend status and e5extend doctor; check the network, the proxy and the account.")
	case SecretExpiring:
		return pick("在 Entra 里新建客户端密钥，然后运行 e5extend account set-secret "+acct+"。", "Create a new client secret in Entra, then run: e5extend account set-secret "+acct)
	case PeriodEnded:
		return pick("需要继续运行时，在设置里调整运行周期。", "To keep running, change the run period in the settings.")
	case FailureSpike:
		return pick("运行 e5extend doctor 查看失败的调用类型（限流 429、5xx、网络错误）；账号本身没有问题时通常会自己恢复。", "Run e5extend doctor to see which calls fail (429 throttling, 5xx, network errors); it usually clears by itself.")
	case SubscriptionExpiring:
		return pick("在 Microsoft 365 管理中心续订；运行 e5extend subscription 查看当前到期日和续期记录。", "Renew it in the Microsoft 365 admin center; run e5extend subscription to see the end date and the renewal history.")
	case SubscriptionStatus:
		return pick("运行 e5extend subscription 查看当前状态，并在 Microsoft 365 管理中心（账单 > 你的产品）核对订阅。", "Run e5extend subscription to see the status, and check the subscription in the Microsoft 365 admin center (Billing > Your products).")
	case AllFailing:
		return pick("先检查网络、代理和 Microsoft 365 服务状态，再逐个看账号。", "Check the network, the proxy and the Microsoft 365 service status first, then each account.")
	}
	return ""
}

// NewData builds the template data of an event. loc and host may be nil/empty.
func NewData(ev Event, lang, host string, loc *time.Location) Data {
	if loc == nil {
		loc = time.Local
	}
	t := ev.Time
	if t.IsZero() {
		t = time.Now()
	}
	d := Data{
		App: AppName, Host: host, Event: ev.Event, EventLabel: EventLabel(ev.Event, lang),
		Level: ev.Level(), LevelLabel: levelLabel(ev.Level(), lang), Account: ev.Account, Tenant: ev.PublicTenant(),
		Message: ev.Message, Time: t.In(loc).Format("2006-01-02 15:04 MST"), Hint: Hint(ev.Event, ev.Account, lang),
		Failures: ev.Failures, Rounds: ev.Rounds, Origin: ev.Origin,
	}
	if ev.Subscription != "" {
		d.SKU, d.SubStatus, d.EndDate = ev.SKU, ev.SubStatus, ev.EndDate
		// The message names the tenant the way the configuration does; add its ID when that is another string.
		if t := ev.PublicTenant(); t != "" && !strings.Contains(ev.Message, t) {
			d.Message += " (tenant " + t + ")"
		}
	}
	if ev.IsTest {
		// Titles and headlines are built from the label, so the mark shows everywhere.
		d.EventLabel = map[string]string{"zh": "[测试] ", "en": "[Test] "}[lang] + d.EventLabel
	}
	if ev.Event == FailureSpike && ev.Rounds > 0 {
		d.FailRate = fmt.Sprintf("%.0f%%", ev.FailRate*100)
	}
	if ev.Origin != "" {
		d.OriginLabel = EventLabel(ev.Origin, lang)
	}
	for _, r := range ev.Summary {
		d.Summary = append(d.Summary, summaryData(r, lang, loc))
	}
	for _, it := range ev.Items {
		x := NewData(it, lang, host, loc)
		d.Digest = append(d.Digest, DigestItem{Event: x.Event, EventLabel: x.EventLabel, Level: x.Level,
			LevelLabel: x.LevelLabel, Account: x.Account, Tenant: x.Tenant, Message: x.Message, Time: x.Time, Hint: x.Hint})
	}
	return d
}

// summaryData turns a summary row into the text a message shows.
func summaryData(r SummaryRow, lang string, loc *time.Location) SummaryData {
	zh := lang == "zh"
	d := SummaryData{Account: r.Account, OK: r.OK, Failed: r.Failed, Throttled: r.Throttled, State: r.State,
		StateLabel: labels[lang]["state."+r.State], PausedAPIs: r.PausedAPIs, LastSuccess: "-"}
	if d.StateLabel == "" {
		d.StateLabel = r.State
	}
	if !r.LastSuccess.IsZero() {
		d.LastSuccess = r.LastSuccess.In(loc).Format("01-02 15:04")
	}
	if r.HasSubDays {
		d.SubDays = strconv.Itoa(r.SubDays)
	}
	var b strings.Builder
	if zh {
		fmt.Fprintf(&b, "%s：成功 %d，失败 %d", r.Account, r.OK, r.Failed)
		if r.Throttled > 0 {
			fmt.Fprintf(&b, "，限流 %d", r.Throttled)
		}
		fmt.Fprintf(&b, "；最近成功 %s；%s", d.LastSuccess, d.StateLabel)
		if r.PausedAPIs > 0 {
			fmt.Fprintf(&b, "；%d 个 API 已暂停 24 小时", r.PausedAPIs)
		}
		if r.HasSubDays {
			fmt.Fprintf(&b, "；E5 订阅%s", subDaysText(r.SubDays, true))
		}
	} else {
		fmt.Fprintf(&b, "%s: %d ok, %d failed", r.Account, r.OK, r.Failed)
		if r.Throttled > 0 {
			fmt.Fprintf(&b, ", %d throttled", r.Throttled)
		}
		fmt.Fprintf(&b, "; last success %s; %s", d.LastSuccess, d.StateLabel)
		if r.PausedAPIs > 0 {
			fmt.Fprintf(&b, "; %d APIs paused for 24 h", r.PausedAPIs)
		}
		if r.HasSubDays {
			fmt.Fprintf(&b, "; E5 subscription %s", subDaysText(r.SubDays, false))
		}
	}
	d.Line = b.String()
	return d
}

func subDaysText(days int, zh bool) string {
	switch {
	case days < 0 && zh:
		return fmt.Sprintf("已过期 %d 天", -days)
	case days < 0:
		return fmt.Sprintf("ended %d days ago", -days)
	case zh:
		return fmt.Sprintf("还剩 %d 天", days)
	case days == 1:
		return "1 day left"
	}
	return fmt.Sprintf("%d days left", days)
}

// mdEscaper backslash-escapes what Markdown (WeCom, DingTalk, Discord, Server
// Chan) would read as formatting or markup, so untrusted text stays text.
var mdEscaper = strings.NewReplacer(
	"\\", `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`, "(", `\(`, ")", `\)`,
	"#", `\#`, ">", `\>`, "<", `\<`, "~", `\~`, "|", `\|`, "!", `\!`, "&", `\&`,
)

// mdSafeReplacer is for WeCom and DingTalk, whose Markdown does not honour
// backslash escapes reliably: instead of escaping, whatever could start a
// link, formatting, a heading or a mention is replaced by a look-alike.
var mdSafeReplacer = strings.NewReplacer(
	"<", "\u2039", ">", "\u203a", "[", "\uff3b", "]", "\uff3d", "*", "\uff0a", "`", "'",
	"#", "\uff03", "~", "\uff5e", "|", "\u00a6", "@", "\uff20",
)

// mdSafe applies mdSafeReplacer and also neutralises underscores, except the
// ones inside a word (my_acct stays copy-pasteable); only leading, trailing
// or standalone ones can make emphasis.
func mdSafe(s string) string {
	s = mdSafeReplacer.Replace(s)
	r := []rune(s)
	word := func(i int) bool { return i >= 0 && i < len(r) && (unicode.IsLetter(r[i]) || unicode.IsDigit(r[i])) }
	for i, c := range r {
		if c == '_' && !(word(i-1) && word(i+1)) {
			r[i] = '\uff3f'
		}
	}
	return string(r)
}

// slackEscaper is what Slack asks for in text: & < > only (they start links and mentions).
var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

var funcs = template.FuncMap{
	// md escapes text for Markdown; slack escapes it for Slack mrkdwn.
	"md":    func(s string) string { return mdEscaper.Replace(s) },
	"slack": func(s string) string { return slackEscaper.Replace(s) },
	// mdsafe neutralises Markdown by look-alike substitution (WeCom, DingTalk).
	"mdsafe": mdSafe,
	// json encodes a value as a JSON literal, for bodies written by hand.
	"json": func(v any) (string, error) { b, err := json.Marshal(v); return string(b), err },
	// trunc cuts s to at most n characters.
	"trunc": func(n int, s string) string {
		r := []rune(s)
		if len(r) <= n {
			return s
		}
		return string(r[:n])
	},
}

type tmplSet struct {
	text *template.Template
	html *htmltemplate.Template // nil when the source defines no "html" block
}

var (
	builtinMu    sync.Mutex
	builtinCache = map[string]*tmplSet{}
)

// BuiltinTypes are the channel types that have built-in templates.
var BuiltinTypes = []string{config.ChannelTelegram, config.ChannelEmail, config.ChannelWeCom, config.ChannelFeishu,
	config.ChannelDingTalk, config.ChannelDiscord, config.ChannelSlack, config.ChannelBark, config.ChannelNtfy, config.ChannelServerChan}

// Builtin returns the source of the built-in template of a type and language.
func Builtin(typ, lang string) (string, error) {
	b, err := builtinFS.ReadFile("templates/" + typ + "." + lang + ".tmpl")
	if err != nil {
		return "", fmt.Errorf("no built-in template for %s/%s", typ, lang)
	}
	return string(b), nil
}

func parseSet(name, src string) (*tmplSet, error) {
	tt, err := template.New(name).Funcs(funcs).Parse(src)
	if err != nil {
		return nil, err
	}
	set := &tmplSet{text: tt}
	if tt.Lookup("html") != nil {
		ht, err := htmltemplate.New(name).Funcs(htmltemplate.FuncMap(funcs)).Parse(src)
		if err != nil {
			return nil, err
		}
		set.html = ht
	}
	return set, nil
}

func builtinSet(typ, lang string) (*tmplSet, error) {
	key := typ + "." + lang
	builtinMu.Lock()
	defer builtinMu.Unlock()
	if s := builtinCache[key]; s != nil {
		return s, nil
	}
	src, err := Builtin(typ, lang)
	if err != nil {
		return nil, err
	}
	s, err := parseSet(key, src)
	if err != nil {
		return nil, fmt.Errorf("built-in template %s: %w", key, err)
	}
	builtinCache[key] = s
	return s, nil
}

func run(t *template.Template, name string, d Data) (string, error) {
	if t.Lookup(name) == nil {
		return "", nil
	}
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, name, d); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

// Render renders ev for a channel of the given type. A non-empty custom is
// used in place of the built-in template: it may define the blocks "title",
// "body" and (for email) "html"; without a "body" block the whole text is the
// body. Blocks it does not define come from the built-in template.
func Render(typ, lang, custom string, d Data) (Rendered, error) {
	if lang != "zh" && lang != "en" {
		lang = "en"
	}
	var r Rendered
	var builtin *tmplSet
	if typ != config.ChannelWebhook {
		var err error
		if builtin, err = builtinSet(typ, lang); err != nil {
			return r, err
		}
	}
	set := builtin
	if set == nil && strings.TrimSpace(custom) == "" {
		return r, errors.New("template: this channel type has no built-in template; give one")
	}
	wholeIsBody := false
	if strings.TrimSpace(custom) != "" {
		cs, err := parseSet("custom", custom)
		if err != nil {
			return r, fmt.Errorf("template: %w", err)
		}
		wholeIsBody = cs.text.Lookup("body") == nil
		set = cs
	}
	var err error
	if wholeIsBody {
		var b bytes.Buffer
		if err = set.text.Execute(&b, d); err != nil {
			return r, fmt.Errorf("template: %w", err)
		}
		r.Body = strings.TrimSpace(b.String())
	} else if r.Body, err = run(set.text, "body", d); err != nil {
		return r, fmt.Errorf("template: %w", err)
	}
	if r.Title, err = run(set.text, "title", d); err != nil {
		return r, fmt.Errorf("template: %w", err)
	}
	if r.Title == "" && builtin != nil {
		if r.Title, err = run(builtin.text, "title", d); err != nil {
			return r, err
		}
	}
	if typ == config.ChannelEmail {
		switch {
		case set.html != nil:
			var b bytes.Buffer
			if err := set.html.ExecuteTemplate(&b, "html", d); err != nil {
				return r, fmt.Errorf("template: %w", err)
			}
			r.HTML = b.String()
		case set != builtin:
			r.HTML = `<pre style="font-family:inherit;white-space:pre-wrap">` + htmltemplate.HTMLEscapeString(r.Body) + `</pre>`
		}
	}
	return r, nil
}

// SampleEvent returns an example event of a kind, for previews and for
// checking templates before they are saved. It uses made-up names only.
func SampleEvent(kind string, now time.Time) Event {
	ev := Event{Event: kind, Account: "demo", Time: now}
	switch kind {
	case NeedsAttention:
		ev.Message = "needs attention: login required; run: e5extend account login demo"
	case Recovered:
		ev.Message, ev.Origin = "is working again", NeedsAttention
	case AutoPaused:
		ev.Message = "paused automatically: 5 rounds in a row failed (last error: server_error); fix the cause, then run: e5extend account resume demo"
	case Stale:
		ev.Message = "no successful call for 80h (last success " + now.Add(-80*time.Hour).Format("2006-01-02 15:04 MST") + ")"
	case SecretExpiring:
		ev.Message = "client secret expires in 7 days (" + now.AddDate(0, 0, 7).Format("2006-01-02") + "); create a new one and run: e5extend account set-secret"
	case PeriodEnded:
		ev.Message = "the run period has ended; no more rounds will run"
	case SubscriptionExpiring:
		ev.Account, ev.Subscription, ev.Severity = "", "sub-1", config.LevelCritical
		ev.Message = "subscription DEVELOPERPACK_E5 of tenant contoso.onmicrosoft.com expires in 7 days (" + now.AddDate(0, 0, 7).UTC().Format("2006-01-02") + "); renew it in the Microsoft 365 admin center"
	case SubscriptionStatus:
		ev.Account, ev.Subscription, ev.Severity = "", "sub-1", config.LevelCritical
		ev.Message = "subscription DEVELOPERPACK_E5 of tenant contoso.onmicrosoft.com changed from Enabled to Warning; check it in the Microsoft 365 admin center (Billing > Your products)"
	case SubscriptionRenewed:
		ev.Account, ev.Subscription = "", "sub-1"
		ev.Message = "subscription DEVELOPERPACK_E5 of tenant contoso.onmicrosoft.com was renewed: the end date moved from " + now.AddDate(0, 0, 3).UTC().Format("2006-01-02") + " to " + now.AddDate(0, 3, 3).UTC().Format("2006-01-02")
	case FailureSpike:
		ev.Message, ev.Failures, ev.Rounds, ev.FailRate = "9 of the last 12 calls failed (75%) over the last 3 rounds", 9, 3, 0.75
	case AllFailing:
		ev.Account = ""
		ev.Message = "all 3 accounts that are running are failing; check the network, the proxy and the Microsoft 365 service status first"
	case Test:
		ev.Account = ""
		ev.Message = "this is a test alert; alerts are working"
	case DailySummary:
		ev.Account = ""
		ev.Message = "daily summary: 2 accounts, 41 calls ok, 1 failed, 2 throttled in the last 24 hours"
		ev.Summary = []SummaryRow{
			{Account: "demo", OK: 29, Failed: 0, Throttled: 2, LastSuccess: now.Add(-25 * time.Minute), State: SumOK, SubDays: 87, HasSubDays: true},
			{Account: "demo-2", OK: 12, Failed: 1, LastSuccess: now.Add(-3 * time.Hour), State: SumNeedsAttention, PausedAPIs: 2},
		}
	case Digest:
		ev.Account = ""
		ev.Message = "2 alerts were held during the quiet hours"
		ev.Items = []Event{SampleEvent(Stale, now), SampleEvent(SecretExpiring, now)}
	}
	if ev.Subscription != "" {
		ev.Tenant, ev.SKU, ev.SubStatus, ev.EndDate = "0b3c0b0e-1111-4222-8333-444455556666", "DEVELOPERPACK_E5", "Enabled", now.AddDate(0, 0, 7).Format("2006-01-02")
		if kind == SubscriptionStatus {
			ev.SubStatus = "Warning"
		}
	}
	return ev
}

// SampleDigest is a quiet-hours summary of n example items.
func SampleDigest(n int, now time.Time) Event {
	d := SampleEvent(Digest, now)
	d.Items = nil
	kinds := []string{Stale, SecretExpiring, FailureSpike, Recovered, PeriodEnded}
	for i := 0; i < n; i++ {
		it := SampleEvent(kinds[i%len(kinds)], now)
		it.Account = fmt.Sprintf("demo-%d", i+1)
		it.IsTest = true
		d.Items = append(d.Items, it)
	}
	d.Message = fmt.Sprintf("%d alerts were held during the quiet hours", n)
	return d
}

// SampleKinds are the kinds a template is checked with.
func SampleKinds() []string {
	return append(append([]string(nil), config.NotifyEvents...), Test, Digest)
}

// CheckTemplate verifies that a custom template of a channel type parses and
// renders every kind of event with example data, in both languages. For a
// webhook with format template the output must also be valid JSON.
func CheckTemplate(typ, custom string, webhookJSON bool) error {
	if strings.TrimSpace(custom) == "" {
		return nil
	}
	now := time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC)
	for _, lang := range []string{"zh", "en"} {
		for _, kind := range SampleKinds() {
			d := NewData(SampleEvent(kind, now), lang, "host", time.UTC)
			r, err := Render(typ, lang, custom, d)
			if err != nil {
				return fmt.Errorf("%w (checked with the example %s event)", err, kind)
			}
			if strings.TrimSpace(r.Body) == "" {
				return fmt.Errorf("template: the message is empty for the example %s event", kind)
			}
			if webhookJSON && !json.Valid([]byte(r.Body)) {
				return fmt.Errorf("template: the result is not valid JSON for the example %s event (use {{json .Message}} for text)", kind)
			}
		}
	}
	return nil
}
