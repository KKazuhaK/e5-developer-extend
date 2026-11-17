package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
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

// ErrNoUsableAccount means no account of the tenant can sign in now.
var ErrNoUsableAccount = errors.New("no account in this tenant can sign in")

// usableForSubscription lists the accounts of a tenant that can be asked now,
// in configuration order (see subscription.Usable).
func (e *Engine) usableForSubscriptionLocked(accts []config.Account, forced bool) []config.Account {
	var out []config.Account
	for _, a := range accts {
		if subscription.Usable(e.state, a, forced) {
			out = append(out, a)
		}
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
		for _, g := range subscription.Group(e.cfg, e.state) {
			key, forced := g.Key, force[g.Key]
			if e.subRunning[key] {
				continue
			}
			if !forced && !(ready && !now.Before(subscription.NextCheck(e.state.Tenants[key]))) {
				continue
			}
			usable := e.usableForSubscriptionLocked(g.Accounts, forced)
			if len(usable) == 0 {
				if forced {
					e.event("", "subscription_check", "no account of "+g.Label+" can sign in now")
				}
				continue
			}
			if e.subRunning == nil {
				e.subRunning = map[string]bool{}
			}
			e.subRunning[key] = true
			e.wg.Add(1)
			go func() {
				defer e.wg.Done()
				e.guard("subscription check", func() { e.checkTenant(ctx, key, usable, forced) })
			}()
		}
	})
}

// checkTenant reads the subscriptions of one tenant with the token of the
// first account that works, and records the outcome. A refusal counts as
// "no permission" only when every account that could ask was refused: by
// Graph, or by its token lacking the permission, which is not even tried.
// Checks run one at a time, so a tenant that two accounts name differently
// (a domain and a GUID) is found out by the first and read once.
// A forced check asks for a fresh token, so permissions consented to a moment
// ago show up in it.
func (e *Engine) checkTenant(ctx context.Context, key string, accts []config.Account, forced bool) {
	running := key // key may change to the tenant GUID below
	defer e.withLock(func() { delete(e.subRunning, running) })
	e.subMu.Lock()
	defer e.subMu.Unlock()

	var subs []graph.Subscription
	var used string
	refused, ok := false, false
	detail := "could not sign in"
	for _, acct := range accts {
		au, err := e.authFor(acct)
		if err != nil {
			continue
		}
		tctx := e.gateContext(ctx, acct.Key())
		if forced {
			tctx = auth.ForceRefresh(tctx)
		}
		tok, err := au.Token(tctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			continue
		}
		// The token says which tenant this really is: when another group (or
		// the saved state) already knows it, they are one.
		if tid := strings.ToLower(tok.TenantID); tid != "" {
			var skip bool
			e.withLock(func() {
				if e.state == nil {
					return
				}
				// Only this account's own tenant string is resolved: an account
				// that names no tenant (organizations, common) says nothing
				// about another account's, and an alias that exists is not re-pointed.
				switch own, cur := acct.TenantKey(), subscription.KeyOf(e.state, acct); {
				case cur == tid || cur != own:
				default:
					subscription.Merge(e.state, own, tid)
					key = tid
					e.saveLocked()
				}
				// Another group may have read this tenant a moment ago.
				skip = !forced && e.clock.Now().Before(subscription.NextCheck(e.state.Tenants[key]))
			})
			if skip {
				return
			}
		}
		if subscription.MissingPermission(tok.Scopes) {
			refused = true
			continue
		}
		got, res := e.o.Graph.ListSubscriptions(ctx, tok.AccessToken)
		switch res.Class {
		case graph.OK:
			subs, used, ok = got, acct.Key(), true
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
				ts.TenantID = key
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
	configured := map[string]bool{}
	for _, a := range e.cfg.Accounts {
		configured[a.TenantKey()] = true
	}
	for k := range e.state.TenantAliases {
		if !configured[k] {
			delete(e.state.TenantAliases, k)
		}
	}
	keys := map[string]bool{}
	for _, a := range e.cfg.Accounts {
		keys[subscription.KeyOf(e.state, a)] = true
	}
	for k := range e.state.Tenants {
		if !keys[k] {
			delete(e.state.Tenants, k)
		}
	}
	if len(e.state.Tenants) == 0 {
		e.state.Tenants = nil
	}
	if len(e.state.TenantAliases) == 0 {
		e.state.TenantAliases = nil
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

	type job struct {
		g      subscription.Tenant
		usable []config.Account
	}
	var jobs []job
	var stuck []string
	e.withLock(func() {
		for _, g := range subscription.Group(e.cfg, e.state) {
			named := len(names) == 0
			for _, a := range g.Accounts {
				named = named || contains(names, a.Name)
			}
			if !named {
				continue
			}
			if u := e.usableForSubscriptionLocked(g.Accounts, true); len(u) > 0 {
				jobs = append(jobs, job{g, u})
			} else {
				stuck = append(stuck, g.Label)
			}
		}
	})
	if len(stuck) > 0 {
		for _, label := range stuck {
			e.event("", "subscription_check", "no account of "+label+" can sign in now")
		}
		if len(jobs) == 0 {
			return fmt.Errorf("%w (%s): resume the account or sign it in first", ErrNoUsableAccount, strings.Join(stuck, ", "))
		}
	}
	for _, j := range jobs {
		e.checkTenant(ctx, j.g.Key, j.usable, true)
	}
	return nil
}

// handleSubscriptionTrigger turns the trigger file of an account into a forced check.
func (e *Engine) handleSubscriptionTrigger(file string, force map[string]bool) {
	name := strings.TrimSuffix(file, subscriptionSuffix)
	var key string
	e.withLock(func() {
		if a, ok := e.cfg.FindKey(name); ok {
			key = subscription.KeyOf(e.state, *a)
		}
	})
	if key == "" {
		e.event(name, "trigger_ignored", "unknown account")
		return
	}
	force[key] = true
}
