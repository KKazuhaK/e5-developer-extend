package recommend

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	f := newFake(t, map[string]int{"/me/joinedTeams": 403, "/sites/root": 404})
	r := Run(context.Background(), f.input(t, catalog.Delegated, allDelegated(), ""))
	expect(t, f, r, "teams", Unavailable, ReasonNoTeams)
	expect(t, f, r, "sites", Unavailable, ReasonNoSharePoint)
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
		expect(t, f, r, "mail", Maybe, ReasonProbeFailed)
		expect(t, f, r, "files", Recommended, "")
	})
	t.Run("no user found", func(t *testing.T) {
		f := newFake(t, map[string]int{"/users": 403})
		r := Run(context.Background(), f.input(t, catalog.App, granted, ""))
		expect(t, f, r, "mail", Maybe, ReasonProbeFailed)
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
