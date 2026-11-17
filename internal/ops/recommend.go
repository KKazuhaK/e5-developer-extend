package ops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
	"github.com/KKazuhaK/e5-developer-extend/internal/recommend"
)

// Recommendation says, per API category, whether the account can use it.
type Recommendation = recommend.Recommendation

// CategoryAdvice is the verdict on one category.
type CategoryAdvice = recommend.CategoryAdvice

// RecommendCategories signs the account in silently and probes which API
// categories it can really use (mailbox, OneDrive, Teams, SharePoint, and the
// granted permissions). It only reads. An account that is not signed in gives
// an error that wraps auth.ErrLoginRequired.
func (o *Ops) RecommendCategories(ctx context.Context, name string) (Recommendation, error) {
	cfg, _, err := o.LoadConfig()
	if err != nil {
		return Recommendation{}, err
	}
	acct, err := o.FindAccount(cfg, name)
	if err != nil {
		return Recommendation{}, err
	}
	au, err := o.Authenticator(cfg, *acct)
	if err != nil {
		return Recommendation{}, WithHint(err, name)
	}
	tctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	tok, err := au.Token(tctx)
	if err != nil {
		return Recommendation{}, WithHint(err, name)
	}
	apis, err := catalog.Load()
	if err != nil {
		return Recommendation{}, err
	}
	kind := catalog.Delegated
	if !acct.Delegated() {
		kind = catalog.App
	}
	rec := recommend.Run(ctx, recommend.Input{
		Account: name, Kind: kind, Granted: tok.Scopes, APIs: apis,
		Graph: &graph.Client{HTTP: auth.NewHTTPClient(cfg.Network.Proxy), Base: o.GraphBase},
		Token: tok.AccessToken, Username: acct.Username,
	})
	if err := ctx.Err(); err != nil {
		return Recommendation{}, err
	}
	return rec, nil
}

// SetCategories replaces the API categories of an account and leaves every
// other field alone. The config is read, changed and saved under one lock, so
// a concurrent change to the same file is not lost. It does not tell the
// engine; the caller reloads it.
func (o *Ops) SetCategories(ctx context.Context, name string, categories []string) error {
	if len(categories) == 0 {
		return errors.New("no categories to set")
	}
	if err := checkCategories(categories); err != nil {
		return err
	}
	found := false
	err := o.updateAccount(ctx, name, func(ac *config.Account) {
		found = true
		ac.Categories = slices.Clone(categories)
	})
	if err != nil {
		return fmt.Errorf("save categories: %w", err)
	}
	if !found {
		return fmt.Errorf("no account named %q", name)
	}
	return nil
}

// DefaultCategories lists the categories an account without a categories
// setting runs on.
func (o *Ops) DefaultCategories() ([]string, error) {
	apis, err := catalog.Load()
	if err != nil {
		return nil, err
	}
	return catalog.DefaultCategories(apis), nil
}
