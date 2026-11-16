package engine

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-renew/internal/auth"
	"github.com/KKazuhaK/e5-renew/internal/catalog"
	"github.com/KKazuhaK/e5-renew/internal/config"
	"github.com/KKazuhaK/e5-renew/internal/graph"
	"github.com/KKazuhaK/e5-renew/internal/store"
)

type outcome struct {
	calls, ok, failed int
	throttled         bool
	netStop           bool   // a network error cut the round short
	skipped           string // whole round skipped: network, needs_attention
	attention         string // set when the account needs the user
	lastFail          graph.Class
	panicked          bool
	aborted           bool // context cancelled
}

// round runs one round for acct in the calling goroutine. The caller has set
// the account's running flag and added to e.wg.
func (e *Engine) round(ctx context.Context, acct config.Account, manual bool) {
	defer e.wg.Done()
	defer func() {
		e.mu.Lock()
		if rt := e.rt[acct.Name]; rt != nil {
			rt.running = false
		}
		e.mu.Unlock()
	}()

	roundID := fmt.Sprintf("%08x", e.intn(1<<32))
	if manual {
		// An explicit run retries a failed user lookup.
		e.withLock(func() {
			if rt := e.rt[acct.Name]; rt != nil {
				rt.userFail = ""
			}
		})
	}
	var out outcome
	func() {
		defer func() {
			if r := recover(); r != nil {
				e.log.Error("round panicked", "account", acct.Name, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
				out = outcome{failed: 1, panicked: true, lastFail: "panic"}
			}
		}()
		out = e.execute(ctx, acct, roundID)
	}()
	if out.aborted || ctx.Err() != nil {
		return
	}
	// A panic while booking the result must not take the process down either.
	e.guard("finish "+acct.Name, func() { e.finish(acct, roundID, out) })
}

func (e *Engine) finish(acct config.Account, roundID string, out outcome) {
	now := e.clock.Now()
	var skipped, msg string
	var follow []string // events to log after unlocking
	found := true
	e.withLock(func() {
		as := e.state.Accounts[acct.Name]
		if as == nil { // removed from the config meanwhile
			found = false
			return
		}
		skipped, msg, follow = e.bookLocked(as, acct, out, now)
		e.saveLocked()
	})
	if !found {
		return
	}

	e.emit(store.Entry{TS: now, Account: acct.Name, Round: roundID, Event: "round_end",
		OK: out.ok, Failed: out.failed, Skipped: skipped, Message: msg})
	for _, ev := range follow {
		e.event(acct.Name, ev, msg)
	}
	e.log.Info("round finished", "account", acct.Name, "ok", out.ok, "failed", out.failed, "skipped", skipped)
}

// bookLocked records the outcome of a round in the account's state.
func (e *Engine) bookLocked(as *store.AccountState, acct config.Account, out outcome, now time.Time) (skipped, msg string, follow []string) {
	as.LastRun = now
	as.Totals.Rounds++
	as.Totals.Calls += out.calls
	as.Totals.OK += out.ok
	as.Totals.Failed += out.failed

	skipped = out.skipped
	switch {
	case out.attention != "":
		if as.Status != store.StatusNeedsAttention || as.Reason != out.attention {
			follow = append(follow, "needs_attention")
		}
		as.Status, as.Reason = store.StatusNeedsAttention, out.attention
		skipped, msg = "needs_attention", out.attention
		e.setNextLocked(as, now.Add(attentionRetry))
	case out.skipped == "network":
		e.setNextLocked(as, now.Add(e.rnd(5*time.Minute, 15*time.Minute)))
	default:
		if as.Status == store.StatusNeedsAttention {
			as.Status, as.Reason = store.StatusOK, ""
			follow = append(follow, "recovered")
		}
		switch {
		case out.ok > 0:
			as.LastSuccess, as.ConsecutiveFailedRound = now, 0
		case out.failed > 0:
			as.ConsecutiveFailedRound++
			if as.ConsecutiveFailedRound >= AutoPauseRounds {
				as.Status = store.StatusPaused
				as.Reason = fmt.Sprintf("%d rounds in a row failed (last error: %s); fix the cause, then run: e5renew account resume %s",
					as.ConsecutiveFailedRound, out.lastFail, acct.Name)
				as.NextRun = time.Time{}
				follow = append(follow, "auto_pause")
				msg = as.Reason
			}
		}
		if as.Status == store.StatusOK {
			e.setNextLocked(as, now.Add(e.rnd(e.sched.min, e.sched.max)))
		}
	}
	if out.panicked {
		skipped, msg = "panic", "round panicked; see the engine log"
	} else if out.throttled && msg == "" {
		msg = "throttled; round stopped early"
	}
	return skipped, msg, follow
}

// execute does the work of a round without touching the account's schedule.
func (e *Engine) execute(ctx context.Context, acct config.Account, roundID string) (out outcome) {
	au, err := e.authFor(acct)
	if err == nil {
		var tok auth.Token
		// After Graph rejected a token, do not trust the cached one again.
		force := false
		e.withLock(func() {
			if rt := e.rt[acct.Name]; rt != nil {
				force, rt.forceTok = rt.forceTok, false
			}
		})
		tctx := ctx
		if force {
			tctx = auth.ForceRefresh(ctx)
		}
		tok, err = au.Token(tctx)
		if err != nil && force && ctx.Err() == nil && !errors.Is(err, auth.ErrTransient) {
			// A forced refresh is a recovery attempt; if the identity service
			// rejects it, fall back to a normal silent request before raising
			// attention.
			e.log.Warn("forced token refresh failed; retrying without force", "account", acct.Name)
			tok, err = au.Token(ctx)
		}
		if err == nil {
			return e.callAPIs(ctx, acct, roundID, tok)
		}
	}
	switch {
	case ctx.Err() != nil:
		out.aborted = true
	case errors.Is(err, auth.ErrTransient):
		out.skipped = "network"
	default:
		out.attention = e.attentionReason(err, acct.Name)
	}
	return out
}

// attentionReason describes a sign-in failure for state.json and the call log.
// Provider text can hold tenant names, user names and IDs, so it only goes to
// the engine log; the stored reason has fixed wording plus an error code.
func (e *Engine) attentionReason(err error, name string) string {
	var label string
	switch {
	case errors.Is(err, auth.ErrLoginRequired):
		label = "login required"
	case errors.Is(err, auth.ErrInvalidClient):
		label = "client credentials rejected"
	case errors.Is(err, auth.ErrConsentRequired):
		label = "admin consent required"
	default:
		code := auth.ErrorCode(err)
		if code == "" {
			code = "unknown error"
		}
		label = "sign-in failed (" + code + ")"
		e.log.Warn("sign-in failed", "account", name, "err", firstLine(err.Error()))
	}
	if e.o.Hint != nil {
		if h := e.o.Hint(err, name); h != "" {
			return label + "; " + h
		}
	}
	return label
}

func (e *Engine) callAPIs(ctx context.Context, acct config.Account, roundID string, tok auth.Token) (out outcome) {
	now := e.clock.Now()
	kind := catalog.Delegated
	if !acct.Delegated() {
		kind = catalog.App
	}

	// Record the grants and drop expired skips under the lock.
	skip := map[string]bool{}
	var minN, maxN int
	var random, gone bool
	e.withLock(func() {
		as := e.state.Accounts[acct.Name]
		if as == nil {
			gone = true
			return
		}
		as.GrantedScopes = append([]string(nil), tok.Scopes...)
		sort.Strings(as.GrantedScopes)
		for id, until := range as.APISkipUntil {
			if !until.After(now) {
				delete(as.APISkipUntil, id)
			}
		}
		if len(as.APISkipUntil) == 0 {
			as.APISkipUntil = nil
		}
		for id := range as.APISkipUntil {
			skip[id] = true
		}
		minN, maxN, random = e.cfg.Schedule.APIsPerRoundMin, e.cfg.Schedule.APIsPerRoundMax, e.cfg.Schedule.RandomOrder
	})
	if gone {
		out.aborted = true
		return out
	}

	var cands []catalog.API
	for _, a := range catalog.Available(e.apis, kind, tok.Scopes, acct.Categories) {
		if !skip[a.ID] {
			cands = append(cands, a)
		}
	}
	if len(cands) == 0 {
		out.attention = "no usable APIs: check the app's granted permissions and the account's categories (e5renew account test " + acct.Name + ")"
		return out
	}

	n := minN + e.intn(max(maxN-minN, 0)+1)
	n = max(1, min(n, len(cands)))
	picked := make([]catalog.API, 0, n)
	for _, i := range e.perm(len(cands))[:n] {
		picked = append(picked, cands[i])
	}
	if !random {
		sort.Slice(picked, func(i, j int) bool { return picked[i].ID < picked[j].ID })
	}

	// handle books the result of one call; it reports whether the round must stop.
	handle := func(id string, res graph.Result) (stop bool) {
		switch res.Class {
		case graph.OK:
			out.calls++
			out.ok++
			e.updateAccount(acct.Name, func(as *store.AccountState) { delete(as.APIForbiddenCount, id) })
		case graph.Throttled:
			out.calls++
			out.throttled = true
			wait := res.RetryAfter
			if wait <= 0 {
				wait = defaultRetryAfter
			}
			e.sleep(ctx, min(wait, maxRetryAfterWait))
			return true
		case graph.Network:
			out.netStop = true
			return true
		case graph.Canceled:
			out.aborted = true
			return true
		case graph.AuthFailed:
			out.calls++
			out.failed++
			out.lastFail = res.Class
			e.withLock(func() {
				if rt := e.rt[acct.Name]; rt != nil {
					rt.forceTok = true
				}
			})
			return true
		case graph.Forbidden:
			out.calls++
			out.failed++
			out.lastFail = res.Class
			e.updateAccount(acct.Name, func(as *store.AccountState) {
				if as.APIForbiddenCount == nil {
					as.APIForbiddenCount = map[string]int{}
				}
				as.APIForbiddenCount[id]++
				if as.APIForbiddenCount[id] >= forbiddenLimit {
					if as.APISkipUntil == nil {
						as.APISkipUntil = map[string]time.Time{}
					}
					as.APISkipUntil[id] = e.clock.Now().Add(forbiddenSkip)
					delete(as.APIForbiddenCount, id)
				}
			})
		default:
			out.calls++
			out.failed++
			out.lastFail = res.Class
		}
		return false
	}

	for i, api := range picked {
		if i > 0 && !e.sleep(ctx, e.rnd(e.o.CallGapMin, e.o.CallGapMax)) {
			out.aborted = true
			return out
		}
		path := api.Path(kind)
		if api.UsesUser(kind) {
			user, res := e.targetUser(ctx, acct, roundID, tok.AccessToken)
			if user == "" {
				// Only stop-worthy outcomes end the round; otherwise skip this entry.
				switch res.Class {
				case graph.Network, graph.Canceled, graph.AuthFailed, graph.Throttled, graph.ServerError:
					if handle("users.pick", res) {
						return finishCalls(ctx, out)
					}
				}
				continue
			}
			path = strings.ReplaceAll(path, "{user}", url.PathEscape(user))
		}
		res := e.o.Graph.Call(ctx, tok.AccessToken, path, e.pickQuery(api))
		e.logCall(acct.Name, roundID, api.ID, res)
		if handle(api.ID, res) {
			break
		}
	}
	return finishCalls(ctx, out)
}

func finishCalls(ctx context.Context, out outcome) outcome {
	if ctx.Err() != nil {
		out.aborted = true
	}
	if out.calls == 0 && !out.netStop && !out.aborted {
		// Every picked call needed a {user} that could not be found.
		out.attention = "no calls could be made: set `username` for this account or grant User.Read.All"
	}
	if out.netStop && out.ok == 0 && out.failed == 0 {
		out.skipped = "network"
	}
	return out
}

// logCall writes one call-log line: IDs and numbers only, never content.
func (e *Engine) logCall(account, roundID, apiID string, res graph.Result) {
	e.emit(store.Entry{TS: e.clock.Now(), Account: account, Round: roundID, API: apiID,
		Status: res.Status, Class: string(res.Class), LatencyMS: res.Latency.Milliseconds()})
}

func (e *Engine) updateAccount(name string, f func(*store.AccountState)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if as := e.state.Accounts[name]; as != nil {
		f(as)
	}
}

// pickQuery chooses one random value per query option of the API.
func (e *Engine) pickQuery(a catalog.API) url.Values {
	if len(a.Query) == 0 {
		return nil
	}
	keys := make([]string, 0, len(a.Query))
	for k := range a.Query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	q := url.Values{}
	for _, k := range keys {
		if vals := a.Query[k]; len(vals) > 0 {
			q.Set(k, vals[e.intn(len(vals))])
		}
	}
	return q
}

// targetUser returns the {user} value for application identities: the
// configured user, or one picked from the tenant and cached for the day. On
// failure it returns "" and the lookup's result.
func (e *Engine) targetUser(ctx context.Context, acct config.Account, roundID, token string) (string, graph.Result) {
	if acct.Username != "" {
		return acct.Username, graph.Result{Class: graph.OK}
	}
	day := e.clock.Now().Format("2006-01-02")
	var cached string
	var failedToday bool
	e.withLock(func() {
		if rt := e.rt[acct.Name]; rt != nil {
			if rt.userID != "" && rt.userDay == day {
				cached = rt.userID
			}
			failedToday = rt.userFail == day
		}
	})
	if cached != "" {
		return cached, graph.Result{Class: graph.OK}
	}
	if failedToday {
		// Do not ask again for every entry of every round today.
		return "", graph.Result{Class: graph.NotFound}
	}

	ids, res := e.o.Graph.ListUserIDs(ctx, token)
	e.logCall(acct.Name, roundID, "users.pick", res)
	if res.Class != graph.OK || len(ids) == 0 {
		if res.Class == graph.OK {
			res.Class = graph.NotFound
		}
		switch res.Class {
		case graph.Network, graph.Canceled, graph.AuthFailed, graph.Throttled, graph.ServerError:
			// Temporary: try again next time.
		default:
			e.withLock(func() {
				if rt := e.rt[acct.Name]; rt != nil {
					rt.userFail = day
				}
			})
		}
		return "", res
	}
	id := ids[e.intn(len(ids))]
	e.withLock(func() {
		if rt := e.rt[acct.Name]; rt != nil {
			rt.userID, rt.userDay = id, day
		}
	})
	return id, res
}

// sleep waits for d on the engine clock; it returns false if ctx ended first.
func (e *Engine) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-e.clock.After(d):
		return true
	}
}
