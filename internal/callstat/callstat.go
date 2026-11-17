// Package callstat reads the call log the way the health check and the alert
// rules do, so both agree on what counts as a real problem: a call that found
// nothing (404, or 400 for a missing service) means the account lacks the
// service, not that anything is broken.
package callstat

import (
	"strings"

	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// IsMissing reports whether a call found nothing: the account has no such
// service (no mailbox, no OneDrive, ...).
func IsMissing(class string, status int) bool {
	return class == string(graph.NotFound) || (class == string(graph.ClientError) && status == 400)
}

// IsCall reports whether e is one API call of a round (not an event line).
func IsCall(e store.Entry) bool { return e.Event == "" && e.Round != "" }

// CategoryIndex maps an API id to its category.
func CategoryIndex(apis []catalog.API) map[string]string {
	m := make(map[string]string, len(apis))
	for _, a := range apis {
		m[a.ID] = a.Category
	}
	return m
}

// EnabledCategories returns the lower-cased categories an account currently
// runs. A category switched off has old failures in the log but no problem.
func EnabledCategories(apis []catalog.API, accountCategories []string) map[string]bool {
	enabled := map[string]bool{}
	for _, c := range catalog.EffectiveCategories(apis, accountCategories) {
		enabled[strings.ToLower(c)] = true
	}
	return enabled
}

// Spike is the failure rate over the most recent rounds of one account.
type Spike struct {
	Rounds   int     // rounds that went into the numbers (at most the window)
	Calls    int     // calls counted: successes and real failures
	Failures int     // real failures: throttling, 5xx, 401/403, network and other errors
	Rate     float64 // Failures / Calls
	Enough   bool    // the log holds at least a full window of rounds
	Active   bool    // Enough, and the rate and the count reach the thresholds
}

// SpikeOf judges the entries of one account (oldest first). Calls that found
// nothing, cancelled calls and calls of categories the account no longer runs
// are left out of both the failures and the total.
func SpikeOf(entries []store.Entry, apis []catalog.API, accountCategories []string, window int, rate float64, minFailures int) Spike {
	category := CategoryIndex(apis)
	enabled := EnabledCategories(apis, accountCategories)
	type round struct {
		id          string
		calls, fail int
	}
	var rounds []*round
	byID := map[string]*round{}
	for _, e := range entries {
		if !IsCall(e) || e.Class == "" || e.Class == string(graph.Canceled) || IsMissing(e.Class, e.Status) {
			continue
		}
		if cat, known := category[e.API]; known && !enabled[strings.ToLower(cat)] {
			continue
		}
		r := byID[e.Round]
		if r == nil {
			r = &round{id: e.Round}
			byID[e.Round] = r
			rounds = append(rounds, r)
		}
		r.calls++
		if e.Class != string(graph.OK) {
			r.fail++
		}
	}
	var s Spike
	s.Enough = window > 0 && len(rounds) >= window
	if len(rounds) > window && window > 0 {
		rounds = rounds[len(rounds)-window:]
	}
	for _, r := range rounds {
		s.Rounds++
		s.Calls += r.calls
		s.Failures += r.fail
	}
	if s.Calls > 0 {
		s.Rate = float64(s.Failures) / float64(s.Calls)
	}
	s.Active = s.Enough && s.Failures >= minFailures && s.Rate >= rate
	return s
}
