package callstat

import (
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func TestTallyCountsPerCallNotPerAttempt(t *testing.T) {
	t0 := time.Unix(10_000, 0)
	e := func(round, api, class string, status int, ago time.Duration) store.Entry {
		return store.Entry{TS: t0.Add(-ago), Round: round, API: api, Class: class, Status: status}
	}
	es := []store.Entry{
		// throttled twice, then ok: one ok
		e("r1", "a", "throttled", 429, 50*time.Minute), e("r1", "a", "throttled", 429, 49*time.Minute), e("r1", "a", "ok", 200, 48*time.Minute),
		// throttled three times: one throttled call
		e("r1", "b", "throttled", 429, 47*time.Minute), e("r1", "b", "throttled", 429, 46*time.Minute), e("r1", "b", "throttled", 429, 45*time.Minute),
		e("r1", "c", "server_error", 500, 44*time.Minute),
		e("r1", "d", "not_found", 404, 43*time.Minute),
		e("r1", "e", "forbidden", 403, 42*time.Minute),
		e("r1", "f", "network", 0, 41*time.Minute),
		e("r1", "g", "client_error", 400, 40*time.Minute), // found nothing
		e("r1", "users.pick", "server_error", 500, 39*time.Minute),
		e("r0", "old", "server_error", 500, 30*time.Hour), // before the period
		{TS: t0, Account: "x", Event: "round_end", OK: 3}, // an event line
	}
	got := Tally(es, t0.Add(-24*time.Hour))
	if got != (Counts{OK: 1, Failed: 1, Throttled: 1}) {
		t.Errorf("%+v", got)
	}
}
