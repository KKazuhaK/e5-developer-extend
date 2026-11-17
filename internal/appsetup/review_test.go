package appsetup_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/appsetup"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
)

func TestTaggedAppIsFoundByTheListingWhenTheTagQueryIsStale(t *testing.T) {
	r := newRig(t)
	first := apply(t, r.setup(appsetup.KindDelegated), appsetup.Choice{})
	r.g.TagQueryEmpty = true
	r.g.ResetRequests()

	s := r.setup(appsetup.KindDelegated)
	plan, err := s.Plan(context.Background())
	if err != nil || plan.Existing == nil || plan.Existing.ClientID != first.ClientID || len(plan.Actions) != 0 {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
	res := apply(t, s, appsetup.Choice{})
	if res.CreatedApp || res.ClientID != first.ClientID || len(r.g.Apps) != 1 || len(r.g.Writes()) != 0 {
		t.Fatalf("result = %+v, writes %v", res, r.g.Writes())
	}
}

func TestTwoTaggedAppsAreAnErrorNotAGuess(t *testing.T) {
	r := newRig(t)
	a := r.g.AddApp(map[string]any{"displayName": "one", "tags": []string{"e5extend:delegated"}})
	b := r.g.AddApp(map[string]any{"displayName": "two", "tags": []string{"E5Extend:Delegated"}})
	idA, idB := r.g.App(a)["appId"].(string), r.g.App(b)["appId"].(string)
	for _, stale := range []bool{false, true} {
		r.g.TagQueryEmpty = stale
		s := r.setup(appsetup.KindDelegated)
		_, perr := s.Plan(context.Background())
		_, aerr := s.Apply(context.Background(), appsetup.Choice{})
		for _, err := range []error{perr, aerr} {
			var me *appsetup.MultipleAppsError
			if !errors.As(err, &me) || !strings.Contains(err.Error(), idA) || !strings.Contains(err.Error(), idB) {
				t.Fatalf("stale=%v: err = %v", stale, err)
			}
		}
	}
	if len(r.g.Writes()) != 0 {
		t.Errorf("wrote: %v", r.g.Writes())
	}
}

func TestPatchMergesIntoTheFreshAppNotTheStaleListing(t *testing.T) {
	r := newRig(t)
	id := r.g.AddApp(map[string]any{
		"displayName": "E5 Developer Extend", "tags": []string{"e5extend", "e5extend:delegated"},
		"isFallbackPublicClient": true,
		"publicClient":           map[string]any{"redirectUris": []string{"https://example.com/cb"}},
		"requiredResourceAccess": []any{
			map[string]any{"resourceAppId": "other-api", "resourceAccess": []any{map[string]any{"id": "x", "type": "Role"}}},
		},
	})
	// The listing is an older copy: it knows neither the redirect URI nor the other API.
	r.g.ListView = func(a map[string]any) map[string]any {
		delete(a, "publicClient")
		delete(a, "requiredResourceAccess")
		return a
	}
	res := apply(t, r.setup(appsetup.KindDelegated), appsetup.Choice{})
	app := r.g.App(id)
	uris := fmt.Sprint(app["publicClient"].(map[string]any)["redirectUris"])
	if uris != "[https://example.com/cb http://localhost]" {
		t.Errorf("redirect URIs = %s", uris)
	}
	rra := app["requiredResourceAccess"].([]any)
	if len(rra) != 2 || rra[0].(map[string]any)["resourceAppId"] != "other-api" {
		t.Errorf("the other API's permission was lost: %v", rra)
	}
	if len(res.AddedPermissions) != len(catalog.RecommendedDelegated) {
		t.Errorf("added = %v", res.AddedPermissions)
	}
}

func TestExistingConsentIsToppedUpWhenCreateSaysItExists(t *testing.T) {
	for _, status := range []int{400, 409} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := newRig(t)
			apply(t, r.setup(appsetup.KindDelegated), appsetup.Choice{})
			r.g.Do(func() { r.g.Grants[0]["scope"] = "User.Read" })
			r.g.ExistsStatus, r.g.GrantListMiss = status, 1
			r.g.ResetRequests()

			res := apply(t, r.setup(appsetup.KindDelegated), appsetup.Choice{})
			if !res.ConsentGranted || len(res.ConsentAdded) != len(catalog.RecommendedDelegated)-1 {
				t.Fatalf("result = %+v", res)
			}
			for _, p := range catalog.RecommendedDelegated {
				if !strings.Contains(" "+r.g.GrantScope()+" ", " "+p+" ") {
					t.Errorf("consent lacks %s", p)
				}
			}
			if r.g.Count("POST /v1.0/oauth2PermissionGrants") != 1 || r.g.Count("PATCH /v1.0/oauth2PermissionGrants/") != 1 || len(r.g.Grants) != 1 {
				t.Errorf("writes: %v", r.g.Writes())
			}
		})
	}
}

func TestRoleAssignmentThatExistsIsSuccess(t *testing.T) {
	for _, status := range []int{400, 409} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := newRig(t)
			apply(t, r.setup(appsetup.KindApp), appsetup.Choice{})
			r.g.ExistsStatus, r.g.RoleListMiss = status, 1
			res := apply(t, r.setup(appsetup.KindApp), appsetup.Choice{})
			if !res.ConsentGranted || len(res.ConsentAdded) != 0 || len(r.g.Assigned) != len(catalog.RecommendedApplication) {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}

func TestAdoptWithATaggedAppAlreadyThere(t *testing.T) {
	r := newRig(t)
	tagged := r.g.AddApp(map[string]any{"displayName": "E5 Developer Extend", "tags": []string{"e5extend", "e5extend:delegated"}})
	hand := r.g.AddApp(map[string]any{"displayName": "e5renew"})
	taggedClient, handClient := r.g.App(tagged)["appId"].(string), r.g.App(hand)["appId"].(string)

	_, err := r.setup(appsetup.KindDelegated).Apply(context.Background(), appsetup.Choice{Adopt: handClient})
	if err == nil || !strings.Contains(err.Error(), taggedClient) || !strings.Contains(err.Error(), "drop --adopt") {
		t.Fatalf("err = %v", err)
	}
	if len(r.g.Writes()) != 0 {
		t.Fatalf("wrote: %v", r.g.Writes())
	}
	// Naming the tagged app itself is just a reuse.
	res := apply(t, r.setup(appsetup.KindDelegated), appsetup.Choice{Adopt: taggedClient})
	if res.AdoptedApp || res.CreatedApp || res.ClientID != taggedClient {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := r.g.App(hand)["tags"]; ok {
		t.Error("the hand-made app was touched")
	}
}

func TestAdoptWithNewIsAlwaysAnError(t *testing.T) {
	r := newRig(t)
	hand := r.g.AddApp(map[string]any{"displayName": "e5renew"})
	_, err := r.setup(appsetup.KindDelegated).Apply(context.Background(),
		appsetup.Choice{Adopt: r.g.App(hand)["appId"].(string), CreateNew: true})
	if err == nil || len(r.g.Writes()) != 0 {
		t.Fatalf("err = %v, writes %v", err, r.g.Writes())
	}
}

// writeMatchers says which writes each action code stands for.
var writeMatchers = map[string]*regexp.Regexp{
	"create_app":               regexp.MustCompile(`^POST /v1.0/applications$`),
	"rename_app":               regexp.MustCompile(`^PATCH /v1.0/applications/`),
	"set_notes":                regexp.MustCompile(`^PATCH /v1.0/applications/`),
	"add_tags":                 regexp.MustCompile(`^PATCH /v1.0/applications/`),
	"add_redirect_uri":         regexp.MustCompile(`^PATCH /v1.0/applications/`),
	"set_public_client":        regexp.MustCompile(`^PATCH /v1.0/applications/`),
	"add_permissions":          regexp.MustCompile(`^PATCH /v1.0/applications/`),
	"create_service_principal": regexp.MustCompile(`^POST /v1.0/servicePrincipals$`),
	"grant_consent":            regexp.MustCompile(`^(POST /v1.0/oauth2PermissionGrants$|PATCH /v1.0/oauth2PermissionGrants/)`),
	"assign_roles":             regexp.MustCompile(`^POST /v1.0/servicePrincipals/[^/]+/appRoleAssignedTo$`),
	"create_secret":            regexp.MustCompile(`^POST /v1.0/applications/[^/]+/(addPassword|removePassword)$`),
}

func TestApplyWritesNothingThePlanDidNotList(t *testing.T) {
	type scenario struct {
		name   string
		kind   appsetup.Kind
		mutate func(r *rig, app map[string]any)
		want   []string // codes the plan must contain
	}
	userRead := func(r *rig) string { return r.g.Scopes["User.Read"] }
	scenarios := []scenario{
		{"fresh delegated", appsetup.KindDelegated, nil, []string{"create_app", "create_service_principal", "grant_consent"}},
		{"complete delegated", appsetup.KindDelegated, func(*rig, map[string]any) {}, nil},
		{"missing common tag", appsetup.KindDelegated, func(_ *rig, a map[string]any) { a["tags"] = []any{"e5extend:delegated"} }, []string{"add_tags"}},
		{"public client flag off", appsetup.KindDelegated, func(_ *rig, a map[string]any) { a["isFallbackPublicClient"] = false }, []string{"set_public_client"}},
		{"redirect missing", appsetup.KindDelegated, func(_ *rig, a map[string]any) { delete(a, "publicClient") }, []string{"add_redirect_uri"}},
		{"permission missing", appsetup.KindDelegated, func(r *rig, a map[string]any) {
			e := a["requiredResourceAccess"].([]any)[0].(map[string]any)
			var keep []any
			for _, ra := range e["resourceAccess"].([]any) {
				if ra.(map[string]any)["id"] != userRead(r) {
					keep = append(keep, ra)
				}
			}
			e["resourceAccess"] = keep
		}, []string{"add_permissions"}},
		{"service principal gone", appsetup.KindDelegated, func(r *rig, a map[string]any) {
			delete(r.g.SPs, a["appId"].(string))
			r.g.Grants = nil
		}, []string{"create_service_principal", "grant_consent"}},
		{"consent short", appsetup.KindDelegated, func(r *rig, _ map[string]any) { r.g.Grants[0]["scope"] = "User.Read" }, []string{"grant_consent"}},
		{"fresh app-only", appsetup.KindApp, nil, []string{"create_app", "create_service_principal", "assign_roles", "create_secret"}},
		{"complete app-only", appsetup.KindApp, func(*rig, map[string]any) {}, []string{"create_secret"}},
		{"app-only roles missing", appsetup.KindApp, func(r *rig, _ map[string]any) { r.g.Assigned = r.g.Assigned[:2] }, []string{"assign_roles", "create_secret"}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			r := newRig(t)
			if sc.mutate != nil {
				apply(t, r.setup(sc.kind), appsetup.Choice{}) // build the full state first
				r.g.Do(func() { sc.mutate(r, r.g.Apps[0]) })
			}
			r.g.ResetRequests()
			s := r.setup(sc.kind)
			plan, err := s.Plan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(r.g.Writes()) != 0 {
				t.Fatalf("Plan wrote: %v", r.g.Writes())
			}
			var codes []string
			for _, a := range plan.Actions {
				codes = append(codes, a.Code)
			}
			for _, w := range sc.want {
				found := false
				for _, c := range codes {
					found = found || c == w
				}
				if !found {
					t.Errorf("plan %v lacks %s", codes, w)
				}
			}
			apply(t, s, appsetup.Choice{})
			writes := r.g.Writes()
			if len(plan.Actions) == 0 && len(writes) != 0 {
				t.Fatalf("empty plan but writes %v", writes)
			}
			covered := map[string]bool{}
			for _, w := range writes {
				ok := false
				for _, c := range codes {
					if m := writeMatchers[c]; m != nil && m.MatchString(w) {
						ok, covered[c] = true, true
					}
				}
				if !ok {
					t.Errorf("write %q is not in the plan %v", w, codes)
				}
			}
			for _, c := range codes {
				if !covered[c] {
					t.Errorf("plan promised %s but nothing was written (%v)", c, writes)
				}
			}
		})
	}
}

func TestPlanForAdoptionNamesTheChanges(t *testing.T) {
	r := newRig(t)
	r.g.AddApp(map[string]any{"displayName": "e5renew"})
	plan, err := r.setup(appsetup.KindDelegated).Plan(context.Background())
	if err != nil || len(plan.Actions) != 1 || plan.Actions[0].Code != "adopt_or_create" {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
	for _, w := range []string{"tags", "note", "renames"} {
		if !strings.Contains(plan.Actions[0].Text, w) {
			t.Errorf("text lacks %q: %s", w, plan.Actions[0].Text)
		}
	}
}

func TestAdoptionPatchIsListedByTheActions(t *testing.T) {
	r := newRig(t)
	id := r.g.AddApp(map[string]any{"displayName": "e5renew"})
	r.g.ResetRequests()
	res := apply(t, r.setup(appsetup.KindDelegated), appsetup.Choice{Adopt: id, Rename: true})
	if !res.AdoptedApp || !res.Renamed || r.g.Count("PATCH /v1.0/applications/") != 1 {
		t.Fatalf("result = %+v, writes %v", res, r.g.Writes())
	}
	if r.g.App(id)["notes"] == nil {
		t.Error("notes not set")
	}
}

func TestOnlyANewObjectsNotFoundIsRetried(t *testing.T) {
	r := newRig(t)
	apply(t, r.setup(appsetup.KindDelegated), appsetup.Choice{})
	r.g.Do(func() { r.g.Grants = nil })
	r.g.GrantNotFound = 1
	r.sleeps = nil
	res := apply(t, r.setup(appsetup.KindDelegated), appsetup.Choice{})
	if res.ConsentGranted || len(r.sleeps) != 0 {
		t.Fatalf("a 404 on an old app's consent was retried: %+v, sleeps %v", res, r.sleeps)
	}
	if !strings.Contains(res.ConsentError, "404") {
		t.Errorf("consent error = %q", res.ConsentError)
	}
}

func TestSecretExpiresIsTheUTCDateThatWasSent(t *testing.T) {
	r := newRig(t)
	s := r.setup(appsetup.KindApp)
	s.Now = func() time.Time { return time.Date(2026, 11, 16, 23, 30, 0, 0, time.FixedZone("PST", -8*3600)) }
	res := apply(t, s, appsetup.Choice{})
	end := fmt.Sprint(r.g.Passwords[res.ObjectID][0]["endDateTime"])
	if end != "2027-11-17T07:30:00Z" || res.SecretExpires != "2027-11-17" {
		t.Errorf("endDateTime %s, SecretExpires %s", end, res.SecretExpires)
	}
	if res.SecretName != "e5extend dev-box acct 2026-11-16" {
		t.Errorf("name = %s", res.SecretName)
	}
}

func TestConsentRoleMessageNamesTheRightRoles(t *testing.T) {
	r := newRig(t)
	r.g.Forbidden["POST /oauth2PermissionGrants"] = true
	res := apply(t, r.setup(appsetup.KindDelegated), appsetup.Choice{})
	for _, want := range []string{"Cloud Application Administrator", "Application Administrator"} {
		if !strings.Contains(res.ConsentError, want) {
			t.Errorf("delegated: %q lacks %s", res.ConsentError, want)
		}
	}

	r = newRig(t)
	r.g.Forbidden["POST /servicePrincipals/"+r.g.GraphSP+"/appRoleAssignedTo"] = true
	res = apply(t, r.setup(appsetup.KindApp), appsetup.Choice{})
	if !strings.Contains(res.ConsentError, "Privileged Role Administrator") || strings.Contains(res.ConsentError, "Application Administrator") {
		t.Errorf("app-only: %q", res.ConsentError)
	}
}

func credFor(key, name string) map[string]any {
	return map[string]any{"keyId": key, "displayName": name, "endDateTime": "2027-01-01T00:00:00Z", "secretText": "old"}
}

func TestEarlierSecretsOfThisAccountAreRemovedAfterTheNewOneIsStored(t *testing.T) {
	r := newRig(t)
	id := r.g.AddApp(map[string]any{"displayName": "x", "tags": []string{"e5extend:app"}})
	r.g.Do(func() {
		r.g.Passwords[id] = []map[string]any{
			credFor("k-ours-1", "e5extend dev-box acct 2026-01-01"),
			credFor("k-ours-2", "e5extend dev-box acct 2026-06-30"),
			credFor("k-other-account", "e5extend dev-box acct2 2026-01-01"),
			credFor("k-other-host", "e5extend dev-box-2 acct 2026-01-01"),
			credFor("k-suffix", "e5extend dev-box acct 2026-01-01 manual"),
			credFor("k-mine", "my own secret"),
			credFor("k-legacy", "e5extend dev-box 2026-01-01"),
		}
	})

	// A failing store removes nothing old and drops the new secret.
	s := r.setup(appsetup.KindApp)
	s.StoreSecret = func(string) error { return errors.New("vault is locked") }
	if _, err := s.Apply(context.Background(), appsetup.Choice{}); err == nil {
		t.Fatal("expected an error")
	}
	if got := len(r.g.PasswordNames(id)); got != 7 {
		t.Fatalf("%d secrets after a failed store: %v", got, r.g.PasswordNames(id))
	}

	// Without a sole owner the old per-host secret is only reported.
	res := apply(t, r.setup(appsetup.KindApp), appsetup.Choice{})
	names := r.g.PasswordNames(id)
	if len(names) != 6 || names[res.SecretKeyID] != res.SecretName {
		t.Fatalf("secrets = %v", names)
	}
	for _, gone := range []string{"k-ours-1", "k-ours-2"} {
		if _, ok := names[gone]; ok {
			t.Errorf("%s was not removed", gone)
		}
	}
	for _, kept := range []string{"k-other-account", "k-other-host", "k-suffix", "k-mine", "k-legacy"} {
		if _, ok := names[kept]; !ok {
			t.Errorf("%s was removed", kept)
		}
	}
	if fmt.Sprint(res.RemovedSecrets) != "[k-ours-1 k-ours-2]" || fmt.Sprint(res.NotRemovedSecrets) != "[k-legacy]" {
		t.Errorf("removed %v, not removed %v", res.RemovedSecrets, res.NotRemovedSecrets)
	}

	// With the account as the sole user of the app the old format goes too.
	s = r.setup(appsetup.KindApp)
	s.LegacyOwner = func(appsetup.AppRef) bool { return true }
	res2 := apply(t, s, appsetup.Choice{})
	names = r.g.PasswordNames(id)
	if _, ok := names["k-legacy"]; ok || len(res2.NotRemovedSecrets) != 0 {
		t.Errorf("legacy secret kept: %v, not removed %v", names, res2.NotRemovedSecrets)
	}
	if _, ok := names[res2.SecretKeyID]; !ok || fmt.Sprint(res2.RemovedSecrets) != fmt.Sprintf("[k-legacy %s]", res.SecretKeyID) && fmt.Sprint(res2.RemovedSecrets) != fmt.Sprintf("[%s k-legacy]", res.SecretKeyID) {
		t.Errorf("second run: %v removed %v", names, res2.RemovedSecrets)
	}
}

func TestTwoAccountsSharingOneAppKeepTheirOwnSecrets(t *testing.T) {
	r := newRig(t)
	id := r.g.AddApp(map[string]any{"displayName": "x", "tags": []string{"e5extend:app"}})
	run := func(account string) *appsetup.Result {
		s := r.setup(appsetup.KindApp)
		s.Account = account
		return apply(t, s, appsetup.Choice{})
	}
	a1, b1 := run("a"), run("b")
	if a1.SecretName != "e5extend dev-box a 2026-11-16" || b1.SecretName != "e5extend dev-box b 2026-11-16" {
		t.Fatalf("names %q %q", a1.SecretName, b1.SecretName)
	}
	a2 := run("a")
	names := r.g.PasswordNames(id)
	if _, ok := names[b1.SecretKeyID]; !ok {
		t.Error("rerunning a removed b's secret")
	}
	if _, ok := names[a1.SecretKeyID]; ok || len(names) != 2 || fmt.Sprint(a2.RemovedSecrets) != fmt.Sprintf("[%s]", a1.SecretKeyID) {
		t.Errorf("secrets %v, removed %v", names, a2.RemovedSecrets)
	}
}

func TestRevokeFailureSaysTheSecretIsStillLive(t *testing.T) {
	r := newRig(t)
	id := r.g.AddApp(map[string]any{"displayName": "x", "tags": []string{"e5extend:app"}})
	r.g.Forbidden["POST /applications/"+id+"/removePassword"] = true
	s := r.setup(appsetup.KindApp)
	s.StoreSecret = func(string) error { return errors.New("vault is locked") }
	_, err := s.Apply(context.Background(), appsetup.Choice{})
	if err == nil {
		t.Fatal("expected an error")
	}
	key := ""
	for k := range r.g.PasswordNames(id) {
		key = k
	}
	for _, want := range []string{"vault is locked", "still live in Entra", key, "e5extend dev-box acct 2026-11-16"} {
		if want == "" || !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), r.g.Secrets[0]) {
		t.Error("the secret value is in the error")
	}
}

func TestBeforeSecretRefusesBeforeAnySecretExists(t *testing.T) {
	r := newRig(t)
	s := r.setup(appsetup.KindApp)
	s.BeforeSecret = func(appsetup.AppRef) error { return errors.New("not this account") }
	if _, err := s.Apply(context.Background(), appsetup.Choice{}); err == nil || !strings.Contains(err.Error(), "not this account") {
		t.Fatalf("err = %v", err)
	}
	if len(r.g.Secrets) != 0 || r.g.Count("POST /v1.0/servicePrincipals") != 0 {
		t.Errorf("went on: %v", r.g.Writes())
	}
}

// What PlanFor says about a choice is what Apply then writes for that choice.
func TestApplyWritesWhatPlanForListed(t *testing.T) {
	for _, kind := range []appsetup.Kind{appsetup.KindDelegated, appsetup.KindApp} {
		for _, tc := range []struct {
			name   string
			choice func(clientID string) appsetup.Choice
		}{
			{"adopt and rename", func(c string) appsetup.Choice { return appsetup.Choice{Adopt: c, Rename: true} }},
			{"adopt keep name", func(c string) appsetup.Choice { return appsetup.Choice{Adopt: c} }},
			{"create new", func(string) appsetup.Choice { return appsetup.Choice{CreateNew: true} }},
		} {
			t.Run(string(kind)+"/"+tc.name, func(t *testing.T) {
				r := newRig(t)
				id := r.g.AddApp(map[string]any{"displayName": "e5renew"})
				choice := tc.choice(r.g.App(id)["appId"].(string))
				s := r.setup(kind)
				plan, err := s.PlanFor(context.Background(), &choice)
				if err != nil {
					t.Fatal(err)
				}
				if len(r.g.Writes()) != 0 {
					t.Fatalf("PlanFor wrote: %v", r.g.Writes())
				}
				var codes []string
				for _, a := range plan.Actions {
					codes = append(codes, a.Code)
				}
				apply(t, s, choice)
				covered := map[string]bool{}
				for _, w := range r.g.Writes() {
					ok := false
					for _, c := range codes {
						if m := writeMatchers[c]; m != nil && m.MatchString(w) {
							ok, covered[c] = true, true
						}
					}
					if !ok {
						t.Errorf("write %q is not in the plan %v", w, codes)
					}
				}
				for _, c := range codes {
					if writeMatchers[c] != nil && !covered[c] {
						t.Errorf("plan promised %s but nothing was written (%v)", c, r.g.Writes())
					}
				}
			})
		}
	}
}
