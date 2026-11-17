package notify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

func slackTarget(id, url string, c config.Channel) Target {
	c.ID, c.Type = id, config.ChannelSlack
	return Target{Channel: c, Secrets: map[string]string{"url": url}}
}

func routerFor(f *fakeService, targets ...Target) *Router {
	return &Router{Targets: func() ([]Target, error) { return targets, nil }, Configured: func() bool { return len(targets) > 0 },
		Deliverer: testDeliverer(f)}
}

func TestAcceptsMinLevelAndEvents(t *testing.T) {
	warn := config.Channel{MinLevel: config.LevelWarning}
	crit := config.Channel{MinLevel: config.LevelCritical}
	only := config.Channel{Events: []string{FailureSpike, Recovered}}
	for _, tc := range []struct {
		c    config.Channel
		kind string
		want bool
	}{
		{config.Channel{}, Recovered, true},
		{warn, Recovered, false}, {warn, Stale, true}, {warn, NeedsAttention, true},
		{crit, Stale, false}, {crit, NeedsAttention, true}, {crit, AllFailing, true}, {crit, FailureSpike, false},
		{only, FailureSpike, true}, {only, Recovered, true}, {only, NeedsAttention, false},
		{crit, Test, true}, {only, Test, true}, // a test message always goes through
	} {
		if got := Accepts(tc.c, sampleEv(tc.kind)); got != tc.want {
			t.Errorf("%+v %s: got %v", tc.c, tc.kind, got)
		}
	}
}

func TestRouterAppliesFiltersPerChannel(t *testing.T) {
	all, warn, spikeOnly := newFake(t, 200, "ok"), newFake(t, 200, "ok"), newFake(t, 200, "ok")
	r := routerFor(all,
		slackTarget("all", all.URL+"/h", config.Channel{}),
		slackTarget("warn", warn.URL+"/h", config.Channel{MinLevel: config.LevelWarning}),
		slackTarget("spike", spikeOnly.URL+"/h", config.Channel{Events: []string{FailureSpike}}))
	r.Deliverer.HTTP = trustAll(all, warn, spikeOnly)
	for _, kind := range []string{Recovered, FailureSpike} {
		if err := r.Send(context.Background(), sampleEv(kind)); err != nil {
			t.Fatal(err)
		}
	}
	if all.count() != 2 || warn.count() != 1 || spikeOnly.count() != 1 {
		t.Errorf("deliveries: all %d, warn %d, spike %d", all.count(), warn.count(), spikeOnly.count())
	}
}

func TestOneFailingChannelDoesNotBlockTheOthers(t *testing.T) {
	good, bad := newFake(t, 200, "ok"), newFake(t, 500, "")
	r := routerFor(good,
		slackTarget("bad", bad.URL+"/QUERYSECRET", config.Channel{}),
		slackTarget("good", good.URL+"/h", config.Channel{}))
	r.Deliverer.HTTP = trustAll(good, bad)
	if err := r.Send(context.Background(), sampleEv(NeedsAttention)); err != nil {
		t.Fatalf("one channel delivered, so the event counts as sent: %v", err)
	}
	if good.count() != 1 || bad.count() != 1 {
		t.Errorf("good %d, bad %d", good.count(), bad.count())
	}
}

func TestEveryChannelFailingIsAnError(t *testing.T) {
	a, b := newFake(t, 500, ""), newFake(t, 403, "")
	r := routerFor(a, slackTarget("a", a.URL+"/QUERYSECRET", config.Channel{}), slackTarget("b", b.URL+"/QUERYSECRET", config.Channel{}))
	r.Deliverer.HTTP = trustAll(a, b)
	err := r.Send(context.Background(), sampleEv(NeedsAttention))
	if err == nil || !strings.Contains(err.Error(), "a:") || !strings.Contains(err.Error(), "b:") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("got %v", err)
	}
	mustNotContain(t, "error", err.Error(), "QUERYSECRET")
}

func TestNoChannelWantsTheEventIsNotAnError(t *testing.T) {
	f := newFake(t, 200, "ok")
	r := routerFor(f, slackTarget("crit", f.URL+"/h", config.Channel{MinLevel: config.LevelCritical}))
	if err := r.Send(context.Background(), sampleEv(Recovered)); err != nil || f.count() != 0 {
		t.Errorf("err %v, deliveries %d", err, f.count())
	}
}

func TestDigestIsNarrowedPerChannel(t *testing.T) {
	both, crit, secretOnly := newFake(t, 200, "ok"), newFake(t, 200, "ok"), newFake(t, 200, "ok")
	r := routerFor(both,
		slackTarget("both", both.URL+"/h", config.Channel{}),
		slackTarget("crit", crit.URL+"/h", config.Channel{MinLevel: config.LevelCritical}),
		slackTarget("secret", secretOnly.URL+"/h", config.Channel{Events: []string{SecretExpiring}}))
	r.Deliverer.HTTP = trustAll(both, crit, secretOnly)
	if err := r.Send(context.Background(), sampleEv(Digest)); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "full digest", string(both.only(t).Body), "2 alerts", "No successful call")
	if crit.count() != 0 {
		t.Error("a digest of warnings reached a critical-only channel")
	}
	one := string(secretOnly.only(t).Body)
	mustContain(t, "single item", one, "Client secret expiring")
	mustNotContain(t, "single item", one, "alerts were held", "No successful call")
}

func TestSendToIgnoresFiltersAndNamesUnknownChannels(t *testing.T) {
	f := newFake(t, 200, "ok")
	r := routerFor(f, slackTarget("crit", f.URL+"/h", config.Channel{MinLevel: config.LevelCritical}))
	if err := r.SendTo(context.Background(), "crit", sampleEv(Recovered)); err != nil || f.count() != 1 {
		t.Errorf("SendTo: %v %d", err, f.count())
	}
	if err := r.SendTo(context.Background(), "nope", sampleEv(Test)); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("unknown channel: %v", err)
	}
}

func TestRouterEnabled(t *testing.T) {
	if (&Router{}).Enabled() {
		t.Error("no source: enabled")
	}
	if !routerFor(nil, slackTarget("a", "https://x", config.Channel{})).Enabled() {
		t.Error("with a channel: not enabled")
	}
}

func TestRouterRecordsEachResult(t *testing.T) {
	good, bad := newFake(t, 200, "ok"), newFake(t, 403, "")
	r := routerFor(good, slackTarget("good", good.URL+"/h", config.Channel{}), slackTarget("bad", bad.URL+"/h", config.Channel{}))
	r.Deliverer.HTTP = trustAll(good, bad)
	got := map[string]error{}
	var mu sync.Mutex
	r.Record = func(id string, err error) { mu.Lock(); got[id] = err; mu.Unlock() }
	_ = r.Send(context.Background(), sampleEv(NeedsAttention))
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got["good"] != nil || got["bad"] == nil {
		t.Errorf("recorded: %v", got)
	}
}

func TestSplitDigestSettlesTheItemsOfThePartsThatArrived(t *testing.T) {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		fail := n == 2
		mu.Unlock()
		if fail {
			w.WriteHeader(500)
			return
		}
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()
	r := &Router{Targets: func() ([]Target, error) {
		return []Target{{Channel: config.Channel{ID: "wc", Type: config.ChannelWeCom}, Secrets: map[string]string{"url": srv.URL + "/send"}}}, nil
	}, Configured: func() bool { return true }, Deliverer: testDeliverer(nil)}
	r.Deliverer.HTTP = srv.Client()

	ev := bigDigest(40)
	for i := range ev.Items {
		ev.Items[i].Attempt = fixedNow.Add(time.Duration(i) * time.Second)
	}
	err := r.Send(context.Background(), ev)
	var pe *PartialError
	if !errors.As(err, &pe) {
		t.Fatalf("got %v, want a PartialError", err)
	}
	// Part 1 arrived, part 2 failed and nothing after it was tried.
	if len(pe.Delivered) == 0 || len(pe.Delivered) >= 40 {
		t.Fatalf("%d of 40 delivered", len(pe.Delivered))
	}
	for i, it := range pe.Delivered {
		if it.Account != ev.Items[i].Account {
			t.Errorf("delivered item %d is %s", i, it.Account)
		}
	}
	if mu.Lock(); n != 2 {
		t.Errorf("requests: %d", n)
	}
	mu.Unlock()

	// Settling a digest of just those items touches only them.
	st := store.NewState()
	as := &store.AccountState{Notified: map[string]store.Notice{}}
	st.Accounts[ev.Items[0].Account] = as
	ok := ev.Items[0]
	as.Notified[ok.Event] = store.Notice{Pending: true, Tried: ok.Attempt, Msg: "m"}
	if !MarkEvent(st, Event{Event: Digest, Items: pe.Delivered[:1]}) || as.Notified[ok.Event].Pending {
		t.Error("the delivered item was not settled")
	}
}
