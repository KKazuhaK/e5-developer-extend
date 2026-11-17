// Package store keeps engine state (state.json) and the call log (one JSONL
// file per day). Neither ever holds tokens, secrets, response bodies or user
// identities.
package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
)

// StateVersion is the current state.json format version.
const StateVersion = 1

// Account statuses.
const (
	StatusOK             = "ok"
	StatusNeedsAttention = "needs_attention"
	StatusPaused         = "paused"
	StatusPeriodEnded    = "period_ended"
)

// Totals are lifetime counters of one account.
type Totals struct {
	Rounds int `json:"rounds"`
	Calls  int `json:"calls"`
	OK     int `json:"ok"`
	Failed int `json:"failed"`
}

// APIStat counts the calls of one API for one account. Only the engine writes it.
type APIStat struct {
	OK         int       `json:"ok"`
	Failed     int       `json:"failed"`
	LastClass  string    `json:"last_class,omitempty"` // graph.Class of the last call
	LastStatus int       `json:"last_status,omitempty"`
	LastAt     time.Time `json:"last_at,omitzero"`
}

// Total is the number of counted calls.
func (a APIStat) Total() int { return a.OK + a.Failed }

// Notice records the last time an alert of one kind was sent for an account,
// so the same condition is not announced again. Key holds the value the
// decision was based on (a threshold, a reason); it never holds secrets.
type Notice struct {
	At  time.Time `json:"at"`
	Key string    `json:"key,omitempty"`
	// Pending means the alert was handed to the sender but not yet confirmed
	// delivered; Msg and Tried let the engine try again later.
	Pending bool      `json:"pending,omitempty"`
	Tried   time.Time `json:"tried,omitzero"`
	Msg     string    `json:"msg,omitempty"`
	// Held means the alert is waiting for the quiet hours to end; it is sent
	// then, together with the others that were held.
	Held bool `json:"held,omitempty"`
	// Cooled means the cooldown kept the alert back while its condition still
	// holds; it is sent (Msg) once the cooldown ends. Nothing was announced yet.
	Cooled bool `json:"cooled,omitempty"`
}

// PasswordBlock records why automatic password sign-in stopped.
type PasswordBlock struct {
	Code string    `json:"code"` // AADSTS code of the failure
	At   time.Time `json:"at"`
}

// AccountState is the persisted state of one account.
type AccountState struct {
	NextRun                time.Time            `json:"next_run,omitzero"`
	LastRun                time.Time            `json:"last_run,omitzero"`
	LastSuccess            time.Time            `json:"last_success,omitzero"`
	Status                 string               `json:"status"`
	Reason                 string               `json:"reason,omitempty"`
	ConsecutiveFailedRound int                  `json:"consecutive_failed_rounds"`
	GrantedScopes          []string             `json:"granted_scopes,omitempty"`
	APISkipUntil           map[string]time.Time `json:"api_skip_until,omitempty"`
	APIForbiddenCount      map[string]int       `json:"api_forbidden_count,omitempty"`
	Totals                 Totals               `json:"totals"`
	// APIStats counts calls per catalog API id; ids that left the catalog are dropped.
	APIStats map[string]*APIStat `json:"api_stats,omitempty"`
	// PasswordBlocked is set after a password sign-in failed in a way that
	// retrying cannot fix; the engine then stops signing the account in with
	// its password until the person acts (set-secret, login, editing the account).
	PasswordBlocked *PasswordBlock `json:"password_blocked,omitempty"`
	// LastPasswordAttempt is when the engine last tried an automatic password sign-in.
	LastPasswordAttempt time.Time `json:"last_password_attempt,omitzero"`
	// LastPasswordCode is the AADSTS code the last automatic password sign-in failed with ("" when it worked),
	// so the reason shown inside the interval is the real cause.
	LastPasswordCode string `json:"last_password_code,omitempty"`
	// Notified maps an alert kind to its last notice; only the engine writes it.
	Notified map[string]Notice `json:"notified,omitempty"`
	// LastSent is when an alert of each kind was last handed out, which is
	// what the cooldown is measured from; unlike Notified it outlives the
	// condition.
	LastSent map[string]time.Time `json:"last_sent,omitempty"`
}

// State is the content of state.json.
type State struct {
	Version  int                      `json:"version"`
	Accounts map[string]*AccountState `json:"accounts"`
	// Notified and LastSent are the notices of alerts that belong to no
	// single account (all_failing).
	Notified map[string]Notice    `json:"notified,omitempty"`
	LastSent map[string]time.Time `json:"last_sent,omitempty"`
	// Tenants holds what the subscription check saw, per tenant (see config.Account.TenantKey).
	Tenants map[string]*TenantState `json:"tenants,omitempty"`
	// TenantAliases maps the key an account's configured tenant gives (a
	// domain, or "account:<name>") to the tenant GUID a token revealed, so
	// accounts that name one tenant in different ways share one state.
	TenantAliases map[string]string `json:"tenant_aliases,omitempty"`
	// RenamedFrom lists, per account, the names it had before (newest last).
	// The call log keeps the name an entry was written under, so reading the
	// log of an account also reads the entries of these names.
	RenamedFrom map[string][]string `json:"renamed_from,omitempty"`
}

// NewState returns an empty state.
func NewState() *State {
	return &State{Version: StateVersion, Accounts: map[string]*AccountState{}}
}

// Entry is one line of the call log: a call (API set) or an event (Event set).
type Entry struct {
	TS        time.Time `json:"ts"`
	Account   string    `json:"account,omitempty"`
	Round     string    `json:"round,omitempty"`
	API       string    `json:"api,omitempty"`
	Status    int       `json:"status,omitempty"`
	Class     string    `json:"class,omitempty"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
	Event     string    `json:"event,omitempty"`
	OK        int       `json:"ok,omitempty"`
	Failed    int       `json:"failed,omitempty"`
	Skipped   string    `json:"skipped_reason,omitempty"`
	Message   string    `json:"msg,omitempty"`
}

// Filter selects entries for Read. Zero fields match everything.
type Filter struct {
	Account string
	Since   time.Time
	Limit   int // newest Limit entries; 0 means all
}

// Store reads and writes under one data directory.
type Store struct {
	dir string
	mu  sync.Mutex
}

// New returns a Store rooted at the data directory dir.
func New(dir string) *Store { return &Store{dir: dir} }

// Dir returns the data directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) statePath() string { return filepath.Join(s.dir, "state.json") }
func (s *Store) logDir() string    { return filepath.Join(s.dir, "logs") }

// LoadState reads state.json. A missing file gives an empty state; a damaged
// one gives an empty state together with an error.
func (s *Store) LoadState() (*State, error) {
	b, err := os.ReadFile(s.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return NewState(), nil
	}
	if err != nil {
		return NewState(), err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return NewState(), fmt.Errorf("state.json is damaged: %w", err)
	}
	if st.Version > StateVersion {
		return NewState(), fmt.Errorf("state.json version %d is newer than supported (%d)", st.Version, StateVersion)
	}
	if st.Accounts == nil {
		st.Accounts = map[string]*AccountState{}
	}
	for k, a := range st.Accounts {
		if a == nil {
			delete(st.Accounts, k)
		}
	}
	return &st, nil
}

// SaveState writes state.json atomically.
func (s *Store) SaveState(st *State) error {
	st.Version = StateVersion
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(s.statePath(), append(b, '\n'))
}

const (
	logPrefix = "calls-"
	logSuffix = ".jsonl"
	dayLayout = "2006-01-02"
)

func logName(t time.Time) string { return logPrefix + t.Format(dayLayout) + logSuffix }

// Append writes e as one line to the file of e.TS's local date.
func (s *Store) Append(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := paths.EnsureDir(s.logDir()); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.logDir(), logName(e.TS)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// logFiles lists log files with their dates, oldest first.
func (s *Store) logFiles(loc *time.Location) ([]logFile, error) {
	ents, err := os.ReadDir(s.logDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []logFile
	for _, de := range ents {
		n := de.Name()
		if de.IsDir() || !strings.HasPrefix(n, logPrefix) || !strings.HasSuffix(n, logSuffix) {
			continue
		}
		d, err := time.ParseInLocation(dayLayout, strings.TrimSuffix(strings.TrimPrefix(n, logPrefix), logSuffix), loc)
		if err != nil {
			continue
		}
		out = append(out, logFile{filepath.Join(s.logDir(), n), d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].day.Before(out[j].day) })
	return out, nil
}

type logFile struct {
	path string
	day  time.Time
}

// namesOf returns the names whose log entries belong to account: the name
// itself and the ones the account had before (State.RenamedFrom).
func (s *Store) namesOf(account string) map[string]bool {
	if account == "" {
		return nil
	}
	names := map[string]bool{account: true}
	if st, err := s.LoadState(); err == nil {
		for _, n := range st.RenamedFrom[account] {
			names[n] = true
		}
	}
	return names
}

// Read returns the entries matching f, oldest first. Damaged lines are skipped.
func (s *Store) Read(f Filter) ([]Entry, error) {
	names := s.namesOf(f.Account)
	loc := time.Local
	if !f.Since.IsZero() {
		loc = f.Since.Location()
	}
	files, err := s.logFiles(loc)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, lf := range files {
		// A file holds one local day; skip those that end before Since.
		if !f.Since.IsZero() && lf.day.AddDate(0, 0, 2).Before(f.Since) {
			continue
		}
		data, err := os.ReadFile(lf.path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue
			}
			if f.Account != "" && !names[e.Account] {
				continue
			}
			if !f.Since.IsZero() && e.TS.Before(f.Since) {
				continue
			}
			out = append(out, e)
		}
	}
	// Concurrent writers may interleave slightly out of order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[len(out)-f.Limit:]
	}
	return out, nil
}

// Prune deletes log files whose date is more than keepDays before now's date.
// It returns the number of files removed.
func (s *Store) Prune(now time.Time, keepDays int) (int, error) {
	files, err := s.logFiles(now.Location())
	if err != nil {
		return 0, err
	}
	y, m, d := now.Date()
	cutoff := time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, -keepDays)
	n := 0
	var errs []error
	for _, lf := range files {
		if lf.day.Before(cutoff) {
			if err := os.Remove(lf.path); err != nil {
				errs = append(errs, err)
				continue
			}
			n++
		}
	}
	return n, errors.Join(errs...)
}

// Access says what the last subscription check of a tenant could do.
const (
	AccessOK           = "ok"            // the subscriptions were read
	AccessNoPermission = "no_permission" // Graph refused: Organization.Read.All is missing
	AccessError        = "error"         // could not be checked (network, sign-in, server)
)

// MaxRenewals is how many renewals a subscription keeps.
const MaxRenewals = 20

// Renewal records that a subscription's next lifecycle date moved later.
type Renewal struct {
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	SeenAt time.Time `json:"seen_at"`
}

// SubscriptionState is the last thing seen of one subscription of a tenant.
type SubscriptionState struct {
	SKU           string    `json:"sku,omitempty"`
	Status        string    `json:"status"`
	NextLifecycle time.Time `json:"next_lifecycle,omitzero"`
	CheckedAt     time.Time `json:"checked_at"`
	IsTrial       bool      `json:"is_trial,omitempty"`
	TotalLicenses int       `json:"total_licenses,omitempty"`
	History       []Renewal `json:"history,omitempty"`

	// AnnouncedStatus is the status the alerts have caught up with ("" before
	// the first evaluation); RenewalNotified is the SeenAt of the newest
	// renewal that was announced. Only the alert evaluation writes them.
	AnnouncedStatus string    `json:"announced_status,omitempty"`
	RenewalNotified time.Time `json:"renewal_notified,omitzero"`
	// Notified and LastSent are the alert notices of this subscription.
	Notified map[string]Notice    `json:"notified,omitempty"`
	LastSent map[string]time.Time `json:"last_sent,omitempty"`
}

// TenantState is the subscription knowledge of one tenant.
type TenantState struct {
	// TenantID is the tenant's GUID when a token or Graph told it.
	TenantID string `json:"tenant_id,omitempty"`
	// Access is the outcome of the last check; Detail explains AccessError
	// without secrets. Checked is the last successful read, Attempted the last try.
	Access    string    `json:"access,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	Checked   time.Time `json:"checked_at,omitzero"`
	Attempted time.Time `json:"attempted_at,omitzero"`
	// Failures counts the failed checks in a row, for the backoff.
	Failures      int                           `json:"failures,omitempty"`
	Subscriptions map[string]*SubscriptionState `json:"subscriptions,omitempty"`
	// Notified and LastSent are the notices of the manual expiry date.
	Notified map[string]Notice    `json:"notified,omitempty"`
	LastSent map[string]time.Time `json:"last_sent,omitempty"`
}
