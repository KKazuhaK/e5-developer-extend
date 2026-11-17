package recommend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
)

func loadAPIs(t *testing.T) []catalog.API {
	t.Helper()
	apis, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	return apis
}

// TestProbeTable keeps the probe table consistent with the catalog: every
// probe API and category exists, and no category is decided by two probes.
func TestProbeTable(t *testing.T) {
	apis := loadAPIs(t)
	ids := map[string]catalog.API{}
	cats := map[string]bool{}
	for _, a := range apis {
		ids[a.ID] = a
		cats[a.Category] = true
	}
	owner := map[string]string{}
	for _, p := range Probes() {
		if len(p.APIs) == 0 || len(p.Categories) == 0 || p.Reason == "" {
			t.Errorf("probe %s is incomplete", p.Name)
		}
		for _, id := range p.APIs {
			a, ok := ids[id]
			if !ok {
				t.Errorf("probe %s: API %q is not in the catalog", p.Name, id)
				continue
			}
			if a.UsesUser(catalog.Delegated) {
				t.Errorf("probe %s: API %q needs {user} for the delegated identity", p.Name, id)
			}
		}
		for _, c := range p.Categories {
			if !cats[c] {
				t.Errorf("probe %s: category %q is not in the catalog", p.Name, c)
			}
			if prev, dup := owner[c]; dup {
				t.Errorf("category %q is decided by both %s and %s", c, prev, p.Name)
			}
			owner[c] = p.Name
		}
	}
	for c := range noProbe {
		if !cats[c] {
			t.Errorf("noProbe category %q is not in the catalog", c)
		}
		if _, dup := owner[c]; dup {
			t.Errorf("category %q is both probed and unprobed", c)
		}
	}
	// The README claims these dependencies; keep them true.
	want := map[string]string{"mail": "mailbox", "calendar": "mailbox", "contacts": "mailbox", "tasks": "mailbox", "files": "onedrive", "notes": "onedrive", "teams": "teams", "sites": "sharepoint"}
	for c, p := range want {
		if owner[c] != p {
			t.Errorf("category %q should be decided by %s, got %q", c, p, owner[c])
		}
	}
}

// fake is a Graph stand-in: status per exact path, default 200.
type fake struct {
	mu     sync.Mutex
	status map[string]int
	hits   []string
	srv    *httptest.Server
}

func newFake(t *testing.T, status map[string]int) *fake {
	f := &fake{status: status}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if r.Method != http.MethodGet {
			t.Errorf("probe used %s %s", r.Method, r.URL.Path)
		}
		if st, ok := f.status[r.URL.Path]; ok {
			w.WriteHeader(st)
			return
		}
		if r.URL.Path == "/users" {
			_, _ = w.Write([]byte(`{"value":[{"id":"u-1"},{"id":"u-2"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) hit(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.hits {
		if h == "GET "+path {
			return true
		}
	}
	return false
}

func (f *fake) input(t *testing.T, kind string, granted []string, user string) Input {
	return Input{Account: "a", Kind: kind, Granted: granted, APIs: loadAPIs(t),
		Graph: &graph.Client{Base: f.srv.URL}, Token: "T", Username: user}
}

func (f *fake) get(r Recommendation, cat string) CategoryAdvice {
	for _, c := range r.Categories {
		if c.Category == cat {
			return c
		}
	}
	return CategoryAdvice{Category: cat, Status: "absent from result"}
}

func allDelegated() []string {
	return append(append([]string{}, catalog.RecommendedDelegated...), "Reports.Read.All", "Application.Read.All", "Domain.Read.All", "Organization.Read.All")
}

func expect(t *testing.T, f *fake, r Recommendation, cat, status, reason string) {
	t.Helper()
	got := f.get(r, cat)
	if got.Status != status || got.Reason != reason {
		t.Errorf("%s = %s/%q, want %s/%q", cat, got.Status, got.Reason, status, reason)
	}
}

func TestFullE5User(t *testing.T) {
	f := newFake(t, nil)
	r := Run(context.Background(), f.input(t, catalog.Delegated, allDelegated(), ""))
	for _, c := range r.Categories {
		switch c.Category {
		case "reports":
			if c.Status != Maybe || c.Reason != ReasonNeedsAdminRole {
				t.Errorf("reports = %+v", c)
			}
		default:
			if c.Status != Recommended || c.Reason != "" {
				t.Errorf("%s = %+v, want recommended", c.Category, c)
			}
		}
	}
	rec := r.Recommended()
	if len(rec) != len(r.Categories)-1 {
		t.Errorf("recommended %v of %d", rec, len(r.Categories))
	}
	if !r.Delegated || r.Account != "a" {
		t.Errorf("header %+v", r)
	}
}

func TestNoMailbox(t *testing.T) {
	f := newFake(t, map[string]int{"/me/mailboxSettings": 404})
	r := Run(context.Background(), f.input(t, catalog.Delegated, allDelegated(), ""))
	for _, c := range []string{"mail", "calendar", "contacts", "tasks"} {
		expect(t, f, r, c, Unavailable, ReasonNoMailbox)
	}
	expect(t, f, r, "files", Recommended, "")
	expect(t, f, r, "teams", Recommended, "")
	expect(t, f, r, "user", Recommended, "")
}

func TestNoOneDrive(t *testing.T) {
	f := newFake(t, map[string]int{"/me/drive": 400})
	r := Run(context.Background(), f.input(t, catalog.Delegated, allDelegated(), ""))
	expect(t, f, r, "files", Unavailable, ReasonNoOneDrive)
	expect(t, f, r, "notes", Unavailable, ReasonNoOneDrive)
	expect(t, f, r, "mail", Recommended, "")
}

func TestNoTeamsAndSites(t *testing.T) {
	f := newFake(t, map[string]int{"/me/joinedTeams": 404, "/sites/root": 404})
	r := Run(context.Background(), f.input(t, catalog.Delegated, allDelegated(), ""))
	expect(t, f, r, "teams", Unavailable, ReasonNoTeams)
	expect(t, f, r, "sites", Unavailable, ReasonNoSharePoint)
}

// A 403 on Teams is a policy matter, not proof of a missing license.
func TestTeamsForbiddenIsMaybe(t *testing.T) {
	f := newFake(t, map[string]int{"/me/joinedTeams": 403})
	r := Run(context.Background(), f.input(t, catalog.Delegated, allDelegated(), ""))
	expect(t, f, r, "teams", Maybe, ReasonProbeFailed)
}

// Only 404 and 400 mean "no mailbox / no OneDrive"; other client errors do not.
func TestOtherClientErrorsAreNotAbsent(t *testing.T) {
	for _, st := range []int{402, 405, 409, 410, 422, 403} {
		f := newFake(t, map[string]int{"/me/mailboxSettings": st, "/me/drive": st})
		r := Run(context.Background(), f.input(t, catalog.Delegated, allDelegated(), ""))
		expect(t, f, r, "mail", Maybe, ReasonProbeFailed)
		expect(t, f, r, "files", Maybe, ReasonProbeFailed)
	}
}

// Collection probes ask for one item with only its id; nothing is downloaded in bulk.
func TestProbesAreSmall(t *testing.T) {
	apis := loadAPIs(t)
	byID := map[string]catalog.API{}
	for _, a := range apis {
		byID[a.ID] = a
	}
	for _, p := range Probes() {
		for _, id := range p.APIs {
			q := probeQuery(byID[id])
			if noQuery[id] {
				if q != nil {
					t.Errorf("%s: sent without a query, got %v", id, q)
				}
				continue
			}
			if q.Get("$select") != "id" {
				t.Errorf("%s: probe must select only id, got %v", id, q)
			}
			if q.Get("$top") != "1" {
				t.Errorf("%s: collection probe must use $top=1, got %v", id, q)
			}
		}
	}
	// And on the wire.
	var got []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	in := Input{Account: "a", Kind: catalog.Delegated, Granted: []string{"Mail.ReadBasic", "Files.Read", "Team.ReadBasic.All"}, APIs: apis, Graph: &graph.Client{Base: srv.URL}, Token: "T"}
	Run(context.Background(), in)
	slices.Sort(got)
	if !slices.Equal(got, []string{"/me/drive?", "/me/joinedTeams?", "/me/mailFolders?$select=id&$top=1"}) {
		t.Errorf("requests: %v", got)
	}
}

func TestMissingPermissionSkipsProbe(t *testing.T) {
	f := newFake(t, nil)
	r := Run(context.Background(), f.input(t, catalog.Delegated, []string{"User.Read"}, ""))
	got := f.get(r, "mail")
	if got.Status != Unavailable || !strings.HasPrefix(got.Reason, ReasonMissingPermission+":") || !strings.Contains(got.Reason, "Mail.ReadBasic") {
		t.Errorf("mail = %+v", got)
	}
	expect(t, f, r, "user", Recommended, "")
	for _, p := range []string{"/me/mailboxSettings", "/me/drive", "/me/joinedTeams", "/sites/root"} {
		if f.hit(p) {
			t.Errorf("probed %s without the permission", p)
		}
	}
}

func TestProbeFallsBackToAnotherAPI(t *testing.T) {
	// No MailboxSettings.Read: the probe uses the next mail API the grants allow.
	f := newFake(t, map[string]int{"/me/mailFolders": 404})
	r := Run(context.Background(), f.input(t, catalog.Delegated, []string{"User.Read", "Mail.ReadBasic", "Files.Read"}, ""))
	expect(t, f, r, "mail", Unavailable, ReasonNoMailbox)
	if f.hit("/me/mailboxSettings") {
		t.Error("probed mailboxSettings without the permission")
	}
}

func TestTransientErrorsGiveMaybe(t *testing.T) {
	for _, st := range []int{429, 500, 503, 401} {
		f := newFake(t, map[string]int{"/me/mailboxSettings": st, "/me/drive": st})
		r := Run(context.Background(), f.input(t, catalog.Delegated, allDelegated(), ""))
		for _, c := range []string{"mail", "calendar", "contacts", "tasks", "files", "notes"} {
			expect(t, f, r, c, Maybe, ReasonProbeFailed)
		}
		expect(t, f, r, "teams", Recommended, "")
	}
}

func TestNetworkErrorGivesMaybe(t *testing.T) {
	f := newFake(t, nil)
	in := f.input(t, catalog.Delegated, allDelegated(), "")
	f.srv.Close()
	r := Run(context.Background(), in)
	expect(t, f, r, "mail", Maybe, ReasonProbeFailed)
	expect(t, f, r, "files", Maybe, ReasonProbeFailed)
}

func TestAppOnlyIdentity(t *testing.T) {
	granted := catalog.RecommendedApplication
	t.Run("named user", func(t *testing.T) {
		f := newFake(t, map[string]int{"/users/bob@x.example/mailboxSettings": 404})
		r := Run(context.Background(), f.input(t, catalog.App, granted, "bob@x.example"))
		expect(t, f, r, "mail", Unavailable, ReasonNoMailbox)
		expect(t, f, r, "files", Recommended, "")
		if f.hit("/users") {
			t.Error("listed users although the account names one")
		}
		if got := f.get(r, "notes"); got.Status != Unavailable || got.Reason != ReasonNotSupported {
			t.Errorf("notes = %+v", got)
		}
	})
	t.Run("picked user is not conclusive", func(t *testing.T) {
		f := newFake(t, map[string]int{"/users/u-1/mailboxSettings": 404})
		r := Run(context.Background(), f.input(t, catalog.App, granted, ""))
		expect(t, f, r, "mail", Maybe, ReasonArbitraryUser)
		expect(t, f, r, "files", Recommended, "")
	})
	t.Run("no user found", func(t *testing.T) {
		f := newFake(t, map[string]int{"/users": 403})
		r := Run(context.Background(), f.input(t, catalog.App, granted, ""))
		expect(t, f, r, "mail", Maybe, ReasonCannotProbe+":user")
	})
	t.Run("full", func(t *testing.T) {
		f := newFake(t, nil)
		r := Run(context.Background(), f.input(t, catalog.App, granted, "bob@x.example"))
		expect(t, f, r, "mail", Recommended, "")
		expect(t, f, r, "teams", Recommended, "")
	})
}

func TestEmptyGrants(t *testing.T) {
	f := newFake(t, nil)
	r := Run(context.Background(), f.input(t, catalog.Delegated, nil, ""))
	if len(r.Recommended()) != 0 {
		t.Errorf("recommended %v with no grants", r.Recommended())
	}
	if len(f.hits) != 0 {
		t.Errorf("made requests: %v", f.hits)
	}
}

func TestSuggestedKeepsMaybeAndDropsUnavailable(t *testing.T) {
	rec := Recommendation{Categories: []CategoryAdvice{
		{Category: "user", Status: Recommended, Default: true},
		{Category: "mail", Status: Unavailable, Reason: ReasonNoMailbox, Default: true},
		{Category: "teams", Status: Maybe, Reason: ReasonProbeFailed, Default: true},
		{Category: "reports", Status: Maybe, Reason: ReasonNeedsAdminRole},
	}}
	// No current setting: the defaults are what runs.
	if got := rec.Suggested(nil); !slices.Equal(got, []string{"user", "teams"}) {
		t.Errorf("defaults: %v", got)
	}
	// An explicit list keeps its maybe categories (any case) and nothing else.
	if got := rec.Suggested([]string{"Reports", "mail"}); !slices.Equal(got, []string{"user", "reports"}) {
		t.Errorf("explicit: %v", got)
	}
	if rec.HasFailedProbes() != true {
		t.Error("probe_failed not noticed")
	}
	rec.Categories[2].Reason = ReasonArbitraryUser
	if rec.HasFailedProbes() {
		t.Error("an arbitrary-user maybe is not a failed probe")
	}
}

func TestDefaultFlagFollowsTheCatalog(t *testing.T) {
	f := newFake(t, nil)
	r := Run(context.Background(), f.input(t, catalog.Delegated, allDelegated(), ""))
	for _, c := range r.Categories {
		if want := c.Category != "reports"; c.Default != want {
			t.Errorf("%s default = %v", c.Category, c.Default)
		}
	}
}

func TestCannotProbeIsNotAFailedProbe(t *testing.T) {
	t.Run("no allowed API", func(t *testing.T) {
		f := newFake(t, nil)
		in := f.input(t, catalog.Delegated, []string{"User.Read", "Mail.ReadBasic"}, "")
		// Take every mailbox probe API away from the catalog view but keep a
		// mail category alive through an extra API.
		var apis []catalog.API
		for _, a := range in.APIs {
			if a.Category == "mail" && slices.Contains(probes[0].APIs, a.ID) {
				continue
			}
			apis = append(apis, a)
		}
		apis = append(apis, catalog.API{ID: "mail.extra", Category: "mail", Name: "x", Delegated: "/me/extra", Scopes: catalog.Scopes{Delegated: []string{"Mail.ReadBasic"}}, Default: true})
		in.APIs = apis
		got := f.get(Run(context.Background(), in), "mail")
		if got.Status != Maybe || !strings.HasPrefix(got.Reason, ReasonCannotProbe+":scopes:") {
			t.Fatalf("mail = %+v", got)
		}
		rec := Recommendation{Categories: []CategoryAdvice{got}}
		if rec.HasFailedProbes() {
			t.Error("cannot_probe must not count as a failed probe")
		}
	})
	t.Run("app-only cannot look up a user", func(t *testing.T) {
		f := newFake(t, map[string]int{"/users": 403})
		r := Run(context.Background(), f.input(t, catalog.App, catalog.RecommendedApplication, ""))
		expect(t, f, r, "mail", Maybe, ReasonCannotProbe+":user")
		if r.HasFailedProbes() {
			t.Error("cannot_probe must not count as a failed probe")
		}
	})
}
