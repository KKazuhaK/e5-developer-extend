package appsetup_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/appsetup"
	"github.com/KKazuhaK/e5-developer-extend/internal/appsetup/fakegraph"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
)

const tenant = "tenant-1111"

type rig struct {
	t        *testing.T
	g        *fakegraph.Server
	progress []string
	sleeps   []time.Duration
	secret   string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return &rig{t: t, g: fakegraph.New(t)}
}

func (r *rig) setup(kind appsetup.Kind) *appsetup.Setup {
	return &appsetup.Setup{
		Kind: kind, Token: fakegraph.Token, TenantID: tenant, Base: r.g.Base(),
		Version: "1.2.3", Hostname: "dev-box",
		Now:      func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) },
		Progress: func(m string) { r.progress = append(r.progress, m) },
		Backoff:  []time.Duration{time.Second, 2 * time.Second, 3 * time.Second},
		Sleep:    func(_ context.Context, d time.Duration) error { r.sleeps = append(r.sleeps, d); return nil },
		StoreSecret: func(s string) error {
			r.secret = s
			return nil
		},
	}
}

func apply(t *testing.T, s *appsetup.Setup, c appsetup.Choice) *appsetup.Result {
	t.Helper()
	res, err := s.Apply(context.Background(), c)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return res
}

func accessIDs(t *testing.T, app map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	rra, _ := app["requiredResourceAccess"].([]any)
	for _, e := range rra {
		m := e.(map[string]any)
		if m["resourceAppId"] != fakegraph.GraphAppID {
			continue
		}
		for _, ra := range m["resourceAccess"].([]any) {
			x := ra.(map[string]any)
			out[x["id"].(string)] = x["type"].(string)
		}
	}
	return out
}

func TestFreshTenantCreatesAppServicePrincipalAndConsent(t *testing.T) {
	r := newRig(t)
	s := r.setup(appsetup.KindDelegated)

	plan, err := s.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Existing != nil || plan.NeedsChoice() || plan.DisplayName != "E5 Developer Extend" ||
		len(plan.Permissions) != len(catalog.RecommendedDelegated) || len(plan.Missing) != len(plan.Permissions) {
		t.Fatalf("plan = %+v", plan)
	}
	codes := []string{}
	for _, a := range plan.Actions {
		codes = append(codes, a.Code)
	}
	if strings.Join(codes, ",") != "create_app,create_service_principal,grant_consent" {
		t.Errorf("actions = %v", codes)
	}
	if len(r.g.Writes()) != 0 {
		t.Fatalf("Plan changed something: %v", r.g.Writes())
	}

	res := apply(t, s, appsetup.Choice{})
	if !res.CreatedApp || !res.CreatedServicePrincipal || !res.ConsentGranted || res.TenantID != tenant || res.ClientID == "" {
		t.Fatalf("result = %+v", res)
	}
	app := r.g.App(res.ObjectID)
	if app["displayName"] != "E5 Developer Extend" || app["signInAudience"] != "AzureADMyOrg" || app["isFallbackPublicClient"] != true {
		t.Errorf("app = %v", app)
	}
	tags := fmt.Sprint(app["tags"])
	if tags != "[e5extend e5extend:delegated]" {
		t.Errorf("tags = %s", tags)
	}
	if !strings.HasPrefix(app["notes"].(string), "Created by E5 Developer Extend 1.2.3 on 2026-11-16.") {
		t.Errorf("notes = %v", app["notes"])
	}
	pc := app["publicClient"].(map[string]any)["redirectUris"].([]any)
	if len(pc) != 1 || pc[0] != "http://localhost" {
		t.Errorf("redirects = %v", pc)
	}
	ids := accessIDs(t, app)
	if len(ids) != len(catalog.RecommendedDelegated) {
		t.Errorf("requiredResourceAccess has %d entries", len(ids))
	}
	for _, typ := range ids {
		if typ != "Scope" {
			t.Errorf("type = %s", typ)
		}
	}
	for _, p := range catalog.RecommendedDelegated {
		if !strings.Contains(" "+r.g.GrantScope()+" ", " "+p+" ") {
			t.Errorf("consent lacks %s", p)
		}
	}
	// The permission IDs come from Graph's own list, by name.
	for _, p := range catalog.RecommendedDelegated {
		if _, ok := ids[r.g.Scopes[p]]; !ok {
			t.Errorf("%s not requested", p)
		}
	}
}

func TestSecondRunCreatesNothing(t *testing.T) {
	r := newRig(t)
	first := apply(t, r.setup(appsetup.KindDelegated), appsetup.Choice{})
	r.g.ResetRequests()

	s := r.setup(appsetup.KindDelegated)
	plan, err := s.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Existing == nil || plan.Existing.ClientID != first.ClientID || len(plan.Missing) != 0 || len(plan.Actions) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	res := apply(t, s, appsetup.Choice{})
	if res.CreatedApp || res.CreatedServicePrincipal || res.UpdatedApp || len(res.AddedPermissions) != 0 ||
		len(res.ConsentAdded) != 0 || !res.ConsentGranted || res.ClientID != first.ClientID {
		t.Fatalf("result = %+v", res)
	}
	if w := r.g.Writes(); len(w) != 0 {
		t.Fatalf("second run wrote: %v", w)
	}
	if len(r.g.Apps) != 1 {
		t.Fatalf("%d apps", len(r.g.Apps))
	}
}

func TestExistingAppOnlyGetsWhatIsMissing(t *testing.T) {
	r := newRig(t)
	userRead := r.g.Scopes["User.Read"]
	extra := r.g.Scopes["Directory.Read.All"]
	id := r.g.AddApp(map[string]any{
		"displayName": "E5 Developer Extend", "tags": []string{"e5extend", "e5extend:delegated", "mine"},
		"notes": "my notes", "signInAudience": "AzureADMultipleOrgs",
		"publicClient":           map[string]any{"redirectUris": []string{"http://localhost", "https://example.com/cb"}},
		"isFallbackPublicClient": true,
		"requiredResourceAccess": []any{
			map[string]any{"resourceAppId": fakegraph.GraphAppID, "resourceAccess": []any{
				map[string]any{"id": userRead, "type": "Scope"}, map[string]any{"id": extra, "type": "Scope"}}},
			map[string]any{"resourceAppId": "other-api", "resourceAccess": []any{map[string]any{"id": "x", "type": "Role"}}},
		},
	})
	clientID := r.g.App(id)["appId"].(string)
	// Service principal and a consent that has an extra scope and lacks most.
	spID := "sp-existing"
	r.g.Do(func() {
		r.g.SPs[clientID] = spID
		r.g.Grants = append(r.g.Grants, map[string]any{"id": "g1", "clientId": spID, "resourceId": r.g.GraphSP,
			"consentType": "AllPrincipals", "scope": "User.Read Directory.Read.All"})
	})

	s := r.setup(appsetup.KindDelegated)
	plan, _ := s.Plan(context.Background())
	if len(plan.Missing) != len(catalog.RecommendedDelegated)-1 {
		t.Errorf("missing = %v", plan.Missing)
	}
	res := apply(t, s, appsetup.Choice{})
	if res.CreatedApp || res.CreatedServicePrincipal || res.Renamed || res.AdoptedApp {
		t.Fatalf("result = %+v", res)
	}
	app := r.g.App(id)
	ids := accessIDs(t, app)
	if ids[userRead] != "Scope" || ids[extra] != "Scope" {
		t.Error("an existing permission was dropped")
	}
	if len(ids) != len(catalog.RecommendedDelegated)+1 {
		t.Errorf("requested %d permissions", len(ids))
	}
	if len(app["requiredResourceAccess"].([]any)) != 2 {
		t.Error("the other API's entry was lost")
	}
	if fmt.Sprint(app["tags"]) != "[e5extend e5extend:delegated mine]" || app["notes"] != "my notes" || app["signInAudience"] != "AzureADMultipleOrgs" {
		t.Errorf("app settings changed: %v", app)
	}
	if len(app["publicClient"].(map[string]any)["redirectUris"].([]any)) != 2 {
		t.Error("a redirect URI was dropped")
	}
	if !strings.Contains(r.g.GrantScope(), "Directory.Read.All") {
		t.Error("an existing consent scope was dropped")
	}
	for _, p := range catalog.RecommendedDelegated {
		if !strings.Contains(" "+r.g.GrantScope()+" ", " "+p+" ") {
			t.Errorf("consent lacks %s", p)
		}
	}
	if r.g.Count("POST /v1.0/oauth2PermissionGrants") != 0 || r.g.Count("PATCH /v1.0/oauth2PermissionGrants/g1") != 1 {
		t.Errorf("consent must be patched, not recreated: %v", r.g.Writes())
	}
	if len(r.g.Apps) != 1 {
		t.Error("created a second app")
	}
}

func TestExistingAppGetsRedirectAndPublicClientFlag(t *testing.T) {
	r := newRig(t)
	id := r.g.AddApp(map[string]any{"displayName": "x", "tags": []string{"e5extend:delegated"}})
	apply(t, r.setup(appsetup.KindDelegated), appsetup.Choice{})
	app := r.g.App(id)
	if app["isFallbackPublicClient"] != true || fmt.Sprint(app["publicClient"]) != "map[redirectUris:[http://localhost]]" {
		t.Errorf("app = %v", app)
	}
	if app["displayName"] != "x" {
		t.Error("a tagged app must not be renamed")
	}
}

func TestAdoptHandMadeApp(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rename bool
	}{{"rename", true}, {"keep name", false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.g.AddApp(map[string]any{"displayName": "E5-Renew"}) // not a known name
			id := r.g.AddApp(map[string]any{"displayName": "e5renew", "tags": []string{"keep"}})
			r.g.AddApp(map[string]any{"displayName": "Unrelated"})
			clientID := r.g.App(id)["appId"].(string)

			s := r.setup(appsetup.KindDelegated)
			plan, err := s.Plan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !plan.NeedsChoice() || len(plan.Candidates) != 1 || plan.Candidates[0].ClientID != clientID {
				t.Fatalf("plan = %+v", plan)
			}
			if _, err := s.Apply(context.Background(), appsetup.Choice{}); !errors.Is(err, appsetup.ErrChoiceRequired) {
				t.Fatalf("Apply without a choice: %v", err)
			}
			if len(r.g.Writes()) != 0 {
				t.Fatalf("wrote before a choice: %v", r.g.Writes())
			}
			// The client ID works as well as the object ID.
			res := apply(t, s, appsetup.Choice{Adopt: clientID, Rename: tc.rename})
			if !res.AdoptedApp || res.CreatedApp || res.Renamed != tc.rename || res.ClientID != clientID {
				t.Fatalf("result = %+v", res)
			}
			app := r.g.App(id)
			want := "e5renew"
			if tc.rename {
				want = "E5 Developer Extend"
			}
			if app["displayName"] != want {
				t.Errorf("name = %v", app["displayName"])
			}
			if fmt.Sprint(app["tags"]) != "[keep e5extend e5extend:delegated]" {
				t.Errorf("tags = %v", app["tags"])
			}
			if len(accessIDs(t, app)) != len(catalog.RecommendedDelegated) || !res.ConsentGranted {
				t.Error("permissions or consent missing")
			}
			if len(r.g.Apps) != 3 {
				t.Errorf("%d apps", len(r.g.Apps))
			}
			// Next time it is found by its tag.
			plan, _ = r.setup(appsetup.KindDelegated).Plan(context.Background())
			if plan.Existing == nil || plan.Existing.ClientID != clientID {
				t.Errorf("not recognised afterwards: %+v", plan)
			}
		})
	}
}

func TestCreateNewDespiteCandidates(t *testing.T) {
	r := newRig(t)
	old := r.g.AddApp(map[string]any{"displayName": "E5 RENEW"})
	res := apply(t, r.setup(appsetup.KindDelegated), appsetup.Choice{CreateNew: true})
	if !res.CreatedApp || res.ObjectID == old || len(r.g.Apps) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := r.g.App(old)["tags"]; ok {
		t.Error("the hand-made app was touched")
	}
}

func TestAdoptRejectsUnknownApp(t *testing.T) {
	r := newRig(t)
	r.g.AddApp(map[string]any{"displayName": "e5extend"})
	r.g.AddApp(map[string]any{"displayName": "Unrelated"})
	other := r.g.Apps[1]["appId"].(string)
	if _, err := r.setup(appsetup.KindDelegated).Apply(context.Background(), appsetup.Choice{Adopt: other}); err == nil {
		t.Fatal("adopted an app that is not a candidate")
	}
}

func TestCandidateListingFollowsPages(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 5; i++ {
		r.g.AddApp(map[string]any{"displayName": fmt.Sprintf("other %d", i)})
	}
	r.g.AddApp(map[string]any{"displayName": "e5extend"})
	r.g.PageSize = 2
	plan, err := r.setup(appsetup.KindDelegated).Plan(context.Background())
	if err != nil || len(plan.Candidates) != 1 {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
}

func TestOtherKindsAppIsNotACandidate(t *testing.T) {
	r := newRig(t)
	r.g.AddApp(map[string]any{"displayName": "E5 Developer Extend (app-only)", "tags": []string{"e5extend", "e5extend:app"}})
	r.g.AddApp(map[string]any{"displayName": "e5extend", "tags": []string{"e5extend", "e5extend:app"}})
	plan, err := r.setup(appsetup.KindDelegated).Plan(context.Background())
	if err != nil || plan.Existing != nil || len(plan.Candidates) != 0 {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
}

func TestReplicationDelayIsRetried(t *testing.T) {
	r := newRig(t)
	r.g.SPNotFound, r.g.GrantNotFound = 2, 2
	s := r.setup(appsetup.KindDelegated)
	res := apply(t, s, appsetup.Choice{})
	if !res.ConsentGranted || !res.CreatedServicePrincipal {
		t.Fatalf("result = %+v", res)
	}
	if len(r.sleeps) != 4 {
		t.Errorf("sleeps = %v", r.sleeps)
	}
	var waiting int
	for _, m := range r.progress {
		if strings.Contains(m, "replicate") {
			waiting++
		}
	}
	if waiting != 4 {
		t.Errorf("progress = %q", r.progress)
	}
	if r.g.Count("POST /v1.0/servicePrincipals") != 3 {
		t.Errorf("service principal attempts: %d", r.g.Count("POST /v1.0/servicePrincipals"))
	}
}

func TestReplicationGivesUp(t *testing.T) {
	r := newRig(t)
	r.g.SPNotFound = 100
	_, err := r.setup(appsetup.KindDelegated).Apply(context.Background(), appsetup.Choice{})
	var ae *appsetup.APIError
	if !errors.As(err, &ae) || ae.Status != 404 {
		t.Fatalf("err = %v", err)
	}
	if len(r.sleeps) != 3 {
		t.Errorf("sleeps = %v", r.sleeps)
	}
	if len(r.g.Apps) != 1 {
		t.Error("the app must be kept so the next run reuses it")
	}
}

func TestCancelWhileWaiting(t *testing.T) {
	r := newRig(t)
	r.g.SPNotFound = 100
	ctx, cancel := context.WithCancel(context.Background())
	s := r.setup(appsetup.KindDelegated)
	s.Sleep = func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }
	if _, err := s.Apply(ctx, appsetup.Choice{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestForbiddenNamesTheRoles(t *testing.T) {
	r := newRig(t)
	r.g.Forbidden["POST /applications"] = true
	_, err := r.setup(appsetup.KindDelegated).Apply(context.Background(), appsetup.Choice{})
	var re *appsetup.RoleError
	if !errors.As(err, &re) || !errors.Is(err, appsetup.ErrInsufficientRole) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"Application Administrator", "Cloud Application Administrator", "docs/user/app-registration.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), fakegraph.Token) {
		t.Error("the token is in the error")
	}
}

func TestConsentFailureKeepsTheApp(t *testing.T) {
	r := newRig(t)
	r.g.Forbidden["POST /oauth2PermissionGrants"] = true
	res := apply(t, r.setup(appsetup.KindDelegated), appsetup.Choice{})
	if res.ConsentGranted || !res.CreatedApp || !res.CreatedServicePrincipal {
		t.Fatalf("result = %+v", res)
	}
	want := "https://login.microsoftonline.com/" + tenant + "/adminconsent?client_id=" + res.ClientID
	if res.AdminConsentURL != want {
		t.Errorf("consent URL = %q", res.AdminConsentURL)
	}
	if !strings.Contains(res.ConsentError, "Privileged Role Administrator") || !strings.Contains(res.ConsentError, "Global Administrator") {
		t.Errorf("consent error = %q", res.ConsentError)
	}
	if len(r.g.Apps) != 1 {
		t.Error("the app was not kept")
	}
}

func TestUnknownPermissionNameIsAnError(t *testing.T) {
	for _, kind := range []appsetup.Kind{appsetup.KindDelegated, appsetup.KindApp} {
		r := newRig(t)
		r.g.RemoveScope("Notes.Read")
		r.g.RemoveRole("Group.Read.All")
		s := r.setup(kind)
		_, err := s.Apply(context.Background(), appsetup.Choice{})
		if !errors.Is(err, appsetup.ErrUnknownPermission) {
			t.Fatalf("%s: err = %v", kind, err)
		}
		want := "Notes.Read"
		if kind == appsetup.KindApp {
			want = "Group.Read.All"
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: message = %v", kind, err)
		}
		if _, err := s.Plan(context.Background()); !errors.Is(err, appsetup.ErrUnknownPermission) {
			t.Errorf("%s: Plan err = %v", kind, err)
		}
		if len(r.g.Writes()) != 0 {
			t.Errorf("%s: changed something before failing: %v", kind, r.g.Writes())
		}
	}
}

func TestApplicationIdentity(t *testing.T) {
	r := newRig(t)
	s := r.setup(appsetup.KindApp)
	res := apply(t, s, appsetup.Choice{})
	if !res.CreatedApp || !res.ConsentGranted || res.DisplayName != "E5 Developer Extend (app-only)" {
		t.Fatalf("result = %+v", res)
	}
	app := r.g.App(res.ObjectID)
	if fmt.Sprint(app["tags"]) != "[e5extend e5extend:app]" {
		t.Errorf("tags = %v", app["tags"])
	}
	if _, ok := app["publicClient"]; ok {
		t.Error("an app-only registration must not get a public client")
	}
	ids := accessIDs(t, app)
	if len(ids) != len(catalog.RecommendedApplication) {
		t.Errorf("%d permissions", len(ids))
	}
	for _, typ := range ids {
		if typ != "Role" {
			t.Errorf("type = %s", typ)
		}
	}
	if len(r.g.Assigned) != len(catalog.RecommendedApplication) {
		t.Errorf("%d role assignments", len(r.g.Assigned))
	}
	if res.SecretName != "e5extend dev-box 2026-11-16" || res.SecretExpires != "2027-11-16" {
		t.Errorf("secret = %q expires %q", res.SecretName, res.SecretExpires)
	}
	end := r.g.Passwords[res.ObjectID][0]["endDateTime"]
	if end != "2027-11-16T12:00:00Z" {
		t.Errorf("endDateTime = %v", end)
	}
	if r.secret == "" || r.secret != r.g.Secrets[0] {
		t.Error("the secret did not reach the sink")
	}
	assertNoSecret(t, r, res)

	// Run again: roles are not assigned twice, the app is not recreated.
	r.g.ResetRequests()
	res2 := apply(t, s, appsetup.Choice{})
	if res2.CreatedApp || len(r.g.Assigned) != len(catalog.RecommendedApplication) || r.g.Count("POST /v1.0/servicePrincipals/"+r.g.GraphSP+"/appRoleAssignedTo") != 0 {
		t.Errorf("second run: %+v, writes %v", res2, r.g.Writes())
	}
	if len(r.g.Apps) != 1 {
		t.Error("created a second app")
	}
}

func TestAppRolesAlreadyAssignedAreSkipped(t *testing.T) {
	r := newRig(t)
	id := r.g.AddApp(map[string]any{"displayName": "x", "tags": []string{"e5extend:app"}})
	clientID := r.g.App(id)["appId"].(string)
	r.g.Do(func() {
		r.g.SPs[clientID] = "sp-x"
		for _, n := range []string{"User.Read.All", "Group.Read.All"} {
			r.g.Assigned = append(r.g.Assigned, map[string]string{"principalId": "sp-x", "resourceId": r.g.GraphSP, "appRoleId": r.g.Roles[n]})
		}
	})
	s := r.setup(appsetup.KindApp)
	plan, _ := s.Plan(context.Background())
	if plan.Existing == nil {
		t.Fatal("not found by tag")
	}
	res := apply(t, s, appsetup.Choice{})
	if got := r.g.Count("POST /v1.0/servicePrincipals/" + r.g.GraphSP + "/appRoleAssignedTo"); got != len(catalog.RecommendedApplication)-2 {
		t.Errorf("%d assignments posted", got)
	}
	if len(res.ConsentAdded) != len(catalog.RecommendedApplication)-2 {
		t.Errorf("added = %v", res.ConsentAdded)
	}
}

func TestSecretNeverLeaks(t *testing.T) {
	r := newRig(t)
	r.g.Forbidden["POST /servicePrincipals/"+r.g.GraphSP+"/appRoleAssignedTo"] = true
	s := r.setup(appsetup.KindApp)
	res := apply(t, s, appsetup.Choice{})
	// Consent failed but the secret was still made and stored.
	if res.ConsentGranted || res.AdminConsentURL == "" || r.secret == "" {
		t.Fatalf("result = %+v", res)
	}
	assertNoSecret(t, r, res)
}

func TestSecretStoreFailureRevokesIt(t *testing.T) {
	r := newRig(t)
	s := r.setup(appsetup.KindApp)
	s.StoreSecret = func(string) error { return errors.New("vault is locked") }
	_, err := s.Apply(context.Background(), appsetup.Choice{})
	if err == nil || !strings.Contains(err.Error(), "vault is locked") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), r.g.Secrets[0]) {
		t.Error("the secret is in the error")
	}
	if r.g.Count("POST /v1.0/applications/") < 2 {
		t.Errorf("secret not revoked: %v", r.g.Writes())
	}
	for _, list := range r.g.Passwords {
		if len(list) != 0 {
			t.Error("a secret nobody holds was left behind")
		}
	}
}

func TestApplicationIdentityNeedsASink(t *testing.T) {
	r := newRig(t)
	s := r.setup(appsetup.KindApp)
	s.StoreSecret = nil
	if _, err := s.Apply(context.Background(), appsetup.Choice{}); err == nil {
		t.Fatal("expected an error")
	}
	if len(r.g.Writes()) != 0 {
		t.Error("changed something")
	}
}

func assertNoSecret(t *testing.T, r *rig, res *appsetup.Result) {
	t.Helper()
	if len(r.g.Secrets) == 0 {
		t.Fatal("no secret was created")
	}
	b, _ := json.Marshal(res)
	hay := string(b) + strings.Join(r.progress, "\n")
	for _, sec := range r.g.Secrets {
		if strings.Contains(hay, sec) {
			t.Errorf("secret %q appears in the result or progress", sec)
		}
	}
	if strings.Contains(hay, fakegraph.Token) {
		t.Error("the token appears in the result or progress")
	}
}
