package callstat

import (
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// Counts are the calls of a period as the status page and the daily summary
// show them.
type Counts struct {
	OK        int
	Failed    int // real failures: 5xx, 401 and other errors
	Throttled int // calls that ended throttled
}

// Tally counts the calls in entries (oldest first) at or after since. A call
// that was throttled and then retried is one call, counted by its last answer,
// so a throttled-then-successful call is only an ok. Calls that found nothing
// or were refused for good (404, 403), network errors and cancelled calls are
// in none of the counts, and neither are the user lookups of application
// identities.
func Tally(entries []store.Entry, since time.Time) Counts {
	type key struct{ round, api string }
	last := map[key]string{}
	var order []key
	for _, e := range entries {
		if !IsCall(e) || e.API == "" || e.API == "users.pick" || e.TS.Before(since) {
			continue
		}
		k := key{e.Round, e.API}
		if _, seen := last[k]; !seen {
			order = append(order, k)
		}
		last[k] = e.Class + "/" + strconv.Itoa(e.Status)
	}
	var c Counts
	for _, k := range order {
		class, st, _ := strings.Cut(last[k], "/")
		status, _ := strconv.Atoi(st)
		switch {
		case class == string(graph.OK):
			c.OK++
		case IsThrottled(class):
			c.Throttled++
		case class == "" || class == string(graph.Network) || class == string(graph.Canceled) || IsMissing(class, status) || IsUnavailable(class):
		default:
			c.Failed++
		}
	}
	return c
}
