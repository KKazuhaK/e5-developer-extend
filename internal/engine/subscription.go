package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
	"github.com/KKazuhaK/e5-developer-extend/internal/subscription"
)

const (
	// subscriptionSuffix marks a trigger file that asks for a subscription check
	// of the tenant of an account, now.
	subscriptionSuffix = ".subscription"
	// subscriptionGrace is how long after the start the first check waits for a
	// round to finish; with no round to wait for (everything paused) it goes ahead.
	subscriptionGrace = 15 * time.Minute
)

// TriggerSubscription asks a running engine to check the subscriptions of the
// tenant of account now.
func TriggerSubscription(dataDir, account string) error {
	return writeTrigger(dataDir, account+subscriptionSuffix)
}

// tenantGroups groups the configured accounts by tenant, in configuration order.
func tenantGroups(cfg *config.Config) (keys []string, groups map[string][]config.Account) {
	groups = map[string][]config.Account{}
	for _, a := range cfg.Accounts {
		k := a.TenantKey()
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], a)
	}
	return keys, groups
}

// usableForSubscription lists the accounts that can sign in now, in
// configuration order. A forced check also tries accounts that need attention:
// the sign-in may work again.
func (e *Engine) usableForSubscriptionLocked(accts []config.Account, forced bool) []config.Account {
	var out []config.Account
	for _, a := range accts {
		as := e.state.Accounts[a.Name]
		if a.Paused || as == nil || as.Status == store.StatusPaused || as.Status == store.StatusPeriodEnded {
			continue
		}
		if as.Status == store.StatusNeedsAttention && !forced {
			continue
		}
		out = append(out, a)
	}
	return out
}

// subscriptionTick starts the checks that are due: once per tenant per day,
// after the first round, without holding up the rounds. force names tenants
// to check now.
func (e *Engine) subscriptionTick(ctx context.Context, now time.Time, force map[string]bool) {
	if e.o.NoSubscriptionCheck {
		return
	}
	e.withLock(func() {
		if e.state == nil {
			return
		}
		ready := e.roundsDone || now.Sub(e.started) >= subscriptionGrace
		keys, groups := tenantGroups(e.cfg)
		for _, key := range keys {
			if e.subRunning[key] {
				continue
			}
			if !force[key] && !(ready && !now.Before(subscription.NextCheck(e.state.Tenants[key]))) {
				continue
			}
			usable := e.usableForSubscriptionLocked(groups[key], force[key])
			if len(usable) == 0 {
				continue
			}
			if e.subRunning == nil {
				e.subRunning = map[string]bool{}
			}
			e.subRunning[key] = true
			e.wg.Add(1)
			go func() {
				defer e.wg.Done()
				e.guard("subscription check", func() { e.checkTenant(ctx, key, usable) })
			}()
		}
	})
}

// checkTenant reads the subscriptions of one tenant with the token of the
// first account that works, and records the outcome. A refusal counts as
// "no permission" only when every account that could ask was refused.
func (e *Engine) checkTenant(ctx context.Context, key string, accts []config.Account) {
	defer e.withLock(func() { delete(e.subRunning, key) })

	var subs []graph.Subscription
	var tid, used string
	refused, ok := false, false
	detail := "could not sign in"
	for _, acct := range accts {
		au, err := e.authFor(acct)
		if err != nil {
			continue
		}
		tok, err := au.Token(e.gateContext(ctx, acct.Name))
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			continue
		}
		got, res := e.o.Graph.ListSubscriptions(ctx, tok.AccessToken)
		switch res.Class {
		case graph.OK:
			subs, tid, used, ok = got, tok.TenantID, acct.Name, true
		case graph.Forbidden, graph.AuthFailed:
			refused = true
			continue
		case graph.Canceled:
			return
		case graph.Throttled:
			detail = "Microsoft Graph is throttling the request"
		case graph.Network:
			detail = "could not reach Microsoft Graph"
		case graph.ClientError:
			detail = fmt.Sprintf("Microsoft Graph answered HTTP %d", res.Status)
			if res.Status/100 == 2 {
				detail = "the subscription list could not be read"
			}
		default:
			detail = fmt.Sprintf("Microsoft Graph answered HTTP %d", res.Status)
		}
		break
	}

	now := e.clock.Now()
	var msg string
	e.withLock(func() {
		if e.state == nil {
			return
		}
		if e.state.Tenants == nil {
			e.state.Tenants = map[string]*store.TenantState{}
		}
		ts := e.state.Tenants[key]
		if ts == nil {
			ts = &store.TenantState{}
			e.state.Tenants[key] = ts
		}
		switch {
		case ok:
			n := subscription.Apply(ts, subs, now)
			if ts.TenantID == "" {
				ts.TenantID = tid
			}
			msg = fmt.Sprintf("read %d subscription(s), %d renewal(s) seen", len(subs), n)
		case refused:
			subscription.NoPermission(ts, now)
			msg = "no permission: Organization.Read.All is missing"
		default:
			subscription.Failed(ts, now, detail)
			msg = fmt.Sprintf("failed (%s); next try in %s", detail, subscription.Backoff(ts.Failures))
		}
		e.saveLocked()
	})
	e.event(used, "subscription_check", msg)
	e.evalNotify()
}

// pruneTenantsLocked forgets the subscription state of tenants that no
// account belongs to any more.
func (e *Engine) pruneTenantsLocked() {
	keys := map[string]bool{}
	for _, a := range e.cfg.Accounts {
		keys[a.TenantKey()] = true
	}
	for k := range e.state.Tenants {
		if !keys[k] {
			delete(e.state.Tenants, k)
		}
	}
	if len(e.state.Tenants) == 0 {
		e.state.Tenants = nil
	}
}

// CheckSubscriptions checks the subscriptions of the tenants of the named
// accounts (all when none are named) in this process, like RunOnce does for
// rounds. It fails with ErrRunning while a background engine holds the lock.
func CheckSubscriptions(ctx context.Context, o Options, names []string) error {
	e, err := New(o)
	if err != nil {
		return err
	}
	release, err := e.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	defer e.closeNotifier()
	e.init()

	_, groups := tenantGroups(e.cfg)
	done := map[string]bool{}
	for _, a := range e.cfg.Accounts {
		key := a.TenantKey()
		if done[key] || (len(names) > 0 && !contains(names, a.Name) && !anyNamed(groups[key], names)) {
			continue
		}
		done[key] = true
		var usable []config.Account
		e.withLock(func() { usable = e.usableForSubscriptionLocked(groups[key], true) })
		if len(usable) == 0 {
			e.event("", "subscription_check", "no account of "+key+" can sign in now")
			continue
		}
		e.checkTenant(ctx, key, usable)
	}
	return nil
}

func anyNamed(accts []config.Account, names []string) bool {
	for _, a := range accts {
		if contains(names, a.Name) {
			return true
		}
	}
	return false
}

// handleSubscriptionTrigger turns the trigger file of an account into a forced check.
func (e *Engine) handleSubscriptionTrigger(file string, force map[string]bool) {
	name := strings.TrimSuffix(file, subscriptionSuffix)
	var key string
	e.withLock(func() {
		if a, ok := e.cfg.Find(name); ok {
			key = a.TenantKey()
		}
	})
	if key == "" {
		e.event(name, "trigger_ignored", "unknown account")
		return
	}
	force[key] = true
}
