// Package recommend decides which API categories an account can really use. It
// reads the granted permissions, runs a few cheap read-only Graph probes and
// maps their outcome to each category of the catalog. It makes GET requests
// only and keeps no response content.
package recommend

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
)

// Statuses of a category.
const (
	Recommended = "recommended"
	Unavailable = "unavailable"
	Maybe       = "maybe"
)

// Reason codes. A missing permission is "missing_permission:" followed by a
// comma separated scope list.
const (
	ReasonMissingPermission = "missing_permission"
	ReasonNoMailbox         = "no_mailbox"
	ReasonNoOneDrive        = "no_onedrive"
	ReasonNoTeams           = "no_teams_license"
	ReasonNoSharePoint      = "no_sharepoint"
	ReasonNoBookings        = "no_bookings"
	ReasonNeedsAdminRole    = "needs_admin_role"
	ReasonNotSupported      = "not_supported"
	ReasonProbeFailed       = "probe_failed"
	// ReasonCannotProbe: the probe could not run at all. It is followed by what
	// is missing: "cannot_probe:scopes:<A,B>" (none of its APIs is allowed) or
	// "cannot_probe:user" (an application identity could not look up a user).
	ReasonCannotProbe = "cannot_probe"
	// ReasonArbitraryUser: an application identity without a configured user
	// was probed with an arbitrary user, so "absent" proves nothing.
	ReasonArbitraryUser = "probe_arbitrary_user"
)

// Timeout bounds the probes of one recommendation.
const Timeout = 15 * time.Second

// CategoryAdvice is the verdict on one category.
type CategoryAdvice struct {
	Category string `json:"category"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	// Default is true when the category runs for an account without a categories setting.
	Default bool `json:"default"`
}

// Recommendation is the verdict on every category of the catalog.
type Recommendation struct {
	Account    string           `json:"account"`
	Delegated  bool             `json:"delegated"`
	Categories []CategoryAdvice `json:"categories"`
}

// Recommended lists the categories whose status is Recommended.
func (r Recommendation) Recommended() []string {
	out := []string{}
	for _, c := range r.Categories {
		if c.Status == Recommended {
			out = append(out, c.Category)
		}
	}
	return out
}

// HasFailedProbes reports whether some category could not be checked because a
// probe failed (probably temporarily).
func (r Recommendation) HasFailedProbes() bool {
	for _, c := range r.Categories {
		if c.Reason == ReasonProbeFailed {
			return true
		}
	}
	return false
}

// Suggested returns the categories to select: every recommended one, plus
// every "maybe" one the account effectively has now. current is the account's
// categories; empty means the catalog defaults. A "maybe" category is never
// dropped, and an unavailable one is never kept.
func (r Recommendation) Suggested(current []string) []string {
	have := map[string]bool{}
	for _, c := range current {
		have[strings.ToLower(c)] = true
	}
	out := []string{}
	for _, c := range r.Categories {
		switch {
		case c.Status == Recommended:
			out = append(out, c.Category)
		case c.Status == Maybe && ((len(current) == 0 && c.Default) || have[strings.ToLower(c.Category)]):
			out = append(out, c.Category)
		}
	}
	return out
}

// probe is one service check. Its request is the first of APIs (catalog IDs)
// the granted permissions allow. Its outcome decides every category in
// Categories; Absent lists the HTTP statuses that mean "this service is not
// there for the account". Any other failure is not conclusive.
type probe struct {
	Name       string
	APIs       []string
	Categories []string
	Absent     []int
	Reason     string
}

// noQuery are probe APIs that are sent without any query: single small
// objects, and joinedTeams, which does not support OData query parameters (its
// result is only the user's own teams). Every other probe API is a collection
// and is asked for one item with only its id (probeQuery), so a probe never
// downloads bulk data.
var noQuery = map[string]bool{"mail.settings": true, "drive.info": true, "sites.root": true, "teams.joined": true,
	"bookings.businesses": true}

func probeQuery(api catalog.API) url.Values {
	if noQuery[api.ID] {
		return nil
	}
	return url.Values{"$top": {"1"}, "$select": {"id"}}
}

// probes is the only place that says which category depends on which service.
var probes = []probe{
	{
		Name:       "mailbox",
		APIs:       []string{"mail.settings", "mail.folders", "mail.messages", "cal.list", "contacts.list"},
		Categories: []string{"mail", "calendar", "contacts", "tasks", "insights"},
		Absent:     []int{http.StatusNotFound, http.StatusBadRequest},
		Reason:     ReasonNoMailbox,
	},
	{
		Name:       "onedrive",
		APIs:       []string{"drive.info", "drive.root"},
		Categories: []string{"files", "notes"},
		Absent:     []int{http.StatusNotFound, http.StatusBadRequest},
		Reason:     ReasonNoOneDrive,
	},
	{
		Name:       "teams",
		APIs:       []string{"teams.joined"},
		Categories: []string{"teams"},
		// 403 is a policy or permission matter, not proof of a missing license.
		Absent: []int{http.StatusNotFound},
		Reason: ReasonNoTeams,
	},
	{
		Name:       "sharepoint",
		APIs:       []string{"sites.root"},
		Categories: []string{"sites"},
		Absent:     []int{http.StatusNotFound},
		Reason:     ReasonNoSharePoint,
	},
	{
		// The list does not support $top or $select; Bookings may be switched off for the tenant.
		Name:       "bookings",
		APIs:       []string{"bookings.businesses"},
		Categories: []string{"bookings"},
		Absent:     []int{http.StatusNotFound},
		Reason:     ReasonNoBookings,
	},
}

// noProbe lists categories that cannot be probed safely and what they need.
var noProbe = map[string]string{
	"reports": ReasonNeedsAdminRole,
}

// ProbeInfo describes one row of the probe table.
type ProbeInfo struct {
	Name       string
	APIs       []string
	Categories []string
	Reason     string
}

// Probes returns the probe table; tests and documentation read it.
func Probes() []ProbeInfo {
	out := make([]ProbeInfo, 0, len(probes))
	for _, p := range probes {
		out = append(out, ProbeInfo{p.Name, append([]string(nil), p.APIs...), append([]string(nil), p.Categories...), p.Reason})
	}
	return out
}

// Input is everything Run needs.
type Input struct {
	Account string
	Kind    string // catalog.Delegated or catalog.App
	Granted []string
	APIs    []catalog.API
	Graph   *graph.Client
	Token   string
	// Username is the target user of an application identity ("" lets Run pick one).
	Username string
}

type verdict int

const (
	unknown verdict = iota // no probe ran, or it could not tell
	present
	absent
	arbitrary // absent, but for a user picked at random
	cannot    // the probe could not run; see the detail
)

// result is a probe's verdict; detail says what was missing for cannot.
type result struct {
	v      verdict
	detail string
}

// Run probes and returns the verdict on every category of in.APIs.
func Run(ctx context.Context, in Input) Recommendation {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	rec := Recommendation{Account: in.Account, Delegated: in.Kind == catalog.Delegated}
	byID := map[string]catalog.API{}
	var cats []string
	seen := map[string]bool{}
	for _, a := range in.APIs {
		byID[a.ID] = a
		if !seen[a.Category] {
			seen[a.Category] = true
			cats = append(cats, a.Category)
		}
	}

	// Permission check per category, straight from the catalog.
	advice := map[string]*CategoryAdvice{}
	for _, c := range cats {
		advice[c] = &CategoryAdvice{Category: c, Status: Recommended}
		if reason := permissionProblem(in, c); reason != "" {
			advice[c].Status, advice[c].Reason = Unavailable, reason
		}
	}

	// Probes, only for services some still-open category depends on.
	var user userLookup
	user.in = in
	results := make([]result, len(probes))
	var wg sync.WaitGroup
	for i, p := range probes {
		open := false
		for _, c := range p.Categories {
			if a := advice[c]; a != nil && a.Status == Recommended {
				open = true
			}
		}
		if !open {
			continue
		}
		api, ok := pickAPI(in, byID, p)
		if !ok {
			results[i] = result{cannot, "scopes:" + strings.Join(probeScopes(in, byID, p), ",")}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = runProbe(ctx, in, &user, api, p)
		}()
	}
	wg.Wait()

	for i, p := range probes {
		for _, c := range p.Categories {
			a := advice[c]
			if a == nil || a.Status != Recommended {
				continue
			}
			switch results[i].v {
			case cannot:
				a.Status, a.Reason = Maybe, ReasonCannotProbe+":"+results[i].detail
			case absent:
				a.Status, a.Reason = Unavailable, p.Reason
			case arbitrary:
				a.Status, a.Reason = Maybe, ReasonArbitraryUser
			case unknown:
				a.Status, a.Reason = Maybe, ReasonProbeFailed
			}
		}
	}
	for c, reason := range noProbe {
		if a := advice[c]; a != nil && a.Status == Recommended {
			a.Status, a.Reason = Maybe, reason
		}
	}
	defaults := map[string]bool{}
	for _, c := range catalog.DefaultCategories(in.APIs) {
		defaults[c] = true
	}
	for _, c := range cats {
		advice[c].Default = defaults[c]
		rec.Categories = append(rec.Categories, *advice[c])
	}
	return rec
}

// permissionProblem returns "" when the identity may call something of the
// category, else the reason code.
func permissionProblem(in Input, category string) string {
	var inCat []catalog.API
	for _, a := range in.APIs {
		if a.Category == category && a.Path(in.Kind) != "" {
			inCat = append(inCat, a)
		}
	}
	if len(inCat) == 0 {
		return ReasonNotSupported
	}
	if len(catalog.Available(in.APIs, in.Kind, in.Granted, []string{category})) > 0 {
		return ""
	}
	var need []string
	dup := map[string]bool{}
	for _, a := range inCat {
		if s := a.ScopesFor(in.Kind); len(s) > 0 && !dup[s[0]] {
			dup[s[0]] = true
			need = append(need, s[0])
		}
	}
	return ReasonMissingPermission + ":" + strings.Join(need, ",")
}

// probeScopes lists, for a probe none of whose APIs is allowed, the first
// (recommended minimum) scope of each of them.
func probeScopes(in Input, byID map[string]catalog.API, p probe) []string {
	var out []string
	for _, id := range p.APIs {
		a, ok := byID[id]
		if !ok || a.Path(in.Kind) == "" {
			continue
		}
		if sc := a.ScopesFor(in.Kind); len(sc) > 0 && !slices.Contains(out, sc[0]) {
			out = append(out, sc[0])
		}
	}
	return out
}

// pickAPI returns the first API of the probe that the granted permissions allow.
func pickAPI(in Input, byID map[string]catalog.API, p probe) (catalog.API, bool) {
	have := map[string]bool{}
	for _, s := range in.Granted {
		have[strings.ToLower(s)] = true
	}
	for _, id := range p.APIs {
		a, ok := byID[id]
		if !ok || a.Path(in.Kind) == "" {
			continue
		}
		for _, s := range a.ScopesFor(in.Kind) {
			if have[strings.ToLower(s)] {
				return a, true
			}
		}
	}
	return catalog.API{}, false
}

func runProbe(ctx context.Context, in Input, user *userLookup, api catalog.API, p probe) result {
	path := api.Path(in.Kind)
	picked := false
	if api.UsesUser(in.Kind) {
		u, ok := user.get(ctx)
		if !ok {
			return result{cannot, "user"}
		}
		picked = in.Username == ""
		path = strings.ReplaceAll(path, "{user}", url.PathEscape(u))
	}
	res := in.Graph.Call(ctx, in.Token, path, probeQuery(api))
	if res.Class == graph.OK {
		return result{v: present}
	}
	if slices.Contains(p.Absent, res.Status) {
		// A user picked at random says nothing certain about the others.
		if picked {
			return result{v: arbitrary}
		}
		return result{v: absent}
	}
	return result{v: unknown}
}

// userLookup finds the target user of an application identity once.
type userLookup struct {
	in   Input
	once sync.Once
	id   string
}

func (u *userLookup) get(ctx context.Context) (string, bool) {
	u.once.Do(func() {
		if u.in.Username != "" {
			u.id = u.in.Username
			return
		}
		if ids, res := u.in.Graph.ListUserIDs(ctx, u.in.Token); res.Class == graph.OK && len(ids) > 0 {
			u.id = ids[0]
		}
	})
	return u.id, u.id != ""
}
