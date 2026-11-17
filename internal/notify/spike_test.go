package notify

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

var testAPIs = []catalog.API{
	{ID: "user.me", Category: "user", Default: true},
	{ID: "mail.list", Category: "mail", Default: true},
}

// callRounds builds three rounds; each has perRound calls of the given class.
func callRounds(account, api, class string, status, perRound int) []store.Entry {
	var out []store.Entry
	for i := 0; i < 3; i++ {
		for j := 0; j < perRound; j++ {
			out = append(out, store.Entry{Account: account, Round: fmt.Sprint(account, i), API: api, Class: class, Status: status})
		}
	}
	return out
}

// spikeRig is a rig that also reads call logs.
type spikeRig struct {
	*rig
	calls map[string][]store.Entry
}

func newSpikeRig() *spikeRig { return &spikeRig{rig: newRig(), calls: map[string][]store.Entry{}} }

func (r *spikeRig) evalNoAck() []Event {
	evs, _ := Evaluate(Input{Now: r.now, Start: r.start, Config: r.cfg, Accounts: []config.Account{r.acct},
		State: r.st, Calls: r.calls, APIs: testAPIs})
	return evs
}

func (r *spikeRig) eval() []Event {
	evs := r.evalNoAck()
	for _, e := range evs {
		MarkEvent(r.st, e)
	}
	return evs
}

func TestOccasionalFailureDoesNotAlert(t *testing.T) {
	r := newSpikeRig()
	r.calls["main"] = append(callRounds("main", "user.me", "ok", 200, 6), store.Entry{Account: "main", Round: "main1", API: "user.me", Class: "server_error", Status: 500})
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("got %v", kinds(evs))
	}
}

func TestOnlyMissingCategoriesDoNotAlert(t *testing.T) {
	r := newSpikeRig()
	r.calls["main"] = append(callRounds("main", "mail.list", "not_found", 404, 5), callRounds("main", "mail.list", "client_error", 400, 3)...)
	r.calls["main"] = append(r.calls["main"], callRounds("main", "user.me", "ok", 200, 1)...)
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("got %v", kinds(evs))
	}
}

func TestFailureSpikeAndRecovery(t *testing.T) {
	r := newSpikeRig()
	r.calls["main"] = append(callRounds("main", "user.me", "throttled", 429, 2), callRounds("main", "mail.list", "server_error", 503, 1)...)
	r.calls["main"] = append(r.calls["main"], callRounds("main", "mail.list", "ok", 200, 1)...)
	evs := r.eval()
	if kinds(evs) != "failure_spike" {
		t.Fatalf("got %v", kinds(evs))
	}
	e := evs[0]
	if e.Account != "main" || e.Failures != 9 || e.Rounds != 3 || e.FailRate < 0.74 || e.FailRate > 0.76 || e.Level() != config.LevelWarning {
		t.Errorf("event: %+v", e)
	}
	// Still failing: announced once.
	r.now = r.now.Add(time.Hour)
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("repeated: %v", kinds(evs))
	}
	// The log recovers: the spike ends and "recovered" names the event it ends.
	r.calls["main"] = callRounds("main", "user.me", "ok", 200, 4)
	r.now = r.now.Add(time.Hour)
	evs = r.eval()
	if kinds(evs) != "recovered" || evs[0].Origin != FailureSpike {
		t.Fatalf("recovery: %+v", evs)
	}
	if kinds(r.eval()) != "" {
		t.Error("recovered twice")
	}
}

func TestSpikeRetryKeepsItsNumbers(t *testing.T) {
	r := newSpikeRig()
	r.calls["main"] = callRounds("main", "user.me", "server_error", 500, 4)
	if evs := r.evalNoAck(); len(evs) != 1 {
		t.Fatalf("got %v", kinds(evs))
	}
	r.now = r.now.Add(RetryAfter + time.Minute)
	evs := r.evalNoAck()
	if len(evs) != 1 || evs[0].Failures != 12 || evs[0].Rounds != 3 || evs[0].FailRate != 1 {
		t.Fatalf("retry lost the numbers: %+v", evs)
	}
}

func TestSpikeNotJudgedWithoutLogOrEnoughRounds(t *testing.T) {
	r := newSpikeRig()
	r.calls["main"] = callRounds("main", "user.me", "server_error", 500, 4)
	r.eval()
	// Too few rounds to tell: what was announced stays and nothing recovers.
	r.calls["main"] = []store.Entry{{Round: "x", API: "user.me", Class: "ok", Status: 200}}
	r.now = r.now.Add(time.Hour)
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("got %v", kinds(evs))
	}
	// The log could not be read at all: same.
	r.calls = nil
	if evs := r.eval(); len(evs) != 0 {
		t.Fatalf("got %v", kinds(evs))
	}
	if _, ok := r.as.Notified[FailureSpike]; !ok {
		t.Error("announcement forgotten")
	}
}

// multi evaluates several accounts.
func multi(r *rig, accts []config.Account, calls map[string][]store.Entry) []Event {
	evs, _ := Evaluate(Input{Now: r.now, Start: r.start, Config: r.cfg, Accounts: accts, State: r.st, Calls: calls, APIs: testAPIs})
	for _, e := range evs {
		MarkEvent(r.st, e)
	}
	return evs
}

func TestAllFailingNeedsTwoAccounts(t *testing.T) {
	r := newRig()
	a := config.Account{Name: "a", Auth: config.AuthClientSecret}
	b := config.Account{Name: "b", Auth: config.AuthClientSecret}
	r.st.Accounts["a"] = &store.AccountState{Status: store.StatusOK, LastSuccess: r.now}
	r.st.Accounts["b"] = &store.AccountState{Status: store.StatusOK, LastSuccess: r.now}
	delete(r.st.Accounts, "main")
	calls := map[string][]store.Entry{"a": callRounds("a", "user.me", "server_error", 500, 3)}

	// One of two failing: not all.
	if k := kinds(multi(r, []config.Account{a, b}, calls)); k != "failure_spike" {
		t.Fatalf("one failing: %q", k)
	}
	// Both failing, one by spike and one by needs_attention.
	r.st.Accounts["b"].Status, r.st.Accounts["b"].Reason = store.StatusNeedsAttention, "login required"
	evs := multi(r, []config.Account{a, b}, calls)
	if !strings.Contains(kinds(evs), "all_failing") {
		t.Fatalf("both failing: %q", kinds(evs))
	}
	for _, e := range evs {
		if e.Event == AllFailing && (e.Account != "" || e.Level() != config.LevelCritical) {
			t.Errorf("all_failing: %+v", e)
		}
	}
	if k := kinds(multi(r, []config.Account{a, b}, calls)); k != "" {
		t.Errorf("repeated: %q", k)
	}
	// It ends once one account works again, and can come back later.
	r.st.Accounts["b"].Status = store.StatusOK
	multi(r, []config.Account{a, b}, calls)
	if _, ok := r.st.Notified[AllFailing]; ok {
		t.Error("all_failing notice kept after the condition ended")
	}
}

func TestAllFailingNotForOneRunningAccount(t *testing.T) {
	r := newRig()
	p := config.Account{Name: "p", Auth: config.AuthClientSecret, Paused: true}
	r.st.Accounts["p"] = &store.AccountState{Status: store.StatusPaused, Reason: "x"}
	r.as.Status, r.as.Reason = store.StatusNeedsAttention, "login required"
	evs := multi(r, []config.Account{r.acct, p}, nil)
	for _, e := range evs {
		if e.Event == AllFailing {
			t.Fatalf("a paused account counted: %v", kinds(evs))
		}
	}
	// Single account: needs_attention only.
	if k := kinds(evs); !strings.Contains(k, "needs_attention") {
		t.Errorf("got %q", k)
	}
}

func TestAllFailingDeliveryIsTrackedGlobally(t *testing.T) {
	r := newRig()
	a := config.Account{Name: "a", Auth: config.AuthClientSecret}
	b := config.Account{Name: "b", Auth: config.AuthClientSecret}
	for _, n := range []string{"a", "b"} {
		r.st.Accounts[n] = &store.AccountState{Status: store.StatusNeedsAttention, Reason: "x"}
	}
	evs, changed := Evaluate(Input{Now: r.now, Start: r.start, Config: r.cfg, Accounts: []config.Account{a, b}, State: r.st})
	if !changed || !strings.Contains(kinds(evs), AllFailing) {
		t.Fatalf("got %v", kinds(evs))
	}
	var g Event
	for _, e := range evs {
		if e.Event == AllFailing {
			g = e
		}
	}
	if !r.st.Notified[AllFailing].Pending || !MarkDelivered(r.st, "", AllFailing, g.Time) || r.st.Notified[AllFailing].Pending {
		t.Error("global delivery not tracked")
	}
}
