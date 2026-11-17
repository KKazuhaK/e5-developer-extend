package callstat

import (
	"fmt"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

var apis = []catalog.API{
	{ID: "user.me", Category: "user", Default: true},
	{ID: "mail.list", Category: "mail", Default: true},
	{ID: "teams.list", Category: "teams"}, // not on by default
}

// calls builds n entries of one round.
func calls(round, api, class string, status, n int) []store.Entry {
	var out []store.Entry
	for i := 0; i < n; i++ {
		out = append(out, store.Entry{Round: round, API: api, Class: class, Status: status})
	}
	return out
}

func rounds(parts ...func(i int) []store.Entry) []store.Entry {
	var out []store.Entry
	for i := 0; i < 3; i++ {
		for _, p := range parts {
			out = append(out, p(i)...)
		}
	}
	return out
}

func TestMissingIsNotAFailure(t *testing.T) {
	if !IsMissing("not_found", 404) || !IsMissing("client_error", 400) || IsMissing("client_error", 409) || IsMissing("throttled", 429) {
		t.Fatal("IsMissing")
	}
}

func TestSpikeIgnoresKnownUnavailable(t *testing.T) {
	// Every round: the mail category finds nothing; user calls succeed.
	es := rounds(
		func(i int) []store.Entry { return calls(fmt.Sprint("r", i), "mail.list", "not_found", 404, 4) },
		func(i int) []store.Entry { return calls(fmt.Sprint("r", i), "user.me", "ok", 200, 2) },
	)
	s := SpikeOf(es, apis, nil, 3, 0.5, 5)
	if s.Active || s.Failures != 0 || s.Calls != 6 || !s.Enough {
		t.Errorf("%+v", s)
	}
}

func TestSpikeIgnoresThrottledAndRefusedCalls(t *testing.T) {
	es := rounds(
		func(i int) []store.Entry { return calls(fmt.Sprint("r", i), "mail.list", "throttled", 429, 3) },
		func(i int) []store.Entry { return calls(fmt.Sprint("r", i), "mail.list", "forbidden", 403, 3) },
		func(i int) []store.Entry { return calls(fmt.Sprint("r", i), "user.me", "ok", 200, 2) },
	)
	s := SpikeOf(es, apis, nil, 3, 0.5, 5)
	if s.Active || s.Failures != 0 || s.Calls != 6 {
		t.Errorf("%+v", s)
	}
	if !IsUnavailable("forbidden") || !IsUnavailable("not_found") || IsUnavailable("auth_failed") || !IsThrottled("throttled") {
		t.Error("class helpers")
	}
	now := time.Unix(1000, 0)
	if n := PausedAPIs(map[string]time.Time{"a": now.Add(time.Hour), "b": now.Add(-time.Hour)}, now); n != 1 {
		t.Errorf("PausedAPIs = %d", n)
	}
}

func TestSpikeCountsRealFailures(t *testing.T) {
	es := rounds(
		func(i int) []store.Entry { return calls(fmt.Sprint("r", i), "user.me", "network", 0, 2) },
		func(i int) []store.Entry { return calls(fmt.Sprint("r", i), "mail.list", "server_error", 503, 1) },
		func(i int) []store.Entry { return calls(fmt.Sprint("r", i), "mail.list", "ok", 200, 1) },
	)
	s := SpikeOf(es, apis, nil, 3, 0.5, 5)
	if !s.Active || s.Failures != 9 || s.Calls != 12 || s.Rounds != 3 {
		t.Errorf("%+v", s)
	}
	// An occasional failure does not reach either threshold.
	es = rounds(func(i int) []store.Entry { return calls(fmt.Sprint("r", i), "user.me", "ok", 200, 5) })
	es = append(es, store.Entry{Round: "r1", API: "user.me", Class: "server_error", Status: 500})
	if s := SpikeOf(es, apis, nil, 3, 0.5, 5); s.Active || s.Failures != 1 {
		t.Errorf("%+v", s)
	}
}

func TestSpikeNeedsAFullWindowAndEnabledCategories(t *testing.T) {
	es := calls("r1", "user.me", "server_error", 500, 8)
	if s := SpikeOf(es, apis, nil, 3, 0.5, 5); s.Active || s.Enough {
		t.Errorf("one round must not be enough: %+v", s)
	}
	// Failures of a category the account does not run are not counted.
	es = rounds(func(i int) []store.Entry { return calls(fmt.Sprint("r", i), "teams.list", "server_error", 500, 3) })
	if s := SpikeOf(es, apis, nil, 3, 0.5, 5); s.Calls != 0 || s.Active {
		t.Errorf("disabled category counted: %+v", s)
	}
	if s := SpikeOf(es, apis, []string{"teams"}, 3, 0.5, 5); !s.Active {
		t.Errorf("enabled category not counted: %+v", s)
	}
}
