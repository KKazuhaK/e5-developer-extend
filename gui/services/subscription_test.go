package services

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
)

func TestSubscriptionServiceListRefreshAndSetExpires(t *testing.T) {
	r := newRig(t)
	r.fa.tok.Scopes = append(r.fa.tok.Scopes, "Organization.Read.All")
	if err := r.b.Ops.AddAccount(t.Context(), AccountInput{Name: "main", Auth: config.AuthDeviceCode, Tenant: "t.example", ClientID: testGUID}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(`{"value":[{"id":"s1","skuPartNumber":"DEVELOPERPACK_E5","status":"Enabled","nextLifecycleDateTime":"2027-02-14T12:00:00Z","totalLicenses":25,"ownerTenantId":"tid"}]}`))
	}))
	defer srv.Close()
	r.b.Ops.GraphBase = srv.URL
	s := NewSubscriptionService(r.b)

	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].Access != "unchecked" {
		t.Fatalf("list: %+v %v", list, err)
	}
	res, err := s.Refresh("")
	r.keep(res)
	if err != nil || res.Queued || len(res.Tenants[0].Subscriptions) != 1 || res.Tenants[0].Subscriptions[0].Expires != "2027-02-14" {
		t.Fatalf("refresh: %+v %v", res, err)
	}
	if err := s.SetExpires("main", "2027-03-01"); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List()
	r.keep(list)
	if list[0].ManualExpires != "2027-03-01" || !list[0].ManualRedundant {
		t.Errorf("manual date: %+v", list[0])
	}
	if err := s.SetExpires("main", "next week"); err == nil {
		t.Error("a bad date must be refused")
	}
	if err := s.SetExpires("nobody", "2027-03-01"); err == nil {
		t.Error("an unknown account must be refused")
	}
	for _, j := range r.returned {
		for _, secret := range []string{tokenValue, upn} {
			if strings.Contains(j, secret) {
				t.Errorf("a service result leaks %q: %s", secret, j)
			}
		}
	}
}
