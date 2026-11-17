package doctor

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
)

// Msg is one message of a check: a stable code and the values that fill its
// placeholders. The English text (see Render) is what the CLI prints; a user
// interface that speaks another language looks the code up in its own
// dictionary and falls back to the English text for a code it does not know.
//
// Params with a fixed meaning: "seconds" is a duration or an age in seconds,
// "offset" is a number of days from today (negative: in the past). Every other
// value is plain text or a number.
type Msg struct {
	Code   string            `json:"code"`
	Params map[string]string `json:"params,omitempty"`
}

// M builds a message from a code and key, value pairs.
func M(code string, kv ...string) Msg {
	m := Msg{Code: code}
	if len(kv) > 0 {
		m.Params = make(map[string]string, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			m.Params[kv[i]] = kv[i+1]
		}
	}
	return m
}

func itoa(n int) string { return strconv.Itoa(n) }

func secs(d time.Duration) string { return strconv.FormatInt(int64(d/time.Second), 10) }

// English renders the message in English.
func (m Msg) English() string { return Render(m.Code, m.Params) }

// Render fills the English template of code. An unknown code renders as "".
func Render(code string, params map[string]string) string {
	tpl, ok := english[code]
	if !ok {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < len(tpl); i++ {
		if tpl[i] != '{' {
			sb.WriteByte(tpl[i])
			continue
		}
		end := strings.IndexByte(tpl[i:], '}')
		if end < 0 {
			sb.WriteString(tpl[i:])
			break
		}
		sb.WriteString(placeholder(tpl[i+1:i+end], params))
		i += end
	}
	return sb.String()
}

func placeholder(name string, params map[string]string) string {
	switch name {
	case "dur", "age":
		n, _ := strconv.ParseInt(params["seconds"], 10, 64)
		d := time.Duration(n) * time.Second
		if name == "age" {
			return humanAge(d)
		}
		return d.String()
	case "when":
		n, _ := strconv.Atoi(params["offset"])
		return daysPhrase(n)
	}
	return params[name]
}

// english holds the English wording of every message. A code is stable: a
// translation is keyed by it, so reword the text freely but never reuse a code
// for a different meaning.
var english = map[string]string{
	// configuration
	"config.missing":    "no configuration file yet",
	"config.invalid":    "{reason}",
	"config.noAccounts": "the configuration is valid but has no accounts",
	"config.ok":         "valid, {count} account(s)",

	// vault
	"vault.openFailed":    "{reason}",
	"vault.cannotLook":    "{account}: cannot look for stored data",
	"vault.secretMissing": "{account}: the secret is missing",
	"vault.wrongKey":      "{account}: cannot decrypt stored data (wrong master key?)",
	"vault.noMasterKey":   "{account}: no master key available",
	"vault.other":         "{account}: {reason}",
	"vault.empty":         "no encrypted data stored yet",
	"vault.ok":            "master key works; {count} stored item(s) decrypt",

	// engine, service, linger
	"engine.stopped":       "the engine is not running",
	"engine.stoppedSeen":   "the engine is not running (last seen {at})",
	"engine.stale":         "the engine holds its lock but its heartbeat is older than 2 minutes",
	"engine.running":       "running (heartbeat {dur} ago)",
	"service.unsupported":  "background services are not supported on this platform",
	"service.error":        "cannot ask the service manager: {reason}",
	"service.notInstalled": "not installed (fine if you start e5extend some other way)",
	"service.disabled":     "installed but disabled",
	"service.notRunning":   "installed and enabled, but not running",
	"service.ok":           "installed and enabled",
	"service.okRunning":    "installed and enabled, running",
	"linger.off":           "lingering is off: the service stops when you log out",
	"linger.on":            "lingering is on",

	// sign-in
	"token.ok":              "token ok, valid for {dur}",
	"token.okNoExpiry":      "token ok",
	"token.signInFrequency": auth.SignInFrequencyLabel,
	"token.loginRequired":   "login required",
	"token.invalidClient":   "client credentials rejected",
	"token.consent":         "admin consent required",
	"token.transient":       "could not reach the sign-in service (network or service problem)",
	"token.noMasterKey":     "the vault master key is not available",
	"token.wrongKey":        "stored data cannot be decrypted (wrong master key?)",
	"token.failed":          "sign-in failed ({code})",

	// permissions
	"scopes.catalogBroken": "the built-in API list is broken: {reason}",
	"scopes.none":          "no API in the catalog can be called with the granted permissions",
	"scopes.noneRecorded":  "no API in the catalog can be called with the granted permissions (as recorded by the last round)",
	"scopes.few":           "only {count} API(s) usable; more permissions give more variety",
	"scopes.fewRecorded":   "only {count} API(s) usable (as recorded by the last round); more permissions give more variety",
	"scopes.ok":            "{count} APIs usable",
	"scopes.okRecorded":    "{count} APIs usable (as recorded by the last round)",

	// activity and state
	"activity.paused":      "paused in the configuration",
	"activity.none":        "no successful call recorded yet",
	"activity.last":        "last successful call {age} ago",
	"state.paused":         "paused in the configuration",
	"state.needsAttention": "needs attention: {reason}",
	"state.autoPaused":     "paused automatically: {reason}",
	"state.autoResume":     "it is tried again automatically in {dur}",
	"state.periodEnded":    "the run period has ended",
	"state.ok":             "ok",

	// subscription
	"sub.unchecked":          "not checked yet; the engine checks once a day (or run: e5extend subscription)",
	"sub.noPermission":       "cannot read the subscription: Organization.Read.All is needed (add it to the app of account {account} with: {command})",
	"sub.noPermissionManual": "cannot read the subscription (Organization.Read.All is needed)",
	"sub.lastFailed":         "the last check failed: {detail}",
	"sub.manual":             "the date you entered ({date}) is {when}",
	"sub.sku":                "{sku} {status}",
	"sub.skuEnds":            "{sku} {status}, ends {date} ({when})",
	"sub.noneFound":          "no E5 developer subscription found in the tenant (watched SKUs: {skus})",
	"sub.noneWatched":        "{count} subscription(s), none of the watched SKUs",
	"sub.manualRedundant":    "the date entered by hand is no longer needed, remove subscription_expires",

	// client secret
	"secret.badDate": "secret_expires is not a YYYY-MM-DD date",
	"secret.expired": "expired on {date}",
	"secret.today":   "expires today",
	"secret.soon":    "expires in {count} day(s), on {date}",
	"secret.warn":    "expires in {count} days, on {date}",
	"secret.ok":      "expires in {count} days",

	// network and clock
	"net.badAddress":    "bad Graph address: {reason}",
	"net.unreachable":   "cannot reach Microsoft Graph: {reason}",
	"net.ok":            "Microsoft Graph answered (HTTP {status})",
	"net.trouble":       "Microsoft Graph answered HTTP {status}; the service may be having trouble",
	"clock.unreachable": "could not compare the clock because Graph is unreachable",
	"clock.noDate":      "Graph's answer has no usable Date header",
	"clock.ahead":       "this machine is {dur} ahead of Graph's clock",
	"clock.behind":      "this machine is {dur} behind Graph's clock",
	"clock.ok":          "within {dur} of Graph's clock",
	"clock.skipped":     "not checked",

	// alerts
	"notify.undecryptable":       "{type} channel {id}: the stored {field} cannot be decrypted, so nothing can be sent there",
	"notify.fieldMissing":        "{type} channel {id}: {field} is not set",
	"notify.plainHTTP":           "{type} channel {id}: talks to its server over http, so the token travels unencrypted",
	"notify.mailNoEncrypt":       "{type} channel {id}: the mail connection is not encrypted",
	"notify.allowHTTP":           "{type} channel {id}: allow_http is set, so its address may be http and travel unencrypted",
	"notify.allowPlainAuth":      "{type} channel {id}: allow_plain_auth is set, so the password is sent without encryption",
	"notify.badTemplate":         "{type} channel {id}: the custom template does not work: {reason}",
	"notify.deliveriesFailed":    "channel {id}: the last {count} deliveries failed ({reason})",
	"notify.stale":               "channel {id}: nothing delivered in the last {days} days ({reason})",
	"notify.legacyHTTPStored":    "the stored webhook address is http, so it travels unencrypted",
	"notify.legacyHTTP":          "the webhook address is http, so it travels unencrypted",
	"notify.legacyUndecryptable": "the stored webhook address cannot be decrypted, so no alert can be sent",
	"notify.legacyPlain":         "the webhook address is stored in plain text in config.toml",
	"notify.none":                "no alert destination is set; you will not hear about problems",
	"notify.one":                 "1 notification channel set (secrets stored in the vault)",
	"notify.many":                "{count} notification channels set (secrets stored in the vault)",

	// API categories
	"apis.catalogBroken":       "the API catalog could not be read",
	"apis.logUnreadable":       "the call log could not be read",
	"apis.failing":             "category {category} keeps failing (not found)",
	"apis.failingNoMailbox":    "category {category} keeps failing (not found; no mailbox?)",
	"apis.failingNoOneDrive":   "category {category} keeps failing (not found; no OneDrive?)",
	"apis.failingNoTeams":      "category {category} keeps failing (not found; no Teams license?)",
	"apis.failingNoSharePoint": "category {category} keeps failing (not found; no SharePoint?)",
	"apis.paused":              "{count} APIs paused for 24 h (the account was refused: 403 or 404)",
	"apis.ok":                  "no category found nothing in its last {count} rounds",

	// the run itself
	"check.crashed": "the check crashed unexpectedly",
	"check.timeout": "did not finish within {dur}",

	// fixes
	"fix.configInit":        "e5extend config init",
	"fix.configValidate":    "e5extend config validate",
	"fix.accountAdd":        "e5extend account add <name> --auth device_code --tenant <tenant> --client-id <id>",
	"fix.accountSetSecret":  "e5extend account set-secret <name>",
	"fix.engineStart":       "e5extend service install   (or, in the foreground: e5extend run)",
	"fix.engineRestart":     "check the engine log; restart it if it is stuck",
	"fix.serviceInstall":    "e5extend service install",
	"fix.serviceStatus":     "e5extend service status",
	"fix.linger":            "loginctl enable-linger $USER",
	"fix.grantPermissions":  "grant more API permissions and admin consent (docs/user/app-registration.md); see: e5extend catalog list --account {account}",
	"fix.catalogList":       "see: e5extend catalog list --account {account}",
	"fix.runNow":            "e5extend run-now {account}",
	"fix.statusRunNow":      "e5extend status; then e5extend run-now {account}",
	"fix.resume":            "e5extend account resume {account}",
	"fix.resumeAfter":       "fix the cause above, then: e5extend account resume {account}",
	"fix.extendPeriod":      "extend schedule.period_end in the configuration to keep running",
	"fix.renew":             "renew it in the Microsoft 365 admin center (Billing > Your products), then run: e5extend subscription",
	"fix.checkSubscription": "check the subscription in the Microsoft 365 admin center (Billing > Your products)",
	"fix.subscription":      "e5extend subscription",
	"fix.newSecret":         "create a new secret in the app registration, then: e5extend account set-secret {account} --secret-expires YYYY-MM-DD",
	"fix.network":           "check the internet connection and the proxy (network.proxy in the config, or HTTPS_PROXY)",
	"fix.ntp":               "turn on automatic time (NTP) and check the time zone",
	"fix.notifyRestoreKey":  "restore the master key, or set it again: e5extend notify channel edit {id}",
	"fix.channelEdit":       "e5extend notify channel edit {id}",
	"fix.useHTTPS":          "use an https:// server address",
	"fix.setSecurity":       "set security to starttls or tls",
	"fix.clearAllowHTTP":    "set an https address (that clears allow_http), or run: e5extend notify channel edit {id} --allow-http=false",
	"fix.previewTest":       "e5extend notify preview test --type {type}",
	"fix.notifyTest":        "e5extend notify test {id}",
	"fix.setWebhook":        "set an https address: e5extend notify set-webhook <url>",
	"fix.setWebhookRestore": "restore the master key, or set the address again: e5extend notify set-webhook <url>",
	"fix.setWebhookMove":    "e5extend notify set-webhook <url>   (moves it into the vault and clears the config entry)",
	"fix.channelAdd":        "e5extend notify channel add <id> --type telegram|email|wecom|feishu|dingtalk|discord|slack|bark|ntfy|serverchan|webhook",
	"fix.recommend":         "e5extend account test {account} --recommend",
}

// HintCodes are the codes of the suggestions Deps.HintCode may return for a
// sign-in or vault error; their English wording is the caller's (Deps.Hint).
// Each takes the account name as the parameter "account".
var HintCodes = []string{
	"hint.publicClient", "hint.signInFrequency", "hint.passwordWrong", "hint.caBlocked", "hint.locked",
	"hint.passwordExpired", "hint.disabled", "hint.mfa", "hint.tokenCacheKey", "hint.loginRequired",
	"hint.invalidClient", "hint.consent", "hint.transient", "hint.remoteSession", "hint.noMasterKey", "hint.wrongKey",
}

// Codes lists every code a check can carry (detail, fix and hint), sorted.
func Codes() []string {
	out := make([]string, 0, len(english)+len(HintCodes))
	for k := range english {
		out = append(out, k)
	}
	out = append(out, HintCodes...)
	slices.Sort(out)
	return out
}

// ---- building results ----

// setDetail sets the detail from one message (Code, Params) or several, shown
// one after another (Parts). Detail always holds the English text.
func (c *Check) setDetail(ms ...Msg) {
	c.Code, c.Params, c.Parts = "", nil, nil
	c.Detail = joinEnglish(ms)
	switch len(ms) {
	case 0:
	case 1:
		c.Code, c.Params = ms[0].Code, ms[0].Params
	default:
		c.Parts = ms
	}
}

// setFix is setDetail for the fix.
func (c *Check) setFix(ms ...Msg) {
	c.FixCode, c.FixParams, c.FixParts = "", nil, nil
	c.Fix = joinEnglish(ms)
	switch len(ms) {
	case 0:
	case 1:
		c.FixCode, c.FixParams = ms[0].Code, ms[0].Params
	default:
		c.FixParts = ms
	}
}

// setHintFix sets the fix to the caller's suggestion: text is its English
// wording and code names it (empty when the caller cannot).
func (c *Check) setHintFix(text, code, account string) {
	c.Fix, c.FixParts = text, nil
	c.FixCode, c.FixParams = "", nil
	if text != "" && code != "" {
		c.FixCode, c.FixParams = code, map[string]string{"account": account}
	}
}

func joinEnglish(ms []Msg) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = m.English()
	}
	return strings.Join(parts, "; ")
}

func (d Deps) hintCode(err error) string {
	if d.HintCode == nil || err == nil {
		return ""
	}
	return d.HintCode(err)
}

// dedupMsgs drops repeated messages, keeping the first of each.
func dedupMsgs(in []Msg) []Msg {
	seen := map[string]bool{}
	var out []Msg
	for _, m := range in {
		if k := m.Code + "\x00" + fmt.Sprint(m.Params); !seen[k] {
			seen[k] = true
			out = append(out, m)
		}
	}
	return out
}
