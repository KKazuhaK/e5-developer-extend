package services

import (
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
)

// CatalogService lists the API calls the engine can make.
type CatalogService struct{ b *Backend }

// NewCatalogService returns the service.
func NewCatalogService(b *Backend) *CatalogService { return &CatalogService{b} }

// List returns every call of the catalog. With an account, Usable says which
// the account can use; without one it is false everywhere.
func (s *CatalogService) List(account string) ([]CatalogEntry, error) {
	res, err := s.b.Ops.Catalog(account)
	if err != nil {
		return nil, err
	}
	kind := catalog.Delegated
	if res.Account != nil && !res.Account.Delegated() {
		kind = catalog.App
	}
	out := make([]CatalogEntry, 0, len(res.Entries))
	for _, e := range res.Entries {
		sets := [][]string{}
		if all := e.API.AllScopesFor(kind); len(all) > 0 {
			sets = append(sets, append([]string{}, all...))
		}
		out = append(out, CatalogEntry{
			ID: e.API.ID, Category: e.API.Category, Name: e.API.Name, Usable: e.Usable,
			Scopes:    append([]string{}, e.API.ScopesFor(kind)...),
			ScopeSets: sets,
		})
	}
	return out, nil
}

// Categories returns the API categories an account can choose from.
func (s *CatalogService) Categories() ([]string, error) { return s.b.Ops.Categories() }

// DefaultCategories returns the categories an account without a categories
// setting runs on (the catalog's default-on entries).
func (s *CatalogService) DefaultCategories() ([]string, error) { return s.b.Ops.DefaultCategories() }
