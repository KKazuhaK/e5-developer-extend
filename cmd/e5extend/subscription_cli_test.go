package main

import (
	"encoding/json"
	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/subscription"
)

func fakeSubscriptions(t *testing.T, code int, hits *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/directory/subscriptions" || r.Header.Get("Authorization") != "Bearer TOKEN" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.WriteHeader(code)
		if code == 200 {
			_, _ = w.Write([]byte(`{"value":[
{"id":"s1","skuPartNumber":"DEVELOPERPACK_E5","status":"Enabled","nextLifecycleDateTime":"2027-02-14T12:00:00Z","isTrial":false,"totalLicenses":25,"ownerTenantId":"tid"},
{"id":"s2","skuPartNumber":"POWER_BI_STANDARD","status":"Enabled","nextLifecycleDateTime":"2027-01-01T12:00:00Z","isTrial":true,"totalLicenses":1000000,"ownerTenantId":"tid"}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestSubscriptionCommandTableAndJSON(t *testing.T) {
	e := newEnv(t)
	addDelegated(e, "main")
	useAuth(e, &fakeAuth{tok: withOrgRead(goodToken())})
	var hits atomic.Int32
	e.graphBase = fakeSubscriptions(t, 200, &hits)

	out := e.mustRun("", "subscription")
	for _, want := range []string{"TENANT", "t.example", "DEVELOPERPACK_E5", "Enabled", "2027-02-14", "POWER_BI_STANDARD (not watched)", "yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d", hits.Load())
	}
	// --cached makes no call and shows what was saved.
	out = e.mustRun("", "subscription", "--cached")
	if hits.Load() != 1 || !strings.Contains(out, "DEVELOPERPACK_E5") {
		t.Errorf("cached: hits %d\n%s", hits.Load(), out)
	}
	out = e.mustRun("", "subscription", "main", "--cached", "--json")
	var tenants []subscription.TenantView
	if err := json.Unmarshal([]byte(out), &tenants); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(tenants) != 1 || tenants[0].Access != "ok" || len(tenants[0].Subscriptions) != 2 || tenants[0].Subscriptions[0].SKU != "DEVELOPERPACK_E5" || tenants[0].Subscriptions[0].DaysLeft == nil {
		t.Errorf("json = %+v", tenants)
	}
	if strings.Contains(out, "TOKEN") {
		t.Errorf("output leaks the token:\n%s", out)
	}
	if _, _, err := e.run("", "subscription", "nobody"); err == nil {
		t.Error("an unknown account must fail")
	}
}

func TestSubscriptionCommandWithoutPermissionAndManualDate(t *testing.T) {
	e := newEnv(t)
	addDelegated(e, "main")
	useAuth(e, &fakeAuth{tok: withOrgRead(goodToken())})
	var hits atomic.Int32
	e.graphBase = fakeSubscriptions(t, 403, &hits)

	out, errOut, err := e.run("", "subscription")
	if err != nil {
		t.Fatalf("a missing permission is not an error: %v", err)
	}
	for _, want := range []string{"Organization.Read.All is needed", "e5extend app setup", "--set-expires"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s\n%s", want, out, errOut)
		}
	}
	out = e.mustRun("", "subscription", "main", "--set-expires", "2027-02-14", "--cached")
	if !strings.Contains(out, "(entered by hand)") || !strings.Contains(out, "2027-02-14") || strings.Contains(out, "--set-expires YYYY") {
		t.Errorf("manual date:\n%s", out)
	}
	e.mustRun("", "config", "validate") // the file stays valid with the date in it
	if _, _, err := e.run("", "subscription", "main", "--set-expires", "tomorrow"); err == nil {
		t.Error("a bad date must be refused")
	}
	if _, _, err := e.run("", "subscription", "--set-expires", "2027-02-14"); err == nil {
		t.Error("the date needs an account")
	}
	out = e.mustRun("", "subscription", "main", "--clear-expires", "--cached")
	if strings.Contains(out, "(entered by hand)\t") || !strings.Contains(out, "removed") {
		t.Errorf("clear:\n%s", out)
	}
}

func TestSubscriptionCommandHelpListedAndDoctorCheck(t *testing.T) {
	e := newEnv(t)
	addDelegated(e, "main")
	useAuth(e, &fakeAuth{tok: withOrgRead(goodToken())})
	out := e.mustRun("", "subscription", "--help")
	if !strings.Contains(out, "--json") || !strings.Contains(out, "--cached") {
		t.Errorf("help:\n%s", out)
	}
	out, _, _ = e.run("", "doctor", "--offline")
	if !strings.Contains(out, "Subscription t.example") {
		t.Errorf("doctor has no subscription line:\n%s", out)
	}
}

func withOrgRead(t auth.Token) auth.Token {
	t.Scopes = append(t.Scopes, "Organization.Read.All")
	return t
}
