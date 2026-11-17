package ops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
	"github.com/KKazuhaK/e5-developer-extend/internal/subscription"
)

// TenantSubscriptions is the subscription view of one tenant.
type TenantSubscriptions = subscription.TenantView

// refreshWait is how long RefreshSubscriptions waits for a running engine.
const refreshWait = 75 * time.Second

// Subscriptions returns what is known about the subscriptions of the tenants
// of the named account (every tenant when account is empty), from the saved
// state. It makes no network call.
func (o *Ops) Subscriptions(account string) ([]TenantSubscriptions, error) {
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return nil, err
	}
	return o.subscriptionViews(cfg, account)
}

func (o *Ops) subscriptionViews(cfg *config.Config, account string) ([]TenantSubscriptions, error) {
	dataDir, err := paths.DataDir()
	if err != nil {
		return nil, err
	}
	st, _ := store.New(dataDir).LoadState()
	key := ""
	if account != "" {
		acct, err := o.FindAccount(cfg, account)
		if err != nil {
			return nil, err
		}
		key = subscription.KeyOf(st, *acct)
	}
	var out []TenantSubscriptions
	for _, v := range subscription.Build(cfg, st, time.Now()) {
		if key == "" || v.Tenant == key {
			out = append(out, v)
		}
	}
	if out == nil {
		out = []TenantSubscriptions{}
	}
	return out, nil
}

// SubscriptionRefresh is the outcome of RefreshSubscriptions.
type SubscriptionRefresh struct {
	Tenants []TenantSubscriptions `json:"tenants"`
	// Queued is true when a running engine was asked and had not answered yet
	// when the wait ended; the result then shows the earlier state. That also
	// happens when the engine is an older version that does not know this
	// request: restart it (the background service or the tray app).
	Queued bool `json:"queued"`
}

// RefreshSubscriptions reads the subscriptions of the tenants of the named
// account (all tenants when empty) now. A running engine is asked with a
// trigger file and waited for; otherwise the check runs in this process, and
// the alerts it raises are sent like the engine's.
func (o *Ops) RefreshSubscriptions(ctx context.Context, account string) (SubscriptionRefresh, error) {
	cfg, path, err := o.LoadConfig()
	if err != nil {
		return SubscriptionRefresh{}, err
	}
	views, err := o.subscriptionViews(cfg, account)
	if err != nil {
		return SubscriptionRefresh{}, err
	}
	if len(views) == 0 {
		return SubscriptionRefresh{}, errors.New("no accounts configured; add one with: e5extend account add")
	}
	eo, err := o.EngineOptions(cfg, path)
	if err != nil {
		return SubscriptionRefresh{}, err
	}
	var names []string
	if account != "" {
		names = []string{account}
	}

	// Say at once when nothing could answer, instead of waiting for it.
	dataDir := eo.DataDir
	if st, _ := store.New(dataDir).LoadState(); st != nil {
		var stuck []string
		for _, g := range subscription.Group(cfg, st) {
			if account != "" && !slices.ContainsFunc(g.Accounts, func(a config.Account) bool { return a.Name == account }) {
				continue
			}
			if !slices.ContainsFunc(g.Accounts, func(a config.Account) bool { return subscription.Usable(st, a, true) }) {
				stuck = append(stuck, g.Label)
			}
		}
		if len(stuck) > 0 && (account != "" || len(stuck) == len(views)) {
			return SubscriptionRefresh{}, fmt.Errorf("%w (%s): resume the account or sign it in first", engine.ErrNoUsableAccount, strings.Join(stuck, ", "))
		}
	}

	if !engine.Running(eo.DataDir) {
		eo.Notifier = o.Notifier(cfg, path, nil)
		err := engine.CheckSubscriptions(ctx, eo, names)
		if errors.Is(err, engine.ErrRunning) {
			return SubscriptionRefresh{}, errors.New("an engine started meanwhile; try again")
		}
		if err != nil {
			return SubscriptionRefresh{}, err
		}
		tenants, err := o.subscriptionViews(cfg, account)
		return SubscriptionRefresh{Tenants: tenants}, err
	}

	// The engine owns state.json: ask it, and wait for it to answer.
	before := map[string]time.Time{}
	st, _ := store.New(eo.DataDir).LoadState()
	for _, v := range views {
		if st != nil && st.Tenants[v.Tenant] != nil {
			before[v.Tenant] = st.Tenants[v.Tenant].Attempted
		}
		trigger := v.Accounts[0] // a name; the trigger file is named by the key
		if a, ok := cfg.Find(trigger); ok {
			trigger = a.Key()
		}
		if err := engine.TriggerSubscription(eo.DataDir, trigger); err != nil {
			return SubscriptionRefresh{}, err
		}
	}
	wait := o.RefreshWait
	if wait <= 0 {
		wait = refreshWait
	}
	deadline := time.Now().Add(wait)
	for {
		done := true
		if st, _ := store.New(eo.DataDir).LoadState(); st != nil {
			for _, v := range views {
				ts := st.Tenants[v.Tenant]
				if ts == nil || ts.Attempted.Equal(before[v.Tenant]) {
					done = false
				}
			}
		} else {
			done = false
		}
		if done || !time.Now().Before(deadline) {
			tenants, err := o.subscriptionViews(cfg, account)
			return SubscriptionRefresh{Tenants: tenants, Queued: !done}, err
		}
		select {
		case <-ctx.Done():
			return SubscriptionRefresh{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// SetSubscriptionExpires writes the manual end date of the tenant of the named
// account ("" removes it). The date belongs to the tenant: it is written on
// that account and removed from the tenant's other accounts, so they never
// disagree. It does not tell the engine; the caller reloads it.
func (o *Ops) SetSubscriptionExpires(ctx context.Context, account, date string) error {
	if date != "" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return fmt.Errorf("not a YYYY-MM-DD date: %q", date)
		}
	}
	unlock, err := o.LockConfig(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, path, err := o.LoadConfig()
	if err != nil {
		return err
	}
	acct, err := o.FindAccount(cfg, account)
	if err != nil {
		return err
	}
	var st *store.State
	if dir, err := paths.DataDir(); err == nil {
		st, _ = store.New(dir).LoadState()
	}
	key := subscription.KeyOf(st, *acct)
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		switch {
		case a.Name == account:
			a.SubscriptionExpires = date
		case subscription.KeyOf(st, *a) == key:
			a.SubscriptionExpires = ""
		}
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	return cfg.Save(path)
}
