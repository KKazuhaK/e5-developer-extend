package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/callstat"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/recommend"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
	"github.com/KKazuhaK/e5-developer-extend/internal/subscription"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
	"github.com/KKazuhaK/e5-developer-extend/internal/version"
)

// ---- individual checks ----

func firstLines(err error, n int) string {
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	if len(lines) > n {
		lines = append(lines[:n], fmt.Sprintf("(%d more)", len(lines)-n))
	}
	return strings.Join(lines, "; ")
}

func checkConfig(d Deps, cfg *config.Config, err error) Check {
	c := Check{ID: "config", Title: "Configuration"}
	switch {
	case errors.Is(err, config.ErrNotFound):
		c.Level = Fail
		c.setDetail(M("config.missing"))
		c.setFix(M("fix.configInit"))
	case err != nil:
		c.Level = Fail
		c.setDetail(M("config.invalid", "reason", firstLines(err, 5)))
		c.setFix(M("fix.configValidate"))
	case len(cfg.Accounts) == 0:
		c.Level = Warn
		c.setDetail(M("config.noAccounts"))
		c.setFix(M("fix.accountAdd"))
	default:
		c.Level = OK
		c.setDetail(M("config.ok", "count", itoa(len(cfg.Accounts))))
	}
	return c
}

func checkVault(d Deps, cfg *config.Config) Check {
	c := Check{ID: "vault", Title: "Vault"}
	v, err := d.OpenVault(cfg)
	if err != nil {
		c.Level = Fail
		c.setDetail(M("vault.openFailed", "reason", firstLines(err, 2)))
		c.setHintFix(d.hint(err, ""), d.hintCode(err), "")
		return c
	}
	var problems []Msg
	var firstErr error
	checked := 0
	for _, acct := range cfg.Accounts {
		keys := []string{vault.TokenCacheKey(acct.Name)}
		if acct.NeedsSecret() {
			keys = append([]string{vault.SecretKey(acct.Name)}, keys...)
		}
		for i, key := range keys {
			has, herr := v.Has(key)
			if herr != nil {
				problems = append(problems, M("vault.cannotLook", "account", acct.Name))
				firstErr = cmp(firstErr, herr)
				continue
			}
			if !has {
				if acct.NeedsSecret() && i == 0 {
					problems = append(problems, M("vault.secretMissing", "account", acct.Name))
					firstErr = cmp(firstErr, fmt.Errorf("secret missing: %w", vault.ErrNotFound))
				}
				continue
			}
			checked++
			// Only ask for the master key when there is something to decrypt.
			if _, gerr := v.Get(key); gerr != nil {
				switch {
				case errors.Is(gerr, vault.ErrWrongKey):
					problems = append(problems, M("vault.wrongKey", "account", acct.Name))
				case errors.Is(gerr, vault.ErrNoMasterKey):
					problems = append(problems, M("vault.noMasterKey", "account", acct.Name))
				default:
					problems = append(problems, M("vault.other", "account", acct.Name, "reason", firstLines(gerr, 1)))
				}
				firstErr = cmp(firstErr, gerr)
			}
		}
	}
	if len(problems) > 0 {
		c.Level = Fail
		c.setDetail(dedupMsgs(problems)...)
		if errors.Is(firstErr, vault.ErrNotFound) {
			c.setFix(M("fix.accountSetSecret"))
		} else {
			first := firstAccount(cfg)
			c.setHintFix(d.hint(firstErr, first), d.hintCode(firstErr), first)
		}
		return c
	}
	c.Level = OK
	if checked == 0 {
		c.setDetail(M("vault.empty"))
	} else {
		c.setDetail(M("vault.ok", "count", itoa(checked)))
	}
	return c
}

func cmp(cur, next error) error {
	if cur != nil {
		return cur
	}
	return next
}

func firstAccount(cfg *config.Config) string {
	if len(cfg.Accounts) > 0 {
		return cfg.Accounts[0].Name
	}
	return ""
}

func checkEngine(d Deps) Check {
	c := Check{ID: "engine", Title: "Engine"}
	running := engine.Running(d.DataDir)
	hb, hbErr := engine.ReadHeartbeat(d.DataDir)
	switch {
	case !running:
		c.Level = Fail
		if hbErr == nil {
			c.setDetail(M("engine.stoppedSeen", "at", hb.Local().Format("2006-01-02 15:04")))
		} else {
			c.setDetail(M("engine.stopped"))
		}
		c.setFix(M("fix.engineStart"))
	case hbErr != nil || d.now().Sub(hb) > HeartbeatMaxAge:
		c.Level = Warn
		c.setDetail(M("engine.stale"))
		c.setFix(M("fix.engineRestart"))
	default:
		c.Level = OK
		c.setDetail(M("engine.running", "seconds", secs(d.now().Sub(hb).Round(time.Second))))
	}
	return c
}

func checkService(d Deps, cfg *config.Config) []Check {
	c := Check{ID: "service", Title: "Background service"}
	if d.Service == nil {
		c.Level = Warn
		c.setDetail(M("service.unsupported"))
		return []Check{c}
	}
	st, err := d.Service()
	var out []Check
	switch {
	case err != nil:
		c.Level = Warn
		c.setDetail(M("service.error", "reason", firstLines(err, 2)))
	case !st.Installed:
		c.Level = Warn
		c.setDetail(M("service.notInstalled"))
		c.setFix(M("fix.serviceInstall"))
	case st.Enabled == service.No:
		c.Level = Warn
		c.setDetail(M("service.disabled"))
		c.setFix(M("fix.serviceInstall"))
	case st.Running == service.No:
		c.Level = Warn
		c.setDetail(M("service.notRunning"))
		c.setFix(M("fix.serviceStatus"))
	default:
		c.Level = OK
		if st.Running == service.Yes {
			c.setDetail(M("service.okRunning"))
		} else {
			c.setDetail(M("service.ok"))
		}
	}
	out = append(out, c)
	if d.GOOS == service.Linux && err == nil && st.Installed {
		switch st.Linger {
		case service.No:
			l := Check{ID: "linger", Title: "Linger", Level: Warn}
			l.setDetail(M("linger.off"))
			l.setFix(M("fix.linger"))
			out = append(out, l)
		case service.Yes:
			l := Check{ID: "linger", Title: "Linger", Level: OK}
			l.setDetail(M("linger.on"))
			out = append(out, l)
		}
	}
	if d.GOOS == service.Windows && err == nil && st.Installed && st.Headless == service.Yes {
		out = append(out, checkHeadless(d, cfg, st))
	}
	return out
}

// checkHeadless looks at the key file a headless Windows task depends on: the
// task runs without a user profile, so without that file (or with a stale
// copy of the key) the engine cannot open the vault. Problems add up: the
// level is the worst of them.
func checkHeadless(d Deps, cfg *config.Config, st service.Status) Check {
	h := Check{ID: "headless", Title: "Headless mode", Level: OK}
	var msgs []Msg
	worse := func(l Level) {
		if l == Fail || (l == Warn && h.Level == OK) {
			h.Level = l
		}
	}
	switch {
	case st.KeyFileMissing:
		worse(Fail)
		msgs = append(msgs, M("headless.keyMissing"))
	case st.KeyFileErr != "":
		worse(Fail)
		msgs = append(msgs, M("headless.keyUnreadable", "reason", st.KeyFileErr))
	default:
		if m, ok := staleKey(d, cfg, st); ok {
			worse(Fail)
			msgs = append(msgs, m)
		}
	}
	if len(st.KeyFileBroad) > 0 {
		worse(Warn)
		msgs = append(msgs, M("headless.keyAcl", "principals", strings.Join(st.KeyFileBroad, ", ")))
	}
	if len(st.KeyFileDirBroad) > 0 {
		worse(Warn)
		msgs = append(msgs, M("headless.dirAcl", "dir", st.KeyFileDir, "principals", strings.Join(st.KeyFileDirBroad, ", ")))
	}
	if st.KeyFileACLErr != "" {
		worse(Warn)
		msgs = append(msgs, M("headless.aclUnreadable", "reason", st.KeyFileACLErr))
	}
	if len(msgs) == 0 {
		msgs = append(msgs, M("headless.ok"))
	} else {
		h.setFix(M("fix.headlessInstall"))
	}
	h.setDetail(msgs...)
	return h
}

// staleKey decrypts one stored item with the key from the key file, as the
// vault check does with the normal key. A key that cannot decrypt what the
// vault holds is a stale copy (the master key changed after the install).
func staleKey(d Deps, cfg *config.Config, st service.Status) (Msg, bool) {
	if d.OpenVaultWithKeyFile == nil || cfg == nil || st.KeyFile == "" {
		return Msg{}, false
	}
	v, err := d.OpenVaultWithKeyFile(cfg, st.KeyFile)
	if err != nil {
		return M("headless.keyUnreadable", "reason", firstLines(err, 1)), true
	}
	for _, acct := range cfg.Accounts {
		keys := []string{vault.TokenCacheKey(acct.Name)}
		if acct.NeedsSecret() {
			keys = append([]string{vault.SecretKey(acct.Name)}, keys...)
		}
		for _, key := range keys {
			if has, err := v.Has(key); err != nil || !has {
				continue
			}
			if _, err := v.Get(key); err != nil {
				if errors.Is(err, vault.ErrWrongKey) {
					return M("headless.staleKey", "account", acct.Name), true
				}
				return M("headless.keyUnreadable", "reason", firstLines(err, 1)), true
			}
			return Msg{}, false // one item decrypted: the key is right
		}
	}
	return Msg{}, false
}

func checkAccount(ctx context.Context, d Deps, cfg *config.Config, acct config.Account, as *store.AccountState, offline bool) []Check {
	id := "account." + acct.Name
	title := "Account " + acct.Name
	var out []Check

	// Sign-in and scopes.
	var scopes []string
	haveScopes, recorded := false, false
	if !offline {
		tc, tok, ok := checkToken(ctx, d, cfg, acct, id, title)
		out = append(out, tc)
		if ok {
			scopes, haveScopes = tok.Scopes, true
		}
	}
	if !haveScopes && as != nil && len(as.GrantedScopes) > 0 {
		scopes, haveScopes, recorded = as.GrantedScopes, true, true
	}
	if haveScopes {
		out = append(out, checkScopes(acct, id, title, scopes, recorded))
	}

	out = append(out, checkActivity(d, acct, as, id, title), checkState(d, acct, as, id, title), checkAPIs(d, acct, as, id, title))
	if acct.NeedsSecret() && acct.SecretExpires != "" {
		out = append(out, checkSecret(d, acct, id, title))
	}
	return out
}

func checkToken(ctx context.Context, d Deps, cfg *config.Config, acct config.Account, id, title string) (Check, auth.Token, bool) {
	c := Check{ID: id + ".token", Title: title + " sign-in"}
	au, err := d.NewAuth(cfg, acct)
	if err != nil {
		c.Level = Fail
		c.setDetail(tokenMsg(err))
		c.setHintFix(d.hint(err, acct.Name), d.hintCode(err), acct.Name)
		return c, auth.Token{}, false
	}
	tok, err := au.Token(ctx)
	if err != nil {
		c.Level = Fail
		c.setDetail(tokenMsg(err))
		c.setHintFix(d.hint(err, acct.Name), d.hintCode(err), acct.Name)
		if errors.Is(err, auth.ErrTransient) {
			c.Level = Warn
		}
		return c, auth.Token{}, false
	}
	c.Level = OK
	if tok.ExpiresOn.IsZero() {
		c.setDetail(M("token.okNoExpiry"))
	} else {
		c.setDetail(M("token.ok", "seconds", secs(time.Until(tok.ExpiresOn).Round(time.Minute))))
	}
	return c, tok, true
}

// tokenMsg names a sign-in failure in fixed words; provider text can hold
// tenant names and user names, so it is never shown.
func tokenMsg(err error) Msg {
	switch {
	case auth.IsSignInFrequency(err):
		return M("token.signInFrequency")
	case errors.Is(err, auth.ErrLoginRequired):
		return M("token.loginRequired")
	case errors.Is(err, auth.ErrInvalidClient):
		return M("token.invalidClient")
	case errors.Is(err, auth.ErrConsentRequired):
		return M("token.consent")
	case errors.Is(err, auth.ErrTransient):
		return M("token.transient")
	case errors.Is(err, vault.ErrNoMasterKey):
		return M("token.noMasterKey")
	case errors.Is(err, vault.ErrWrongKey):
		return M("token.wrongKey")
	}
	code := auth.ErrorCode(err)
	if code == "" {
		code = "unknown error"
	}
	return M("token.failed", "code", code)
}

func checkScopes(acct config.Account, id, title string, scopes []string, recorded bool) Check {
	c := Check{ID: id + ".scopes", Title: title + " permissions"}
	apis, err := catalog.Load()
	if err != nil {
		c.Level = Fail
		c.setDetail(M("scopes.catalogBroken", "reason", err.Error()))
		return c
	}
	kind := catalog.Delegated
	if !acct.Delegated() {
		kind = catalog.App
	}
	n := len(catalog.Available(apis, kind, scopes, acct.Categories))
	pick := func(plain, rec string) string {
		if recorded {
			return rec
		}
		return plain
	}
	switch {
	case n == 0:
		c.Level = Fail
		c.setDetail(M(pick("scopes.none", "scopes.noneRecorded")))
		c.setFix(M("fix.grantPermissions", "account", acct.Name))
	case n < fewAPIs:
		c.Level = Warn
		c.setDetail(M(pick("scopes.few", "scopes.fewRecorded"), "count", itoa(n)))
		c.setFix(M("fix.catalogList", "account", acct.Name))
	default:
		c.Level = OK
		c.setDetail(M(pick("scopes.ok", "scopes.okRecorded"), "count", itoa(n)))
	}
	return c
}

func checkActivity(d Deps, acct config.Account, as *store.AccountState, id, title string) Check {
	c := Check{ID: id + ".activity", Title: title + " activity"}
	if acct.Paused {
		c.Level = OK
		c.setDetail(M("activity.paused"))
		return c
	}
	if as == nil || as.LastSuccess.IsZero() {
		c.Level = Warn
		c.setDetail(M("activity.none"))
		c.setFix(M("fix.runNow", "account", acct.Name))
		return c
	}
	age := d.now().Sub(as.LastSuccess)
	c.setDetail(M("activity.last", "seconds", secs(age)))
	switch {
	case age > ActivityFail:
		c.Level = Fail
		c.setFix(M("fix.statusRunNow", "account", acct.Name))
	case age > ActivityWarn:
		c.Level = Warn
		c.setFix(M("fix.statusRunNow", "account", acct.Name))
	default:
		c.Level = OK
	}
	return c
}

func humanAge(d time.Duration) string {
	if d < 48*time.Hour {
		return d.Round(time.Minute).String()
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

func checkState(d Deps, acct config.Account, as *store.AccountState, id, title string) Check {
	c := Check{ID: id + ".state", Title: title + " state"}
	status := store.StatusOK
	if as != nil {
		status = as.Status
	}
	reason := ""
	if as != nil {
		reason = as.Reason
	}
	switch {
	case acct.Paused:
		c.Level = Warn
		c.setDetail(M("state.paused"))
		c.setFix(M("fix.resume", "account", acct.Name))
	case status == store.StatusNeedsAttention:
		c.Level = Fail
		c.setDetail(M("state.needsAttention", "reason", reason))
		c.setFix(M("fix.resumeAfter", "account", acct.Name))
	case status == store.StatusPaused:
		c.Level = Fail
		if as != nil && !as.NextProbe.IsZero() {
			c.setDetail(M("state.autoPaused", "reason", reason), M("state.autoResume", "seconds", secs(max(as.NextProbe.Sub(d.now()), 0))))
		} else {
			c.setDetail(M("state.autoPaused", "reason", reason))
		}
		c.setFix(M("fix.resume", "account", acct.Name))
	case status == store.StatusPeriodEnded:
		c.Level = Warn
		c.setDetail(M("state.periodEnded"))
		c.setFix(M("fix.extendPeriod"))
	default:
		c.Level = OK
		c.setDetail(M("state.ok"))
	}
	return c
}

// subscriptionWarnDays is how close an end date must be for the doctor to warn.
const subscriptionWarnDays = 7

// checkSubscription judges what the last subscription check saw. A missing
// permission is a state to explain, not a problem: the check is optional.
func checkSubscription(d Deps, cfg *config.Config, tv subscription.TenantView) Check {
	c := Check{ID: "subscription." + tv.Label, Title: "Subscription " + tv.Label}
	var parts []Msg
	var note *Msg // the failure of the last check, when older data is still shown
	manual := func() {
		days := tv.ManualDaysLeft
		if days == nil {
			return
		}
		parts = append(parts, M("sub.manual", "date", tv.ManualExpires, "offset", itoa(*days)))
		if *days <= subscriptionWarnDays {
			c.Level = Warn
			c.setFix(M("fix.renew"))
		}
	}
	done := func() Check {
		c.setDetail(parts...)
		return c
	}
	switch tv.Access {
	case subscription.AccessUnchecked:
		c.Level = OK
		parts = []Msg{M("sub.unchecked")}
		manual()
		return done()
	case store.AccessNoPermission:
		c.Level = OK
		parts = []Msg{M("sub.noPermission", "account", tv.FixAccount, "command", tv.FixCommand)}
		if tv.ManualExpires != "" {
			parts = []Msg{M("sub.noPermissionManual")}
			manual()
		}
		return done()
	case store.AccessError:
		c.Level = OK
		n := M("sub.lastFailed", "detail", tv.Detail)
		note = &n
		parts = []Msg{n}
		if tv.CheckedAt == nil {
			c.Level = Warn
			c.setFix(M("fix.subscription"))
		}
		if len(tv.Subscriptions) == 0 {
			manual()
			return done()
		}
	}

	var skus []Msg
	watched := 0
	for _, s := range tv.Subscriptions {
		if !s.Watched {
			continue
		}
		watched++
		if s.DaysLeft != nil {
			skus = append(skus, M("sub.skuEnds", "sku", s.SKU, "status", s.Status, "date", s.Expires, "offset", itoa(*s.DaysLeft)))
		} else {
			skus = append(skus, M("sub.sku", "sku", s.SKU, "status", s.Status))
		}
		switch {
		case s.Status != notify.StatusEnabled:
			c.Level = Warn
			c.setFix(M("fix.checkSubscription"))
		case s.DaysLeft != nil && *s.DaysLeft <= subscriptionWarnDays:
			c.Level = Warn
			c.setFix(M("fix.renew"))
		}
	}
	if c.Level == "" {
		c.Level = OK
	}
	if watched == 0 {
		if len(tv.Subscriptions) > 0 {
			c.setDetail(M("sub.noneWatched", "count", itoa(len(tv.Subscriptions))))
		} else {
			c.setDetail(M("sub.noneFound", "skus", strings.Join(cfg.Subscription.SKUPrefixes(), ", ")))
		}
		return c
	}
	parts = skus
	if note != nil {
		parts = append(parts, *note)
	}
	if tv.ManualRedundant {
		parts = append(parts, M("sub.manualRedundant"))
	}
	return done()
}

func daysPhrase(d int) string {
	switch {
	case d < 0:
		return fmt.Sprintf("%d day(s) ago", -d)
	case d == 0:
		return "today"
	case d == 1:
		return "tomorrow"
	}
	return fmt.Sprintf("in %d days", d)
}

func checkSecret(d Deps, acct config.Account, id, title string) Check {
	c := Check{ID: id + ".secret", Title: title + " client secret"}
	days, ok := notify.DaysLeft(d.now(), acct.SecretExpires)
	if !ok {
		c.Level = Warn
		c.setDetail(M("secret.badDate"))
		return c
	}
	c.setFix(M("fix.newSecret", "account", acct.Name))
	switch {
	case days < 0:
		c.Level = Fail
		c.setDetail(M("secret.expired", "date", acct.SecretExpires))
	case days == 0:
		c.Level = Fail
		c.setDetail(M("secret.today"))
	case days <= 7:
		c.Level = Fail
		c.setDetail(M("secret.soon", "count", itoa(days), "date", acct.SecretExpires))
	case days <= 30:
		c.Level = Warn
		c.setDetail(M("secret.warn", "count", itoa(days), "date", acct.SecretExpires))
	default:
		c.Level = OK
		c.setDetail(M("secret.ok", "count", itoa(days)))
		c.setFix()
	}
	return c
}

// checkNetwork makes one request to Graph and derives both the network and
// the clock check from it.
func checkNetwork(ctx context.Context, d Deps, cfg *config.Config) []Check {
	nc := Check{ID: "network", Title: "Network"}
	cc := Check{ID: "clock", Title: "Clock"}
	base := d.GraphBase
	if base == "" {
		base = graph.DefaultBase
	}
	var hc *http.Client
	if d.HTTP != nil {
		hc = d.HTTP(cfg)
	} else {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/", nil)
	if err != nil {
		nc.Level = Fail
		nc.setDetail(M("net.badAddress", "reason", err.Error()))
		return []Check{cc.skipped(), nc}
	}
	req.Header.Set("User-Agent", "e5extend/"+version.Version)
	cp := *hc
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := cp.Do(req)
	received := d.now()
	if err != nil {
		var ue *url.Error
		reason := err.Error()
		if errors.As(err, &ue) {
			reason = ue.Err.Error()
		}
		nc.Level = Fail
		nc.setDetail(M("net.unreachable", "reason", reason))
		nc.setFix(M("fix.network"))
		cc.Level = Warn
		cc.setDetail(M("clock.unreachable"))
		return []Check{cc, nc}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	nc.Level = OK
	nc.setDetail(M("net.ok", "status", itoa(resp.StatusCode)))
	if resp.StatusCode >= 500 {
		nc.Level = Warn
		nc.setDetail(M("net.trouble", "status", itoa(resp.StatusCode)))
	}

	remote, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		cc.Level = Warn
		cc.setDetail(M("clock.noDate"))
		return []Check{cc, nc}
	}
	diff := received.Sub(remote)
	abs := diff
	if abs < 0 {
		abs = -abs
	}
	which := "clock.behind"
	if diff >= 0 {
		which = "clock.ahead"
	}
	cc.setDetail(M(which, "seconds", secs(abs.Round(time.Second))))
	switch {
	case abs > ClockFail:
		cc.Level = Fail
		cc.setFix(M("fix.ntp"))
	case abs > ClockWarn:
		cc.Level = Warn
		cc.setFix(M("fix.ntp"))
	default:
		cc.Level = OK
		cc.setDetail(M("clock.ok", "seconds", secs(ClockWarn)))
	}
	return []Check{cc, nc}
}

// skipped is a placeholder used only when the clock cannot be checked at all.
func (c Check) skipped() Check {
	c.Level = Warn
	c.setDetail(M("clock.skipped"))
	return c
}

func checkNotify(d Deps, cfg *config.Config) Check {
	c := Check{ID: "notify", Title: "Alerts"}
	v, verr := d.OpenVault(cfg)
	has := func(key string) bool {
		if verr != nil {
			return false
		}
		ok, _ := v.Has(key)
		return ok
	}
	// A secret is only tried for decryption: its value is never shown.
	unreadable := func(key string) bool {
		if verr != nil || !has(key) {
			return false
		}
		_, err := v.Get(key)
		return err != nil
	}

	var problems, fixes []Msg
	add := func(problem Msg, fix *Msg) {
		problems = append(problems, problem)
		if fix != nil {
			fixes = append(fixes, *fix)
		}
	}
	fixOf := func(code string, kv ...string) *Msg { m := M(code, kv...); return &m }
	channels := len(cfg.Notify.Channels)
	for _, ch := range cfg.Notify.Channels {
		who := []string{"type", ch.Type, "id", ch.ID}
		with := func(extra ...string) []string { return append(slices.Clone(who), extra...) }
		legacyURL := ch.ID == notify.LegacyChannelID && ch.Type == config.ChannelWebhook && has(notify.VaultKey)
		for _, f := range config.ChannelSecrets(ch.Type) {
			key := notify.ChannelSecretKey(ch.ID, f.Name)
			switch {
			case unreadable(key):
				add(M("notify.undecryptable", with("field", f.Name)...), fixOf("fix.notifyRestoreKey", "id", ch.ID))
			case f.Required && !has(key) && !(f.Name == "url" && legacyURL):
				add(M("notify.fieldMissing", with("field", f.Name)...), fixOf("fix.channelEdit", "id", ch.ID))
			}
		}
		if ch.Server != "" && notify.IsPlainHTTP(ch.Server) || ch.APIBase != "" && notify.IsPlainHTTP(ch.APIBase) {
			add(M("notify.plainHTTP", who...), fixOf("fix.useHTTPS"))
		}
		if ch.Type == config.ChannelEmail && ch.SecurityOrDefault() == config.SecurityNone {
			add(M("notify.mailNoEncrypt", who...), fixOf("fix.setSecurity"))
		}
		if ch.AllowHTTP {
			add(M("notify.allowHTTP", who...), fixOf("fix.clearAllowHTTP", "id", ch.ID))
		}
		if ch.AllowPlainAuth {
			add(M("notify.allowPlainAuth", who...), fixOf("fix.setSecurity"))
		}
		if ch.Template != "" {
			if err := notify.CheckTemplate(ch.Type, ch.Template, ch.Type == config.ChannelWebhook && ch.Format == config.FormatTemplate); err != nil {
				add(M("notify.badTemplate", with("reason", err.Error())...), fixOf("fix.previewTest", "type", ch.Type))
			}
		}
	}
	// How the deliveries went (recorded by whoever sent them; no secrets in it).
	status := notify.ReadStatus(d.DataDir)
	ids := []string{}
	for _, ch := range cfg.Notify.Channels {
		ids = append(ids, ch.ID)
	}
	if _, ok := cfg.Notify.FindChannel(notify.LegacyChannelID); !ok {
		ids = append(ids, notify.LegacyChannelID)
	}
	for _, id := range ids {
		st, ok := status[id]
		if !ok || st.OK {
			continue
		}
		stale := st.LastOK.IsZero() || d.now().Sub(st.LastOK) > DeliveryStale
		switch {
		case st.Failures >= DeliveryFailures:
			add(M("notify.deliveriesFailed", "id", id, "count", itoa(st.Failures), "reason", st.Error), fixOf("fix.notifyTest", "id", id))
		case stale:
			add(M("notify.stale", "id", id, "days", itoa(int(DeliveryStale.Hours()/24)), "reason", st.Error), fixOf("fix.notifyTest", "id", id))
		}
	}
	// The webhook of older versions, when it has not been saved as a channel yet.
	if _, ok := cfg.Notify.FindChannel(notify.LegacyChannelID); !ok {
		// Older versions accepted http addresses; they still work, but the token travels unencrypted.
		insecure := func(raw string) bool {
			u, err := url.Parse(strings.TrimSpace(raw))
			return err == nil && u.Scheme == "http" && !notify.IsLoopback(u.Hostname())
		}
		if has(notify.VaultKey) && !unreadable(notify.VaultKey) {
			if b, err := v.Get(notify.VaultKey); err == nil && insecure(string(b)) {
				add(M("notify.legacyHTTPStored"), fixOf("fix.setWebhook"))
			}
		}
		if insecure(cfg.Notify.WebhookURL) {
			add(M("notify.legacyHTTP"), fixOf("fix.setWebhook"))
		}
		switch {
		case unreadable(notify.VaultKey):
			channels++
			add(M("notify.legacyUndecryptable"), fixOf("fix.setWebhookRestore"))
		case cfg.Notify.WebhookURL != "":
			channels++
			add(M("notify.legacyPlain"), fixOf("fix.setWebhookMove"))
		case has(notify.VaultKey):
			channels++
		}
	}
	switch {
	case channels == 0:
		c.Level = Warn
		c.setDetail(M("notify.none"))
		c.setFix(M("fix.channelAdd"))
	case len(problems) > 0:
		c.Level = Warn
		c.setDetail(problems...)
		c.setFix(dedupMsgs(fixes)...)
	default:
		c.Level = OK
		if channels == 1 {
			c.setDetail(M("notify.one"))
		} else {
			c.setDetail(M("notify.many", "count", itoa(channels)))
		}
	}
	return c
}

// checkAPIs warns about a category whose calls found nothing in each of its
// last FailingRounds rounds, which usually means the account lacks the service
// (no mailbox, no OneDrive). It only reads the local call log.
func checkAPIs(d Deps, acct config.Account, as *store.AccountState, id, title string) Check {
	c := Check{ID: id + ".apis", Title: title + " APIs"}
	apis, err := catalog.Load()
	if err != nil {
		c.Level = Warn
		c.setDetail(M("apis.catalogBroken"))
		return c
	}
	category := callstat.CategoryIndex(apis)
	entries, err := store.New(d.DataDir).Read(store.Filter{Account: acct.Name, Since: d.now().Add(-failingWindow)})
	if err != nil {
		c.Level = OK
		c.setDetail(M("apis.logUnreadable"))
		return c
	}
	// Per category, the rounds it was called in (oldest first) and whether
	// every call of that round found nothing.
	type round struct {
		id       string
		allFound bool
	}
	rounds := map[string][]round{}
	var order []string
	// A brand-new mailbox or OneDrive answers 403/404 until the person has
	// signed in to office.com once: the account never got a mail or files
	// call through, but was refused some.
	// Mail and OneDrive are judged separately.
	cameThrough, refusedSince := map[string]bool{}, map[string]time.Time{}
	for _, e := range entries {
		cat, known := category[e.API]
		if !callstat.IsCall(e) || !known {
			continue
		}
		if cat == "mail" || cat == "files" {
			if callstat.IsUnavailable(e.Class) {
				if t, ok := refusedSince[cat]; !ok || e.TS.Before(t) {
					refusedSince[cat] = e.TS
				}
			} else if e.Status >= 200 && e.Status < 300 {
				cameThrough[cat] = true
			}
		}
		missing := callstat.IsMissing(e.Class, e.Status)
		rs := rounds[cat]
		if len(rs) == 0 {
			order = append(order, cat)
		}
		if n := len(rs); n > 0 && rs[n-1].id == e.Round {
			rs[n-1].allFound = rs[n-1].allFound && missing
		} else {
			rs = append(rs, round{e.Round, missing})
		}
		rounds[cat] = rs
	}
	// Only categories the account currently runs count: one switched off
	// (for example by --apply) has old failures in the log but no problem.
	enabled := callstat.EnabledCategories(apis, acct.Categories)
	// APIs the account was refused (403/404) are left out for 24 hours.
	var paused []Msg
	pausedN, pausedMailFiles := 0, 0
	if as != nil {
		for id, until := range as.APISkipUntil {
			if until.After(d.now()) && (category[id] == "mail" || category[id] == "files") {
				pausedMailFiles++
			}
		}
		if n := callstat.PausedAPIs(as.APISkipUntil, d.now()); n > 0 {
			pausedN = n
			paused = append(paused, M("apis.paused", "count", itoa(n)))
		}
	}
	var parts []Msg
	// Hint at the first sign-in when the account keeps being refused and has
	// never had a mail or OneDrive call succeed.
	firstSignIn, persistent := false, false
	for _, cat := range []string{"mail", "files"} {
		since, refused := refusedSince[cat]
		if refused && !cameThrough[cat] && enabled[cat] {
			firstSignIn = true
			// Refused for over a week without one success: probably intentional.
			persistent = persistent || d.now().Sub(since) > persistentRefusal
		}
	}
	for _, cat := range order {
		if !enabled[strings.ToLower(cat)] {
			continue
		}
		rs := rounds[cat]
		if len(rs) < FailingRounds {
			continue
		}
		failing := true
		for _, r := range rs[len(rs)-FailingRounds:] {
			failing = failing && r.allFound
		}
		if failing {
			parts = append(parts, M(missingCode(cat), "category", cat))
		}
	}
	manyRefused := false
	if firstSignIn && len(parts) == 0 && pausedMailFiles >= manyPausedAPIs && pausedMailFiles*2 > pausedN {
		parts = append(parts, M("apis.manyRefused"))
		manyRefused = true
	}
	if len(parts) == 0 {
		c.Level = OK
		c.setDetail(append([]Msg{M("apis.ok", "count", itoa(FailingRounds))}, paused...)...)
		return c
	}
	if firstSignIn {
		code := "apis.firstSignIn"
		if !acct.Delegated() {
			code = "apis.firstSignInApp" // no one signs in for an app identity
		}
		paused = append(paused, M(code))
	}
	c.Level = Warn
	if manyRefused && persistent {
		c.Level = OK // an information line: likely no mailbox on purpose
	}
	c.setDetail(append(parts, paused...)...)
	c.setFix(M("fix.recommend", "account", acct.Name))
	return c
}

// missingCode picks the message for a failing category, guessing why it finds
// nothing from the probe table.
func missingCode(category string) string {
	for _, p := range recommend.Probes() {
		for _, c := range p.Categories {
			if c != category {
				continue
			}
			switch p.Reason {
			case recommend.ReasonNoMailbox:
				return "apis.failingNoMailbox"
			case recommend.ReasonNoOneDrive:
				return "apis.failingNoOneDrive"
			case recommend.ReasonNoTeams:
				return "apis.failingNoTeams"
			case recommend.ReasonNoSharePoint:
				return "apis.failingNoSharePoint"
			}
		}
	}
	return "apis.failing"
}

// checkUpdate is an information line: a newer release exists. It is never a
// warning, because being a version behind is not a fault of the setup.
func checkUpdate(n UpdateNote) Check {
	c := Check{ID: "update", Title: "Update", Level: OK}
	c.setDetail(M("update.available", "current", n.Current, "latest", n.Latest, "url", n.URL))
	c.setFix(M("fix.update"))
	return c
}
