// Package doctor runs the health checks behind `e5extend doctor`. Every check
// returns a level, a short detail and, where one exists, the command that
// fixes the problem. Details never contain tokens, secrets, user names or
// webhook addresses.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
	"github.com/KKazuhaK/e5-developer-extend/internal/subscription"
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

	// DeliveryFailures is how many failed deliveries in a row make the doctor
	// warn about a channel; DeliveryStale is how long without a success does too.
	DeliveryFailures = 3
	DeliveryStale    = 7 * 24 * time.Hour

	// FailingRounds is how many rounds in a row a category must have failed
	// with "not found" before the doctor warns about it.
	FailingRounds = 3
	// failingWindow is how far back the call log is read for that check.
	failingWindow = 14 * 24 * time.Hour
)

// Check is one result. Detail and Fix are English text, for the CLI. The
// same words come as codes for a user interface in another language: Code and
// Params name the detail (or Parts, when it is several messages shown one
// after another), FixCode and FixParams the fix (or FixParts). See Msg.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Level  Level  `json:"level"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`

	Code      string            `json:"code,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	Parts     []Msg             `json:"parts,omitempty"`
	FixCode   string            `json:"fixCode,omitempty"`
	FixParams map[string]string `json:"fixParams,omitempty"`
	FixParts  []Msg             `json:"fixParts,omitempty"`
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
	// OpenVaultWithKeyFile opens the vault with the master key read from a
	// DPAPI key file instead of the usual sources (the key a headless Windows
	// task uses); nil skips that check.
	OpenVaultWithKeyFile func(cfg *config.Config, keyFile string) (Vault, error)
	NewAuth              func(cfg *config.Config, acct config.Account) (auth.Authenticator, error)
	// HTTP returns the client for the network probe (it honors the proxy setting).
	HTTP func(cfg *config.Config) *http.Client
	// Service asks the platform about the background job; nil when unsupported.
	Service func() (service.Status, error)
	// Hint suggests a next step for a sign-in or vault error, or "".
	Hint func(err error, account string) string
	// HintCode names the suggestion Hint gives for err (one of HintCodes), or
	// "" when there is none; nil when the caller cannot name them.
	HintCode func(err error) string
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
		group{"service", "Background service", func(context.Context) []Check { return checkService(d, cfg) }},
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
	if cfg != nil && cfgErr == nil {
		st, _ := store.New(d.DataDir).LoadState()
		for _, tv := range subscription.Build(cfg, st, d.now()) {
			groups = append(groups, group{"subscription." + tv.Label, "Subscription " + tv.Label,
				func(context.Context) []Check { return []Check{checkSubscription(d, cfg, tv)} }})
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
						c := Check{ID: g.id, Title: g.title, Level: Fail}
						c.setDetail(M("check.crashed"))
						ch <- []Check{c}
					}
				}()
				ch <- g.run(ctx)
			}()
			select {
			case results[i] = <-ch:
			case <-ctx.Done():
				c := Check{ID: g.id, Title: g.title, Level: Fail}
				c.setDetail(M("check.timeout", "seconds", secs(Timeout)))
				results[i] = []Check{c}
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
