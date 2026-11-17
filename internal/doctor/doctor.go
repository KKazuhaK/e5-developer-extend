// Package doctor runs the health checks behind `e5extend doctor`. Every check
// returns a level, a short detail and, where one exists, the command that
// fixes the problem. Details never contain tokens, secrets, user names or
// webhook addresses.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
	"github.com/KKazuhaK/e5-developer-extend/internal/notify"
	"github.com/KKazuhaK/e5-developer-extend/internal/recommend"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
	"github.com/KKazuhaK/e5-developer-extend/internal/vault"
	"github.com/KKazuhaK/e5-developer-extend/internal/version"
)

// Level is the severity of a check result.
type Level string

// Levels.
const (
	OK   Level = "ok"
	Warn Level = "warn"
	Fail Level = "fail"
)

// Thresholds.
const (
	HeartbeatMaxAge = 2 * time.Minute
	ActivityWarn    = 72 * time.Hour
	ActivityFail    = 7 * 24 * time.Hour
	ClockWarn       = 2 * time.Minute
	ClockFail       = 5 * time.Minute
	// Timeout bounds the whole run.
	Timeout = 30 * time.Second

	fewAPIs = 5

	// FailingRounds is how many rounds in a row a category must have failed
	// with "not found" before the doctor warns about it.
	FailingRounds = 3
	// failingWindow is how far back the call log is read for that check.
	failingWindow = 14 * 24 * time.Hour
)

// Check is one result.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Level  Level  `json:"level"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// Vault is the part of the vault the checks use.
type Vault interface {
	Has(key string) (bool, error)
	Get(key string) ([]byte, error)
}

// Deps are the outside things the checks look at.
type Deps struct {
	Now       func() time.Time // time.Now when nil
	GOOS      string
	DataDir   string
	GraphBase string // graph.DefaultBase when empty

	// LoadConfig returns the configuration or the reason it cannot be used.
	LoadConfig func() (*config.Config, error)
	OpenVault  func(cfg *config.Config) (Vault, error)
	NewAuth    func(cfg *config.Config, acct config.Account) (auth.Authenticator, error)
	// HTTP returns the client for the network probe (it honors the proxy setting).
	HTTP func(cfg *config.Config) *http.Client
	// Service asks the platform about the background job; nil when unsupported.
	Service func() (service.Status, error)
	// Hint suggests a next step for a sign-in or vault error, or "".
	Hint func(err error, account string) string
}

// Options choose which checks run.
type Options struct {
	// Offline skips the checks that need the network: sign-in, clock, network.
	Offline bool
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) hint(err error, name string) string {
	if d.Hint == nil {
		return ""
	}
	return d.Hint(err, name)
}

// group is a set of checks produced together; onTimeout stands in for them
// when the run's deadline passes first.
type group struct {
	id, title string
	run       func(ctx context.Context) []Check
}

// Run executes all checks, in parallel, within Timeout, and returns them in a
// fixed order.
func Run(ctx context.Context, d Deps, o Options) []Check {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	cfg, cfgErr := d.LoadConfig()
	groups := []group{{"config", "Configuration", func(context.Context) []Check { return []Check{checkConfig(d, cfg, cfgErr)} }}}
	if cfg != nil && cfgErr == nil {
		groups = append(groups, group{"vault", "Vault", func(context.Context) []Check { return []Check{checkVault(d, cfg)} }})
	}
	groups = append(groups,
		group{"engine", "Engine", func(context.Context) []Check { return []Check{checkEngine(d)} }},
		group{"service", "Background service", func(context.Context) []Check { return checkService(d) }},
	)
	if cfg != nil && cfgErr == nil {
		st, _ := store.New(d.DataDir).LoadState()
		if st == nil {
			st = store.NewState()
		}
		for _, acct := range cfg.Accounts {
			groups = append(groups, group{"account." + acct.Name, "Account " + acct.Name,
				func(ctx context.Context) []Check {
					return checkAccount(ctx, d, cfg, acct, st.Accounts[acct.Name], o.Offline)
				}})
		}
	}
	if !o.Offline {
		groups = append(groups, group{"network", "Network", func(ctx context.Context) []Check { return checkNetwork(ctx, d, cfg) }})
	}
	if cfg != nil && cfgErr == nil {
		groups = append(groups, group{"notify", "Alerts", func(context.Context) []Check { return []Check{checkNotify(d, cfg)} }})
	}

	results := make([][]Check, len(groups))
	var wg sync.WaitGroup
	for i, g := range groups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch := make(chan []Check, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						ch <- []Check{{ID: g.id, Title: g.title, Level: Fail, Detail: "the check crashed unexpectedly"}}
					}
				}()
				ch <- g.run(ctx)
			}()
			select {
			case results[i] = <-ch:
			case <-ctx.Done():
				results[i] = []Check{{ID: g.id, Title: g.title, Level: Fail, Detail: fmt.Sprintf("did not finish within %s", Timeout)}}
			}
		}()
	}
	wg.Wait()
	var out []Check
	for _, r := range results {
		out = append(out, r...)
	}
	return out
}

// ExitCode is 0 when everything is ok, 1 with warnings and 2 with failures.
func ExitCode(checks []Check) int {
	code := 0
	for _, c := range checks {
		switch c.Level {
		case Fail:
			return 2
		case Warn:
			code = 1
		}
	}
	return code
}

// WriteJSON prints the checks as a JSON array.
func WriteJSON(w io.Writer, checks []Check) error {
	if checks == nil {
		checks = []Check{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(checks)
}

// WriteText prints one line per check, the fix on an indented line, and a
// summary. ascii replaces the check marks for consoles that cannot show them.
func WriteText(w io.Writer, checks []Check, ascii, offline bool) {
	marks := map[Level]string{OK: "✓", Warn: "!", Fail: "✗"}
	if ascii {
		marks = map[Level]string{OK: "ok", Warn: "!!", Fail: "xx"}
	}
	var nOK, nWarn, nFail int
	for _, c := range checks {
		line := fmt.Sprintf("%s %s", marks[c.Level], c.Title)
		if c.Detail != "" {
			line += ": " + c.Detail
		}
		fmt.Fprintln(w, line)
		if c.Fix != "" && c.Level != OK {
			fmt.Fprintf(w, "    fix: %s\n", c.Fix)
		}
		switch c.Level {
		case OK:
			nOK++
		case Warn:
			nWarn++
		default:
			nFail++
		}
	}
	if offline {
		fmt.Fprintln(w, "(offline: sign-in, clock and network checks were skipped)")
	}
	fmt.Fprintf(w, "%d ok, %d warnings, %d failures\n", nOK, nWarn, nFail)
}

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
		c.Level, c.Detail, c.Fix = Fail, "no configuration file yet", "e5extend config init"
	case err != nil:
		c.Level, c.Detail, c.Fix = Fail, firstLines(err, 5), "e5extend config validate"
	case len(cfg.Accounts) == 0:
		c.Level, c.Detail, c.Fix = Warn, "the configuration is valid but has no accounts", "e5extend account add <name> --auth device_code --tenant <tenant> --client-id <id>"
	default:
		c.Level, c.Detail = OK, fmt.Sprintf("valid, %d account(s)", len(cfg.Accounts))
	}
	return c
}

func checkVault(d Deps, cfg *config.Config) Check {
	c := Check{ID: "vault", Title: "Vault"}
	v, err := d.OpenVault(cfg)
	if err != nil {
		c.Level, c.Detail, c.Fix = Fail, firstLines(err, 2), d.hint(err, "")
		return c
	}
	var problems []string
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
				problems = append(problems, acct.Name+": cannot look for stored data")
				firstErr = cmp(firstErr, herr)
				continue
			}
			if !has {
				if acct.NeedsSecret() && i == 0 {
					problems = append(problems, acct.Name+": the secret is missing")
					firstErr = cmp(firstErr, fmt.Errorf("secret missing: %w", vault.ErrNotFound))
				}
				continue
			}
			checked++
			// Only ask for the master key when there is something to decrypt.
			if _, gerr := v.Get(key); gerr != nil {
				switch {
				case errors.Is(gerr, vault.ErrWrongKey):
					problems = append(problems, acct.Name+": cannot decrypt stored data (wrong master key?)")
				case errors.Is(gerr, vault.ErrNoMasterKey):
					problems = append(problems, acct.Name+": no master key available")
				default:
					problems = append(problems, acct.Name+": "+firstLines(gerr, 1))
				}
				firstErr = cmp(firstErr, gerr)
			}
		}
	}
	if len(problems) > 0 {
		c.Level, c.Detail = Fail, strings.Join(dedup(problems), "; ")
		c.Fix = d.hint(firstErr, firstAccount(cfg))
		if errors.Is(firstErr, vault.ErrNotFound) {
			c.Fix = "e5extend account set-secret <name>"
		}
		return c
	}
	c.Level = OK
	if checked == 0 {
		c.Detail = "no encrypted data stored yet"
	} else {
		c.Detail = fmt.Sprintf("master key works; %d stored item(s) decrypt", checked)
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

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func checkEngine(d Deps) Check {
	c := Check{ID: "engine", Title: "Engine"}
	running := engine.Running(d.DataDir)
	hb, hbErr := engine.ReadHeartbeat(d.DataDir)
	switch {
	case !running:
		c.Level, c.Detail = Fail, "the engine is not running"
		if hbErr == nil {
			c.Detail += " (last seen " + hb.Local().Format("2006-01-02 15:04") + ")"
		}
		c.Fix = "e5extend service install   (or, in the foreground: e5extend run)"
	case hbErr != nil || d.now().Sub(hb) > HeartbeatMaxAge:
		c.Level, c.Detail = Warn, "the engine holds its lock but its heartbeat is older than 2 minutes"
		c.Fix = "check the engine log; restart it if it is stuck"
	default:
		c.Level, c.Detail = OK, fmt.Sprintf("running (heartbeat %s ago)", d.now().Sub(hb).Round(time.Second))
	}
	return c
}

func checkService(d Deps) []Check {
	c := Check{ID: "service", Title: "Background service"}
	if d.Service == nil {
		c.Level, c.Detail = Warn, "background services are not supported on this platform"
		return []Check{c}
	}
	st, err := d.Service()
	var out []Check
	switch {
	case err != nil:
		c.Level, c.Detail = Warn, "cannot ask the service manager: "+firstLines(err, 2)
	case !st.Installed:
		c.Level, c.Detail, c.Fix = Warn, "not installed (fine if you start e5extend some other way)", "e5extend service install"
	case st.Enabled == service.No:
		c.Level, c.Detail, c.Fix = Warn, "installed but disabled", "e5extend service install"
	case st.Running == service.No:
		c.Level, c.Detail, c.Fix = Warn, "installed and enabled, but not running", "e5extend service status"
	default:
		c.Level, c.Detail = OK, "installed and enabled"
		if st.Running == service.Yes {
			c.Detail += ", running"
		}
	}
	out = append(out, c)
	if d.GOOS == service.Linux && err == nil && st.Installed {
		switch st.Linger {
		case service.No:
			out = append(out, Check{ID: "linger", Title: "Linger", Level: Warn,
				Detail: "lingering is off: the service stops when you log out",
				Fix:    "loginctl enable-linger $USER"})
		case service.Yes:
			out = append(out, Check{ID: "linger", Title: "Linger", Level: OK, Detail: "lingering is on"})
		}
	}
	return out
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

	out = append(out, checkActivity(d, acct, as, id, title), checkState(acct, as, id, title), checkAPIs(d, acct, id, title))
	if acct.NeedsSecret() && acct.SecretExpires != "" {
		out = append(out, checkSecret(d, acct, id, title))
	}
	return out
}

func checkToken(ctx context.Context, d Deps, cfg *config.Config, acct config.Account, id, title string) (Check, auth.Token, bool) {
	c := Check{ID: id + ".token", Title: title + " sign-in"}
	au, err := d.NewAuth(cfg, acct)
	if err != nil {
		c.Level, c.Detail, c.Fix = Fail, tokenLabel(err), d.hint(err, acct.Name)
		return c, auth.Token{}, false
	}
	tok, err := au.Token(ctx)
	if err != nil {
		c.Level, c.Detail, c.Fix = Fail, tokenLabel(err), d.hint(err, acct.Name)
		if errors.Is(err, auth.ErrTransient) {
			c.Level = Warn
		}
		return c, auth.Token{}, false
	}
	c.Level = OK
	c.Detail = fmt.Sprintf("token ok, valid for %s", time.Until(tok.ExpiresOn).Round(time.Minute))
	if tok.ExpiresOn.IsZero() {
		c.Detail = "token ok"
	}
	return c, tok, true
}

// tokenLabel names a sign-in failure in fixed words; provider text can hold
// tenant names and user names, so it is never shown.
func tokenLabel(err error) string {
	switch {
	case errors.Is(err, auth.ErrLoginRequired):
		return "login required"
	case errors.Is(err, auth.ErrInvalidClient):
		return "client credentials rejected"
	case errors.Is(err, auth.ErrConsentRequired):
		return "admin consent required"
	case errors.Is(err, auth.ErrTransient):
		return "could not reach the sign-in service (network or service problem)"
	case errors.Is(err, vault.ErrNoMasterKey):
		return "the vault master key is not available"
	case errors.Is(err, vault.ErrWrongKey):
		return "stored data cannot be decrypted (wrong master key?)"
	}
	code := auth.ErrorCode(err)
	if code == "" {
		code = "unknown error"
	}
	return "sign-in failed (" + code + ")"
}

func checkScopes(acct config.Account, id, title string, scopes []string, recorded bool) Check {
	c := Check{ID: id + ".scopes", Title: title + " permissions"}
	apis, err := catalog.Load()
	if err != nil {
		c.Level, c.Detail = Fail, "the built-in API list is broken: "+err.Error()
		return c
	}
	kind := catalog.Delegated
	if !acct.Delegated() {
		kind = catalog.App
	}
	n := len(catalog.Available(apis, kind, scopes, acct.Categories))
	src := ""
	if recorded {
		src = " (as recorded by the last round)"
	}
	switch {
	case n == 0:
		c.Level, c.Detail = Fail, "no API in the catalog can be called with the granted permissions"+src
		c.Fix = "grant more API permissions and admin consent (docs/user/app-registration.md); see: e5extend catalog list --account " + acct.Name
	case n < fewAPIs:
		c.Level, c.Detail = Warn, fmt.Sprintf("only %d API(s) usable%s; more permissions give more variety", n, src)
		c.Fix = "see: e5extend catalog list --account " + acct.Name
	default:
		c.Level, c.Detail = OK, fmt.Sprintf("%d APIs usable%s", n, src)
	}
	return c
}

func checkActivity(d Deps, acct config.Account, as *store.AccountState, id, title string) Check {
	c := Check{ID: id + ".activity", Title: title + " activity"}
	if acct.Paused {
		c.Level, c.Detail = OK, "paused in the configuration"
		return c
	}
	if as == nil || as.LastSuccess.IsZero() {
		c.Level, c.Detail, c.Fix = Warn, "no successful call recorded yet", "e5extend run-now "+acct.Name
		return c
	}
	age := d.now().Sub(as.LastSuccess)
	c.Detail = "last successful call " + humanAge(age) + " ago"
	switch {
	case age > ActivityFail:
		c.Level, c.Fix = Fail, "e5extend status; then e5extend run-now "+acct.Name
	case age > ActivityWarn:
		c.Level, c.Fix = Warn, "e5extend status; then e5extend run-now "+acct.Name
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

func checkState(acct config.Account, as *store.AccountState, id, title string) Check {
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
		c.Level, c.Detail, c.Fix = Warn, "paused in the configuration", "e5extend account resume "+acct.Name
	case status == store.StatusNeedsAttention:
		c.Level, c.Detail = Fail, "needs attention: "+reason
		c.Fix = "fix the cause above, then: e5extend account resume " + acct.Name
	case status == store.StatusPaused:
		c.Level, c.Detail = Fail, "paused automatically: "+reason
		c.Fix = "e5extend account resume " + acct.Name
	case status == store.StatusPeriodEnded:
		c.Level, c.Detail = Warn, "the run period has ended"
		c.Fix = "extend schedule.period_end in the configuration to keep running"
	default:
		c.Level, c.Detail = OK, "ok"
	}
	return c
}

func checkSecret(d Deps, acct config.Account, id, title string) Check {
	c := Check{ID: id + ".secret", Title: title + " client secret"}
	days, ok := notify.DaysLeft(d.now(), acct.SecretExpires)
	if !ok {
		c.Level, c.Detail = Warn, "secret_expires is not a YYYY-MM-DD date"
		return c
	}
	c.Fix = "create a new secret in the app registration, then: e5extend account set-secret " + acct.Name + " --secret-expires YYYY-MM-DD"
	switch {
	case days < 0:
		c.Level, c.Detail = Fail, fmt.Sprintf("expired on %s", acct.SecretExpires)
	case days == 0:
		c.Level, c.Detail = Fail, "expires today"
	case days <= 7:
		c.Level, c.Detail = Fail, fmt.Sprintf("expires in %d day(s), on %s", days, acct.SecretExpires)
	case days <= 30:
		c.Level, c.Detail = Warn, fmt.Sprintf("expires in %d days, on %s", days, acct.SecretExpires)
	default:
		c.Level, c.Detail, c.Fix = OK, fmt.Sprintf("expires in %d days", days), ""
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
		nc.Level, nc.Detail = Fail, "bad Graph address: "+err.Error()
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
		nc.Level, nc.Detail = Fail, "cannot reach Microsoft Graph: "+reason
		nc.Fix = "check the internet connection and the proxy (network.proxy in the config, or HTTPS_PROXY)"
		cc.Level, cc.Detail = Warn, "could not compare the clock because Graph is unreachable"
		return []Check{cc, nc}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	nc.Level, nc.Detail = OK, fmt.Sprintf("Microsoft Graph answered (HTTP %d)", resp.StatusCode)
	if resp.StatusCode >= 500 {
		nc.Level, nc.Detail = Warn, fmt.Sprintf("Microsoft Graph answered HTTP %d; the service may be having trouble", resp.StatusCode)
	}

	remote, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		cc.Level, cc.Detail = Warn, "Graph's answer has no usable Date header"
		return []Check{cc, nc}
	}
	diff := received.Sub(remote)
	abs := diff
	if abs < 0 {
		abs = -abs
	}
	cc.Detail = fmt.Sprintf("this machine is %s %s Graph's clock", abs.Round(time.Second), map[bool]string{true: "ahead of", false: "behind"}[diff >= 0])
	switch {
	case abs > ClockFail:
		cc.Level, cc.Fix = Fail, "turn on automatic time (NTP) and check the time zone"
	case abs > ClockWarn:
		cc.Level, cc.Fix = Warn, "turn on automatic time (NTP) and check the time zone"
	default:
		cc.Level, cc.Detail = OK, "within "+ClockWarn.String()+" of Graph's clock"
	}
	return []Check{cc, nc}
}

// skipped is a placeholder used only when the clock cannot be checked at all.
func (c Check) skipped() Check {
	c.Level, c.Detail = Warn, "not checked"
	return c
}

func checkNotify(d Deps, cfg *config.Config) Check {
	c := Check{ID: "notify", Title: "Alerts"}
	inVault, unreadable := false, false
	if v, err := d.OpenVault(cfg); err == nil {
		inVault, _ = v.Has(notify.VaultKey)
		if inVault {
			// Only whether it can be decrypted is of interest; the address is never shown.
			if _, gerr := v.Get(notify.VaultKey); gerr != nil {
				unreadable = true
			}
		}
	}
	plain := cfg.Notify.WebhookURL != ""
	switch {
	case unreadable:
		c.Level = Warn
		c.Detail = "the stored webhook address cannot be decrypted, so no alert can be sent"
		c.Fix = "restore the master key, or set the address again: e5extend notify set-webhook <url>"
	case plain:
		c.Level = Warn
		c.Detail = "the webhook address is stored in plain text in config.toml"
		c.Fix = "e5extend notify set-webhook <url>   (moves it into the vault and clears the config entry)"
	case !inVault:
		c.Level = Warn
		c.Detail = "no alert destination is set; you will not hear about problems"
		c.Fix = "e5extend notify set-webhook <url>"
	default:
		c.Level, c.Detail = OK, "webhook set (stored in the vault)"
	}
	return c
}

// checkAPIs warns about a category whose calls found nothing in each of its
// last FailingRounds rounds, which usually means the account lacks the service
// (no mailbox, no OneDrive). It only reads the local call log.
func checkAPIs(d Deps, acct config.Account, id, title string) Check {
	c := Check{ID: id + ".apis", Title: title + " APIs"}
	apis, err := catalog.Load()
	if err != nil {
		c.Level, c.Detail = Warn, "the API catalog could not be read"
		return c
	}
	category := make(map[string]string, len(apis))
	for _, a := range apis {
		category[a.ID] = a.Category
	}
	entries, err := store.New(d.DataDir).Read(store.Filter{Account: acct.Name, Since: d.now().Add(-failingWindow)})
	if err != nil {
		c.Level, c.Detail = OK, "the call log could not be read"
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
	for _, e := range entries {
		cat, known := category[e.API]
		if e.Event != "" || e.Round == "" || !known {
			continue
		}
		missing := e.Class == string(graph.NotFound) || (e.Class == string(graph.ClientError) && e.Status == 400)
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
	var parts []string
	for _, cat := range order {
		rs := rounds[cat]
		if len(rs) < FailingRounds {
			continue
		}
		failing := true
		for _, r := range rs[len(rs)-FailingRounds:] {
			failing = failing && r.allFound
		}
		if failing {
			parts = append(parts, fmt.Sprintf("category %s keeps failing (not found%s)", cat, missingHint(cat)))
		}
	}
	if len(parts) == 0 {
		c.Level, c.Detail = OK, fmt.Sprintf("no category found nothing in its last %d rounds", FailingRounds)
		return c
	}
	c.Level, c.Detail = Warn, strings.Join(parts, "; ")
	c.Fix = "e5extend account test " + acct.Name + " --recommend"
	return c
}

// missingHint guesses why a category finds nothing, from the probe table.
func missingHint(category string) string {
	for _, p := range recommend.Probes() {
		for _, c := range p.Categories {
			if c != category {
				continue
			}
			switch p.Reason {
			case recommend.ReasonNoMailbox:
				return "; no mailbox?"
			case recommend.ReasonNoOneDrive:
				return "; no OneDrive?"
			case recommend.ReasonNoTeams:
				return "; no Teams license?"
			case recommend.ReasonNoSharePoint:
				return "; no SharePoint?"
			}
		}
	}
	return ""
}
