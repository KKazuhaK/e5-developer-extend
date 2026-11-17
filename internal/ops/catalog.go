package ops

import (
	"strings"

	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

// CatalogEntry is one API call of the built-in catalog.
type CatalogEntry struct {
	API catalog.API
	// Usable is true when the account may call it. Without an account it is false.
	Usable bool
	// Availability explains in words why the call is or is not available ("yes", "no (...)").
	Availability string
}

// CatalogList is the catalog, optionally seen from one account.
type CatalogList struct {
	Entries []CatalogEntry
	// NoGrants is true when an account was named but no granted permissions are recorded for it yet.
	NoGrants bool
	Account  *config.Account
}

// Catalog lists the read-only calls the engine can make. With an account it
// says which of them that account can use.
func (o *Ops) Catalog(account string) (CatalogList, error) {
	apis, err := catalog.Load()
	if err != nil {
		return CatalogList{}, err
	}
	var res CatalogList
	if account == "" {
		for _, api := range apis {
			res.Entries = append(res.Entries, CatalogEntry{API: api})
		}
		return res, nil
	}
	cfg, _, err := o.LoadConfig()
	if err != nil {
		return res, err
	}
	acct, err := o.FindAccount(cfg, account)
	if err != nil {
		return res, err
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return res, err
	}
	state, _ := store.New(dataDir).LoadState()
	var granted []string
	if as := state.Accounts[acct.Name]; as != nil {
		granted = as.GrantedScopes
	}
	kind := catalog.Delegated
	if !acct.Delegated() {
		kind = catalog.App
	}
	res.Account = acct
	res.NoGrants = len(granted) == 0
	usable := map[string]bool{}
	for _, api := range catalog.Available(apis, kind, granted, acct.Categories) {
		usable[api.ID] = true
	}
	for _, api := range apis {
		res.Entries = append(res.Entries, CatalogEntry{
			API: api, Usable: usable[api.ID], Availability: availability(api, kind, *acct, usable[api.ID]),
		})
	}
	return res, nil
}

// Categories returns the distinct API categories in catalog order.
func (o *Ops) Categories() ([]string, error) {
	apis, err := catalog.Load()
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, api := range apis {
		if !seen[api.Category] {
			seen[api.Category] = true
			out = append(out, api.Category)
		}
	}
	return out, nil
}

func availability(api catalog.API, kind string, acct config.Account, usable bool) string {
	switch {
	case usable:
		return "yes"
	case api.Path(kind) == "":
		return "no (not supported for this identity)"
	}
	if len(acct.Categories) > 0 {
		found := false
		for _, c := range acct.Categories {
			found = found || strings.EqualFold(c, api.Category)
		}
		if !found {
			return "no (category not selected)"
		}
	} else if !api.Default {
		return "no (off by default; select its category)"
	}
	return "no (missing permission)"
}
