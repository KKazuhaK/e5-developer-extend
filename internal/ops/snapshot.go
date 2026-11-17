package ops

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// StaleAfter marks an account whose last success is older than this.
const StaleAfter = 3 * 24 * time.Hour

// Engine hosts reported in a Snapshot.
const (
	HostGUI     = "gui"
	HostService = "service"
	HostNone    = "none"
)

// Account statuses reported in a Snapshot.
const (
	StatusOK             = "ok"
	StatusNeedsAttention = "needs_attention"
	StatusPaused         = "paused"
	StatusPeriodEnded    = "period_ended"
	StatusNotSignedIn    = "not_signed_in"
)

// EngineInfo says whether an engine runs and who hosts it.
type EngineInfo struct {
	Running         bool   `json:"running"`
	HeartbeatAgeSec *int64 `json:"heartbeatAgeSec"`
	Host            string `json:"host"`
}

// AccountSummary is the state of one account, for display.
type AccountSummary struct {
	Name          string  `json:"name"`
	Auth          string  `json:"auth"`
	Tenant        string  `json:"tenant"`
	Status        string  `json:"status"`
	Reason        string  `json:"reason"`
	Fix           string  `json:"fix"`
	NextRun       *string `json:"nextRun"`
	LastRun       *string `json:"lastRun"`
	LastSuccess   *string `json:"lastSuccess"`
	TodayOK       int     `json:"todayOk"`
	TodayFailed   int     `json:"todayFailed"`
	UsableAPIs    int     `json:"usableApis"`
	TotalAPIs     int     `json:"totalApis"`
	SecretExpires *string `json:"secretExpires"`
	Paused        bool    `json:"paused"`

	// Account and State are for the command line, which formats them itself.
	Account config.Account      `json:"-"`
	State   *store.AccountState `json:"-"`
}

// Snapshot is the state of the engine and of every account right now.
type Snapshot struct {
	Engine      EngineInfo       `json:"engine"`
	Accounts    []AccountSummary `json:"accounts"`
	GeneratedAt string           `json:"generatedAt"`

	// Heartbeat is the engine's last heartbeat, zero if none was readable.
	Heartbeat time.Time `json:"-"`
	// StateErr is set when state.json could not be read; the accounts then show no history.
	StateErr error `json:"-"`
}

func rfc3339(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

// Snapshot reads the configuration, the state file and today's call log. It
// never touches the network or the master key.
func (o *Ops) Snapshot() (Snapshot, error) {
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return Snapshot{}, err
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return Snapshot{}, err
	}
	now := time.Now()
	st := store.New(dataDir)
	snap := Snapshot{GeneratedAt: now.Format(time.RFC3339), Accounts: []AccountSummary{}}

	snap.Engine = EngineInfo{Host: HostNone}
	if hb, err := engine.ReadHeartbeat(dataDir); err == nil {
		snap.Heartbeat = hb
	}
	if engine.Running(dataDir) {
		snap.Engine.Running = true
		snap.Engine.Host = HostService
		if EngineHost(dataDir) == HostGUI {
			snap.Engine.Host = HostGUI
		}
		if !snap.Heartbeat.IsZero() {
			age := int64(now.Sub(snap.Heartbeat).Seconds())
			snap.Engine.HeartbeatAgeSec = &age
		}
	}

	state, err := st.LoadState()
	snap.StateErr = err
	if len(cfg.Accounts) == 0 {
		return snap, nil
	}

	// Today's calls per account.
	y, m, d := now.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	today, _ := st.Read(store.Filter{Since: midnight})
	type counts struct{ ok, failed int }
	byAcct := map[string]*counts{}
	for _, en := range today {
		if en.API == "" || en.API == "users.pick" {
			continue
		}
		c := byAcct[en.Account]
		if c == nil {
			c = &counts{}
			byAcct[en.Account] = c
		}
		switch en.Class {
		case "ok":
			c.ok++
		case "throttled", "network", "canceled":
		default:
			c.failed++
		}
	}

	apis, _ := catalog.Load()
	var local map[string]AccountEntry
	if v, err := o.OpenVault(cfg); err == nil {
		local = map[string]AccountEntry{}
		for _, acct := range cfg.Accounts {
			local[acct.Name] = LocalEntry(v, acct)
		}
	}

	for _, acct := range cfg.Accounts {
		as := state.Accounts[acct.Name]
		sum := AccountSummary{
			Name: acct.Name, Auth: acct.Auth, Tenant: acct.Tenant, Account: acct, State: as,
			TotalAPIs: len(apis), Paused: acct.Paused,
		}
		if c := byAcct[acct.Name]; c != nil {
			sum.TodayOK, sum.TodayFailed = c.ok, c.failed
		}
		if acct.SecretExpires != "" {
			s := acct.SecretExpires
			sum.SecretExpires = &s
		}
		var granted []string
		if as != nil {
			sum.LastRun, sum.LastSuccess = rfc3339(as.LastRun), rfc3339(as.LastSuccess)
			if !acct.Paused {
				sum.NextRun = rfc3339(as.NextRun)
			}
			granted = as.GrantedScopes
			sum.Paused = acct.Paused || as.Status == store.StatusPaused
		}
		kind := catalog.Delegated
		if !acct.Delegated() {
			kind = catalog.App
		}
		sum.UsableAPIs = len(catalog.Available(apis, kind, granted, acct.Categories))

		sum.Status, sum.Reason, sum.Fix = summaryStatus(acct, as, local[acct.Name], local != nil)
		snap.Accounts = append(snap.Accounts, sum)
	}
	return snap, nil
}

// summaryStatus maps the stored state to one display status with a reason and
// a suggested fix. ok is false when the vault could not be looked at.
func summaryStatus(acct config.Account, as *store.AccountState, local AccountEntry, haveLocal bool) (status, reason, fix string) {
	switch {
	case acct.Paused:
		return StatusPaused, "paused by you", "resume it when you want it to run again"
	case as != nil && as.Status == store.StatusPaused:
		reason, fix = splitReason(as.Reason)
		return StatusPaused, reason, fix
	case as != nil && as.Status == store.StatusPeriodEnded:
		return StatusPeriodEnded, "the run period has ended", "change the run period in the settings to keep it running"
	case haveLocal && !local.Credentials:
		if acct.NeedsSecret() {
			return StatusNotSignedIn, "no secret is stored", "set the secret"
		}
		return StatusNotSignedIn, "not signed in yet", "sign in"
	case as != nil && as.Status == store.StatusNeedsAttention:
		reason, fix = splitReason(as.Reason)
		return StatusNeedsAttention, reason, fix
	}
	if haveLocal && local.SecretExpired {
		return StatusNeedsAttention, "the client secret has expired", "set a new secret"
	}
	if as != nil && !as.LastSuccess.IsZero() && time.Since(as.LastSuccess) > StaleAfter {
		return StatusNeedsAttention, "no successful call for " + strconv.Itoa(int(time.Since(as.LastSuccess).Hours()/24)) + " days", "run a round now and check the log"
	}
	return StatusOK, "", ""
}

// splitReason splits "what happened; what to do" as the engine stores it.
func splitReason(s string) (reason, fix string) {
	r, f, ok := strings.Cut(s, "; ")
	if !ok {
		return s, ""
	}
	return r, f
}

// ---- who hosts the engine ----

const hostFile = "engine.host"

// MarkEngineHost records that this process hosts the engine for the desktop
// app, so other processes can tell it from a background service. The returned
// function removes the mark; a mark left by a crash is ignored once its
// process is gone.
func MarkEngineHost(dataDir string) (remove func(), err error) {
	if err := paths.EnsureDir(dataDir); err != nil {
		return nil, err
	}
	p := filepath.Join(dataDir, hostFile)
	if err := paths.WriteFileAtomic(p, []byte(HostGUI+" "+strconv.Itoa(os.Getpid())+"\n")); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(p) }, nil
}

// ClearEngineHost removes the mark. A process that has just started calls it:
// any mark it finds is not its own, and could otherwise be matched by a reused process ID.
func ClearEngineHost(dataDir string) { _ = os.Remove(filepath.Join(dataDir, hostFile)) }

// EngineHost returns HostGUI when the mark file names a live process, else HostService.
func EngineHost(dataDir string) string {
	b, err := os.ReadFile(filepath.Join(dataDir, hostFile))
	if err != nil {
		return HostService
	}
	fields := strings.Fields(string(b))
	if len(fields) != 2 || fields[0] != HostGUI {
		return HostService
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil || !pidAlive(pid) {
		return HostService
	}
	return HostGUI
}

func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		_ = p.Release()
		return true
	}
	return p.Signal(syscall.Signal(0)) == nil
}
