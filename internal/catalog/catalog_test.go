package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

func mustLoad(t *testing.T) []API {
	t.Helper()
	apis, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return apis
}

func ids(apis []API) string {
	var s []string
	for _, a := range apis {
		s = append(s, a.ID)
	}
	return strings.Join(s, ",")
}

func TestEmbeddedCatalog(t *testing.T) {
	apis := mustLoad(t)
	if len(apis) < 20 {
		t.Fatalf("only %d entries", len(apis))
	}
	byID := map[string]API{}
	for _, a := range apis {
		byID[a.ID] = a
	}
	if byID["drive.info"].App != "" || byID["onenote.notebooks"].App != "" {
		t.Error("drive.info and onenote.notebooks must have no app path")
	}
	if byID["reports.active"].Default {
		t.Error("reports.active must default to off")
	}
	if !byID["mail.messages"].Default {
		t.Error("mail.messages should default to on")
	}
}

func TestParseValidation(t *testing.T) {
	cases := map[string]string{
		"duplicate": "- {id: a, category: c, delegated: /me, scopes: {delegated: [X]}}\n- {id: a, category: c, delegated: /me, scopes: {delegated: [X]}}",
		"slash":     "- {id: a, category: c, delegated: me, scopes: {delegated: [X]}}",
		"identity":  "- {id: a, category: c}",
		"scopes":    "- {id: a, category: c, delegated: /me}",
		"orphan":    "- {id: a, category: c, delegated: /me, scopes: {delegated: [X], app: [Y]}}",
	}
	for name, src := range cases {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestAvailable(t *testing.T) {
	apis := mustLoad(t)

	got := Available(apis, Delegated, []string{"mail.read", "User.Read"}, nil)
	for _, want := range []string{"mail.messages", "user.me", "mail.folders"} {
		if !strings.Contains(ids(got), want) {
			t.Errorf("%s missing from %s", want, ids(got))
		}
	}
	if strings.Contains(ids(got), "cal.list") {
		t.Error("cal.list should need a calendar scope")
	}

	// reports.active is off by default but included when its category is listed.
	if strings.Contains(ids(Available(apis, App, []string{"Reports.Read.All"}, nil)), "reports.active") {
		t.Error("default-off entry selected without category")
	}
	if got := Available(apis, App, []string{"Reports.Read.All"}, []string{"reports"}); ids(got) != "reports.active" {
		t.Errorf("reports category: %s", ids(got))
	}

	// Categories restrict the selection.
	got = Available(apis, Delegated, []string{"Mail.Read", "User.Read"}, []string{"mail"})
	if strings.Contains(ids(got), "user.me") || !strings.Contains(ids(got), "mail.messages") {
		t.Errorf("category filter: %s", ids(got))
	}

	// APIs without an app path never show up for app identities.
	got = Available(apis, App, []string{"Files.Read.All", "Notes.Read.All"}, nil)
	if strings.Contains(ids(got), "drive.info") || strings.Contains(ids(got), "onenote") || !strings.Contains(ids(got), "drive.root") {
		t.Errorf("app filter: %s", ids(got))
	}
	if len(Available(apis, App, nil, nil)) != 0 {
		t.Error("no scopes should give no APIs")
	}
}

var footnoteRe = strings.NewReplacer("¹", "", "²", "", "³", "", "⁴", "", "⁵", "", "⁶", "")

func cleanCell(s string) string {
	s = footnoteRe.Replace(strings.TrimSpace(s))
	if i := strings.Index(s, "（"); i >= 0 { // trailing remark such as "（还需管理员角色）"
		s = s[:i]
	}
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "`"))
}

func docPath(s string) string {
	s = cleanCell(s)
	if s == "—" || s == "" {
		return ""
	}
	if i := strings.Index(s, "?"); i >= 0 {
		s = s[:i]
	}
	return s
}

func docScopes(s string) []string {
	s = cleanCell(s)
	if s == "—" || s == "" || s == "同左" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestDocTableMatchesYAML keeps docs/graph-api-catalog.md and apis.yaml in step.
func TestDocTableMatchesYAML(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "graph-api-catalog.md"))
	if err != nil {
		t.Fatal(err)
	}
	apis := map[string]API{}
	for _, a := range mustLoad(t) {
		apis[a.ID] = a
	}

	seen := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) != 6 {
			continue
		}
		id := cleanCell(cells[1])
		a, ok := apis[id]
		if !ok {
			continue // header row, separator or another table
		}
		seen[id] = true
		if got := cleanCell(cells[0]); got != a.Category {
			t.Errorf("%s: category doc %q yaml %q", id, got, a.Category)
		}
		dp, ap := docPath(cells[2]), docPath(cells[4])
		if ap == "同左" { // "same as the delegated path"
			ap = dp
		}
		if dp != a.Delegated {
			t.Errorf("%s: delegated path doc %q yaml %q", id, dp, a.Delegated)
		}
		if ap != a.App {
			t.Errorf("%s: app path doc %q yaml %q", id, ap, a.App)
		}
		if got := docScopes(cells[3]); !reflect.DeepEqual(got, sorted(a.Scopes.Delegated)) {
			t.Errorf("%s: delegated scopes doc %v yaml %v", id, got, sorted(a.Scopes.Delegated))
		}
		if got := docScopes(cells[5]); !reflect.DeepEqual(got, sorted(a.Scopes.App)) {
			t.Errorf("%s: app scopes doc %v yaml %v", id, got, sorted(a.Scopes.App))
		}
	}
	for id := range apis {
		if !seen[id] {
			t.Errorf("%s is in apis.yaml but not in the doc table", id)
		}
	}
}

func TestRecommendedPermissionsAreUsedByTheCatalog(t *testing.T) {
	apis := mustLoad(t)
	used := map[string]map[string]bool{Delegated: {}, App: {}}
	for _, a := range apis {
		for _, k := range []string{Delegated, App} {
			for _, s := range a.ScopesFor(k) {
				used[k][s] = true
			}
		}
	}
	// Besides the catalog, the subscription expiry check (GET /directory/subscriptions)
	// is a user of this permission, for both identities.
	used[Delegated][SubscriptionPermission], used[App][SubscriptionPermission] = true, true
	lists := map[string][]string{Delegated: RecommendedDelegated, App: RecommendedApplication}
	for _, kind := range []string{Delegated, App} {
		if !slices.Contains(lists[kind], SubscriptionPermission) {
			t.Errorf("the subscription check needs %s, which is not recommended for %s", SubscriptionPermission, kind)
		}
	}
	for kind, list := range lists {
		seen := map[string]bool{}
		for _, p := range list {
			if seen[p] {
				t.Errorf("%s: %s listed twice", kind, p)
			}
			seen[p] = true
			if p == "offline_access" { // structural: makes the sign-in renewable
				continue
			}
			if !used[kind][p] {
				t.Errorf("recommended %s permission %s is used by no API in apis.yaml", kind, p)
			}
		}
	}
}

// "An account without categories runs on the default categories" is only a
// clean statement while a category is entirely default or entirely not.
func TestCategoriesAreAllDefaultOrNone(t *testing.T) {
	apis, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	on, off := map[string]int{}, map[string]int{}
	for _, a := range apis {
		if a.Default {
			on[a.Category]++
		} else {
			off[a.Category]++
		}
	}
	for c := range on {
		if off[c] > 0 {
			t.Errorf("category %q mixes default and non-default entries (%d on, %d off)", c, on[c], off[c])
		}
	}
	if len(DefaultCategories(apis)) != len(on) {
		t.Errorf("DefaultCategories = %v", DefaultCategories(apis))
	}
}
