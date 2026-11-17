// Package catalog is the embedded list of read-only Microsoft Graph calls the
// engine picks from.
package catalog

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed apis.yaml
var data []byte

// Identity kinds.
const (
	Delegated = "delegated"
	App       = "app"
)

// Scopes lists the any-of permissions per identity kind.
type Scopes struct {
	Delegated []string `yaml:"delegated"`
	App       []string `yaml:"app"`
}

// API is one read-only Graph call.
type API struct {
	ID        string              `yaml:"id"`
	Category  string              `yaml:"category"`
	Name      string              `yaml:"name"`
	Delegated string              `yaml:"delegated"`
	App       string              `yaml:"app"`
	Query     map[string][]string `yaml:"query"`
	Scopes    Scopes              `yaml:"scopes"`
	Default   bool                `yaml:"default"`
}

// Path returns the path for kind, or "" if the API does not support it.
func (a API) Path(kind string) string {
	if kind == App {
		return a.App
	}
	return a.Delegated
}

// ScopesFor returns the any-of scope list for kind.
func (a API) ScopesFor(kind string) []string {
	if kind == App {
		return a.Scopes.App
	}
	return a.Scopes.Delegated
}

// UsesUser reports whether the path for kind needs a {user} value.
func (a API) UsesUser(kind string) bool { return strings.Contains(a.Path(kind), "{user}") }

// Load parses and validates the embedded catalog.
func Load() ([]API, error) { return Parse(data) }

// Parse parses and validates catalog YAML.
func Parse(b []byte) ([]API, error) {
	var apis []API
	if err := yaml.Unmarshal(b, &apis); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	seen := map[string]bool{}
	for i, a := range apis {
		where := fmt.Sprintf("catalog entry %d (%q)", i, a.ID)
		switch {
		case a.ID == "":
			return nil, fmt.Errorf("%s: id required", where)
		case seen[a.ID]:
			return nil, fmt.Errorf("%s: duplicate id", where)
		case a.Category == "":
			return nil, fmt.Errorf("%s: category required", where)
		case a.Delegated == "" && a.App == "":
			return nil, fmt.Errorf("%s: needs at least one of delegated or app", where)
		}
		seen[a.ID] = true
		for _, k := range []string{Delegated, App} {
			p := a.Path(k)
			if p == "" {
				if len(a.ScopesFor(k)) > 0 {
					return nil, fmt.Errorf("%s: %s scopes without a %s path", where, k, k)
				}
				continue
			}
			if !strings.HasPrefix(p, "/") {
				return nil, fmt.Errorf("%s: %s path must start with /", where, k)
			}
			if len(a.ScopesFor(k)) == 0 {
				return nil, fmt.Errorf("%s: %s scopes must not be empty", where, k)
			}
		}
	}
	return apis, nil
}

// Available returns the APIs usable by an identity of the given kind holding
// the granted scopes. An empty categories list selects every default-on entry;
// listed categories include all their entries. The result is sorted by id.
func Available(apis []API, kind string, granted, categories []string) []API {
	have := make(map[string]bool, len(granted))
	for _, s := range granted {
		have[strings.ToLower(s)] = true
	}
	cats := make(map[string]bool, len(categories))
	for _, c := range categories {
		cats[strings.ToLower(c)] = true
	}
	var out []API
	for _, a := range apis {
		if a.Path(kind) == "" {
			continue
		}
		if len(cats) == 0 {
			if !a.Default {
				continue
			}
		} else if !cats[strings.ToLower(a.Category)] {
			continue
		}
		for _, s := range a.ScopesFor(kind) {
			if have[strings.ToLower(s)] {
				out = append(out, a)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// DefaultCategories lists, in catalog order, the categories that have at
// least one default-on entry. An account without a categories setting runs
// on exactly these.
func DefaultCategories(apis []API) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range apis {
		if a.Default && !seen[a.Category] {
			seen[a.Category] = true
			out = append(out, a.Category)
		}
	}
	return out
}

// EffectiveCategories returns the categories an account really runs: its own
// list, or the default categories when the list is empty.
func EffectiveCategories(apis []API, categories []string) []string {
	if len(categories) > 0 {
		return categories
	}
	return DefaultCategories(apis)
}
