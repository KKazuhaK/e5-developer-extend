package ops

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// canReadSubscriptions gives the fake sign-in the permission the check needs.
func (e *testEnv) canReadSubscriptions() {
	e.fa.tok.Scopes = append(e.fa.tok.Scopes, "Organization.Read.All")
}

func subsServer(t *testing.T, code int, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/directory/subscriptions" {
			w.WriteHeader(404)
			return
		}
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer TOKEN-SECRET" {
			t.Errorf("unexpected token")
		}
		w.WriteHeader(code)
		if code == 200 {
			_, _ = w.Write([]byte(`{"value":[{"id":"s1","skuPartNumber":"DEVELOPERPACK_E5","status":"Enabled","nextLifecycleDateTime":"2027-02-14T12:00:00Z","isTrial":false,"totalLicenses":25,"ownerTenantId":"tid"}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRefreshSubscriptionsInProcess(t *testing.T) {
	e := newTestEnv(t)
	e.canReadSubscriptions()
	e.addDevice(t, "main")
	e.addDevice(t, "second") // same tenant: one call
	var hits atomic.Int32
	e.GraphBase = subsServer(t, 200, &hits).URL

	before, err := e.Subscriptions("")
	if err != nil || len(before) != 1 || before[0].Access != "unchecked" || len(before[0].Accounts) != 2 {
		t.Fatalf("before: %+v %v", before, err)
	}
	res, err := e.RefreshSubscriptions(context.Background(), "")
	if err != nil || res.Queued {
		t.Fatalf("refresh: %+v %v", res, err)
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d, want 1 for one tenant", hits.Load())
	}
	tv := res.Tenants[0]
	if tv.Access != "ok" || len(tv.Subscriptions) != 1 || tv.Subscriptions[0].SKU != "DEVELOPERPACK_E5" ||
		tv.Subscriptions[0].Expires != "2027-02-14" || !tv.Subscriptions[0].Watched || tv.Subscriptions[0].DaysLeft == nil {
		t.Errorf("view = %+v", tv)
	}
	again, _ := e.Subscriptions("second")
	if len(again) != 1 || again[0].Subscriptions[0].Status != "Enabled" {
		t.Errorf("saved state not shown: %+v", again)
	}
	if _, err := e.Subscriptions("nobody"); err == nil {
		t.Error("an unknown account must fail")
	}
	if _, err := e.RefreshSubscriptions(context.Background(), "nobody"); err == nil {
		t.Error("an unknown account must fail")
	}
}

func TestRefreshSubscriptionsWithoutPermission(t *testing.T) {
	e := newTestEnv(t)
	e.canReadSubscriptions()
	e.addDevice(t, "main")
	var hits atomic.Int32
	e.GraphBase = subsServer(t, 403, &hits).URL
	res, err := e.RefreshSubscriptions(context.Background(), "main")
	if err != nil {
		t.Fatal(err) // not an error
	}
	if res.Tenants[0].Access != store.AccessNoPermission || len(res.Tenants[0].Subscriptions) != 0 {
		t.Errorf("view = %+v", res.Tenants[0])
	}
}

func TestRefreshSubscriptionsAsksARunningEngine(t *testing.T) {
	e := newTestEnv(t)
	e.canReadSubscriptions()
	e.addDevice(t, "main")
	e.RefreshWait = 5 * time.Second
	var hits atomic.Int32
	e.GraphBase = subsServer(t, 200, &hits).URL
	release, err := engine.AcquireLock(e.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	// A stand-in for the engine: sees the trigger file, then writes the answer.
	go func() {
		for i := 0; i < 100; i++ {
			ents, _ := os.ReadDir(filepath.Join(e.dataDir, "trigger"))
			for _, de := range ents {
				if strings.HasSuffix(de.Name(), ".subscription") && !strings.Contains(de.Name(), ".tmp") {
					st := store.NewState()
					st.Tenants = map[string]*store.TenantState{"t.example": {Access: store.AccessOK, Attempted: time.Now(), Checked: time.Now(),
						Subscriptions: map[string]*store.SubscriptionState{"s1": {SKU: "DEVELOPERPACK_E5", Status: "Enabled", CheckedAt: time.Now()}}}}
					_ = store.New(e.dataDir).SaveState(st)
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	res, err := e.RefreshSubscriptions(context.Background(), "main")
	if err != nil || res.Queued || len(res.Tenants[0].Subscriptions) != 1 {
		t.Fatalf("refresh: %+v %v", res, err)
	}
	if hits.Load() != 0 {
		t.Error("the process called Graph itself while an engine runs")
	}

	// No answer in time: reported as queued, not as an error.
	e.RefreshWait = 200 * time.Millisecond
	_ = os.Remove(filepath.Join(e.dataDir, "state.json"))
	res, err = e.RefreshSubscriptions(context.Background(), "main")
	if err != nil || !res.Queued {
		t.Errorf("timeout: %+v %v", res, err)
	}
}

func TestSetSubscriptionExpiresIsTenantLevel(t *testing.T) {
	e := newTestEnv(t)
	e.addDevice(t, "a")
	e.addDevice(t, "b")
	ctx := context.Background()
	if err := e.SetSubscriptionExpires(ctx, "a", "2027-02-14"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetSubscriptionExpires(ctx, "b", "2027-03-01"); err != nil { // moves to b
		t.Fatal(err)
	}
	cfg, _, _ := e.LoadConfig()
	if cfg.Accounts[0].SubscriptionExpires != "" || cfg.Accounts[1].SubscriptionExpires != "2027-03-01" {
		t.Errorf("accounts = %+v", cfg.Accounts)
	}
	tv, _ := e.Subscriptions("a")
	if tv[0].ManualExpires != "2027-03-01" || !tv[0].ManualInUse || tv[0].ManualDaysLeft == nil {
		t.Errorf("view = %+v", tv[0])
	}
	if err := e.SetSubscriptionExpires(ctx, "a", "soon"); err == nil {
		t.Error("a bad date must be refused")
	}
	if err := e.SetSubscriptionExpires(ctx, "a", ""); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ = e.LoadConfig()
	if cfg.Accounts[1].SubscriptionExpires != "" {
		t.Errorf("not cleared: %+v", cfg.Accounts)
	}
	if err := e.SetSubscriptionExpires(ctx, "nobody", "2027-01-01"); err == nil {
		t.Error("an unknown account must fail")
	}
}

func TestUpdateAccountKeepsTheManualSubscriptionDate(t *testing.T) {
	e := newTestEnv(t)
	e.addDevice(t, "main")
	if err := e.SetSubscriptionExpires(context.Background(), "main", "2027-02-14"); err != nil {
		t.Fatal(err)
	}
	in, _ := e.GetAccount("main")
	in.Categories = []string{"user"}
	if err := e.UpdateAccount(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ := e.LoadConfig()
	if cfg.Accounts[0].SubscriptionExpires != "2027-02-14" {
		t.Errorf("the date was lost: %+v", cfg.Accounts[0])
	}
}

func TestRefreshSubscriptionsSkipsTheCallWhenTheTokenLacksThePermission(t *testing.T) {
	e := newTestEnv(t) // the fake token has no Organization.Read.All
	e.addDevice(t, "main")
	var hits atomic.Int32
	e.GraphBase = subsServer(t, 200, &hits).URL
	res, err := e.RefreshSubscriptions(context.Background(), "")
	if err != nil || hits.Load() != 0 || res.Tenants[0].Access != store.AccessNoPermission {
		t.Fatalf("hits %d, %+v, %v", hits.Load(), res, err)
	}
	tv := res.Tenants[0]
	if tv.FixAccount != "main" || tv.FixKind != "delegated" || tv.FixCommand != "e5extend app setup --repair --account main" {
		t.Errorf("fix advice: %+v", tv)
	}
}

func TestRefreshSubscriptionsSaysAtOnceWhenNoAccountCanSignIn(t *testing.T) {
	e := newTestEnv(t)
	e.canReadSubscriptions()
	e.addDevice(t, "main")
	if _, err := e.Pause(context.Background(), "main"); err != nil {
		t.Fatal(err)
	}
	release, err := engine.AcquireLock(e.dataDir) // an engine runs: it would never answer
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := time.Now()
	_, err = e.RefreshSubscriptions(context.Background(), "main")
	if !errors.Is(err, engine.ErrNoUsableAccount) || time.Since(start) > time.Second {
		t.Errorf("err = %v after %s", err, time.Since(start))
	}
	// And without an engine.
	release()
	if _, err := e.RefreshSubscriptions(context.Background(), ""); !errors.Is(err, engine.ErrNoUsableAccount) {
		t.Errorf("no engine: %v", err)
	}
}

func TestSubscriptionFixAdviceForAClientSecretAccount(t *testing.T) {
	e := newTestEnv(t)
	in := AccountInput{Name: "app1", Auth: config.AuthClientSecret, Tenant: "contoso.onmicrosoft.com", ClientID: testGUID, Secret: "S"}
	if err := e.AddAccount(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	tv, _ := e.Subscriptions("")
	if tv[0].FixKind != "app" || tv[0].FixCommand != "e5extend app setup --repair --account app1" {
		t.Errorf("fix advice: %+v", tv[0])
	}
}
