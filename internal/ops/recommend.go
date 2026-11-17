package ops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
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
// other field alone. It does not tell the engine; the caller reloads it.
func (o *Ops) SetCategories(ctx context.Context, name string, categories []string) error {
	if len(categories) == 0 {
		return errors.New("no categories to set")
	}
	in, err := o.GetAccount(name)
	if err != nil {
		return err
	}
	in.Categories = categories
	if err := o.UpdateAccount(ctx, in); err != nil {
		return fmt.Errorf("save categories: %w", err)
	}
	return nil
}
