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

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/graph"
	"github.com/KKazuhaK/e5-developer-extend/internal/store"
)

type outcome struct {
	calls, ok, failed int
	throttled         bool   // some call ended throttled (it is no failure)
	unavailable       int    // calls refused with 403/404; the API is paused for the account
	refused           bool   // some API answered 403/404 (now or within the last 24 hours)
	allSkipped        bool   // every API the account may call is paused after a 403/404
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
		if rt := e.rt[acct.Key()]; rt != nil {
			rt.running = false
		}
		e.mu.Unlock()
	}()

	roundID := fmt.Sprintf("%08x", e.intn(1<<32))
	if manual {
		// An explicit run retries a failed user lookup.
		e.withLock(func() {
			if rt := e.rt[acct.Key()]; rt != nil {
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
		as := e.state.Accounts[acct.Key()]
		if as == nil { // removed from the config meanwhile
			found = false
			return
		}
		skipped, msg, follow = e.bookLocked(as, acct, out, now)
		e.roundsDone = true
		e.saveLocked()
	})
	if !found {
		return
	}

	ent := e.entry(acct.Key())
	ent.TS, ent.Round, ent.Event = now, roundID, "round_end"
	ent.OK, ent.Failed, ent.Skipped, ent.Message = out.ok, out.failed, skipped, msg
	e.emit(ent)
	for _, ev := range follow {
		e.event(acct.Key(), ev, msg)
	}
	e.evalNotify()
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
		// Only a round that succeeded ends an attention state: one that only
		// met throttling or refusals (403/404) proves nothing.
		if as.Status == store.StatusNeedsAttention && out.ok > 0 {
			as.Status, as.Reason = store.StatusOK, ""
			follow = append(follow, "recovered")
		}
		switch {
		case out.ok > 0:
			as.LastSuccess, as.ConsecutiveFailedRound = now, 0
		case as.Status == store.StatusNeedsAttention:
			e.setNextLocked(as, now.Add(attentionRetry))
		case out.failed > 0 || out.allSkipped:
			// Real failures count. So does a round without success in which
			// every API the account may call is refused (paused): an account
			// that lost its license or mailbox ends up paused (and probed).
			// A round that only met some refusals or throttling is neutral:
			// with few APIs per round, picking unavailable ones is normal.
			as.ConsecutiveFailedRound++
			if as.ConsecutiveFailedRound >= AutoPauseRounds && e.cfg.Schedule.AutoPause {
				as.Status = store.StatusPaused
				what := fmt.Sprintf("%d rounds in a row failed (last error: %s)", as.ConsecutiveFailedRound, out.lastFail)
				if out.failed == 0 && out.calls == 0 {
					what = fmt.Sprintf("%d rounds in a row made no call: no API left to call, all were refused (403/404) recently (license or mailbox removed?)", as.ConsecutiveFailedRound)
				} else if out.failed == 0 {
					what = fmt.Sprintf("%d rounds in a row had no successful call: every API answered 403/404 (license or mailbox removed?)", as.ConsecutiveFailedRound)
				}
				as.Reason = fmt.Sprintf("%s; it is tried again automatically, or fix the cause and run: e5extend account resume %s", what, acct.Name)
				as.NextRun = time.Time{}
				as.NextProbe, as.Probes = now.Add(probeDelay(0)), 0
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
		msg = "throttled by Microsoft Graph; some calls were skipped"
	}
	return skipped, msg, follow
}

// execute does the work of a round without touching the account's schedule.
func (e *Engine) execute(ctx context.Context, acct config.Account, roundID string) (out outcome) {
	tok, err := e.signIn(ctx, acct)
	if err == nil {
		return e.callAPIs(ctx, acct, roundID, tok)
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

// signIn gets the access token for acct, as a round and a probe need it.
func (e *Engine) signIn(ctx context.Context, acct config.Account) (auth.Token, error) {
	au, err := e.authFor(acct)
	if err != nil {
		return auth.Token{}, err
	}
	// After Graph rejected a token, do not trust the cached one again.
	force := false
	e.withLock(func() {
		if rt := e.rt[acct.Key()]; rt != nil {
			force, rt.forceTok = rt.forceTok, false
		}
	})
	// Only here may a password account sign in with its password again.
	ctx = e.gateContext(ctx, acct.Key())
	tctx := ctx
	if force {
		tctx = auth.ForceRefresh(ctx)
	}
	tok, err := au.Token(tctx)
	if err != nil && force && ctx.Err() == nil && !errors.Is(err, auth.ErrTransient) && auth.CredentialFailure(err) == "" {
		// A forced refresh is a recovery attempt; if the identity service
		// rejects it, fall back to a normal silent request before raising
		// attention. A credential failure of the password sign-in is not
		// tried again: that would repeat a wrong password.
		e.log.Warn("forced token refresh failed; retrying without force", "account", acct.Name)
		tok, err = au.Token(ctx)
	}
	if err == nil {
		e.saveHomeAccount(ctx, acct, tok)
	}
	return tok, err
}

// attentionReason describes a sign-in failure for state.json and the call log.
// Provider text can hold tenant names, user names and IDs, so it only goes to
// the engine log; the stored reason has fixed wording plus an error code.
func (e *Engine) attentionReason(err error, name string) string {
	var label string
	switch {
	case auth.IsSignInFrequency(err):
		label = auth.SignInFrequencyLabel
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
		as := e.state.Accounts[acct.Key()]
		if as == nil {
			gone = true
			return
		}
		e.pruneAPIStatsLocked(as)
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

	out.refused = len(skip) > 0
	var cands []catalog.API
	allowed := catalog.Available(e.apis, kind, tok.Scopes, acct.Categories)
	for _, a := range allowed {
		if !skip[a.ID] {
			cands = append(cands, a)
		}
	}
	if len(cands) == 0 && len(allowed) > 0 {
		// Every API the account may call answered 403/404 within the last 24
		// hours: that is a refused round, not a permission problem to fix.
		out.allSkipped, out.lastFail = true, graph.Forbidden
		return out
	}
	if len(cands) == 0 {
		out.attention = "no usable APIs: check the app's granted permissions and the account's categories (e5extend account test " + acct.Name + ")"
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
	throttledRun := 0
	handle := func(id string, res graph.Result) (stop bool) {
		e.countAPI(acct.Key(), id, res)
		if res.Class != graph.Throttled {
			throttledRun = 0
		}
		switch res.Class {
		case graph.OK:
			out.calls++
			out.ok++
		case graph.Throttled:
			// The call was retried while Retry-After allowed (see callOne). It
			// is skipped now, and no failure. Two in a row mean the account
			// is being throttled as a whole: the round ends.
			out.calls++
			out.throttled = true
			throttledRun++
			return throttledRun >= 2
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
				if rt := e.rt[acct.Key()]; rt != nil {
					rt.forceTok = true
				}
			})
			return true
		case graph.Forbidden, graph.NotFound:
			// The account cannot use this API: stop calling it for a day. It
			// is no failure of the round.
			out.calls++
			out.unavailable++
			out.refused = true
			out.lastFail = res.Class
			skip[id] = true
			e.updateAccount(acct.Key(), func(as *store.AccountState) {
				if as.APISkipUntil == nil {
					as.APISkipUntil = map[string]time.Time{}
				}
				as.APISkipUntil[id] = e.clock.Now().Add(unavailableFor)
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
				case graph.Forbidden, graph.NotFound:
					// No user to address: this API is refused for the round
					// (not remembered), so a round that lost everything else
					// too still counts as refused.
					out.unavailable++
					out.lastFail = res.Class
					skip[api.ID] = true
				}
				continue
			}
			path = strings.ReplaceAll(path, "{user}", url.PathEscape(user))
		}
		res := e.callOne(ctx, acct, roundID, api.ID, tok.AccessToken, path, e.pickQuery(api))
		if handle(api.ID, res) {
			break
		}
	}
	if out.ok == 0 && out.unavailable > 0 && ctx.Err() == nil {
		// Nothing worked and every API the account may call is now refused.
		out.allSkipped = true
		for _, a := range allowed {
			if !skip[a.ID] {
				out.allSkipped = false
				break
			}
		}
	}
	return finishCalls(ctx, out)
}

// callOne makes one call. When Graph throttles it (429, or 503 with
// Retry-After) the same call is repeated after the time Retry-After names, at
// most throttleRetries more times; a wait longer than maxRetryAfterWait ends
// the attempts at once. Every answer is logged; the result is the last one.
func (e *Engine) callOne(ctx context.Context, acct config.Account, roundID, apiID, token, path string, q url.Values) graph.Result {
	for attempt := 0; ; attempt++ {
		res := e.o.Graph.Call(ctx, token, path, q)
		e.logCall(acct, roundID, apiID, res)
		if res.Class != graph.Throttled {
			return res
		}
		wait := res.RetryAfter
		if wait <= 0 {
			wait = defaultRetryAfter
		}
		if attempt >= throttleRetries || wait > maxRetryAfterWait {
			return res
		}
		if !e.sleep(ctx, wait) {
			return graph.Result{Class: graph.Canceled, Err: ctx.Err()}
		}
	}
}

func finishCalls(ctx context.Context, out outcome) outcome {
	if ctx.Err() != nil {
		out.aborted = true
	}
	if out.calls == 0 && !out.netStop && !out.aborted && !(out.allSkipped && out.refused) {
		// Every picked call needed a {user} that could not be found. (Not when
		// the other APIs were refused too: that is a refused round.)
		out.attention = "no calls could be made: set `username` for this account or grant User.Read.All"
	}
	if out.netStop && out.ok == 0 && out.failed == 0 {
		out.skipped = "network"
	}
	return out
}

// logCall writes one call-log line: IDs and numbers only, never content.
func (e *Engine) logCall(acct config.Account, roundID, apiID string, res graph.Result) {
	// The name is the one the round began with; the ID keeps the line with the account across a rename.
	e.emit(store.Entry{TS: e.clock.Now(), Account: acct.Name, AccountID: acct.ID, Round: roundID, API: apiID,
		Status: res.Status, Class: string(res.Class), LatencyMS: res.Latency.Milliseconds()})
}

// countAPI adds one call to the account's per-API statistics, counted the way
// Totals counts (a throttled call is not counted, only remembered as the last
// result). Calls that
// never got an answer (network error, cancelled) and the user lookup are not
// counted. The map holds at most one entry per catalog API.
func (e *Engine) countAPI(account, id string, res graph.Result) {
	if res.Class == graph.Network || res.Class == graph.Canceled || id == "users.pick" {
		return
	}
	known := false
	for _, a := range e.apis {
		if a.ID == id {
			known = true
			break
		}
	}
	if !known {
		return
	}
	now := e.clock.Now()
	e.updateAccount(account, func(as *store.AccountState) {
		if as.APIStats == nil {
			as.APIStats = map[string]*store.APIStat{}
		}
		st := as.APIStats[id]
		if st == nil {
			st = &store.APIStat{}
			as.APIStats[id] = st
		}
		// Like Totals: a throttled call is neither a success nor a failure.
		switch res.Class {
		case graph.OK:
			st.OK++
		case graph.Throttled:
		default:
			st.Failed++
		}
		st.LastClass, st.LastStatus, st.LastAt = string(res.Class), res.Status, now
	})
}

// pruneAPIStatsLocked drops statistics of APIs that are no longer in the catalog.
func (e *Engine) pruneAPIStatsLocked(as *store.AccountState) {
	if len(as.APIStats) == 0 {
		return
	}
	in := make(map[string]bool, len(e.apis))
	for _, a := range e.apis {
		in[a.ID] = true
	}
	for id := range as.APIStats {
		if !in[id] {
			delete(as.APIStats, id)
		}
	}
	if len(as.APIStats) == 0 {
		as.APIStats = nil
	}
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
		if rt := e.rt[acct.Key()]; rt != nil {
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
	e.logCall(acct, roundID, "users.pick", res)
	if res.Class != graph.OK || len(ids) == 0 {
		if res.Class == graph.OK {
			res.Class = graph.NotFound
		}
		switch res.Class {
		case graph.Network, graph.Canceled, graph.AuthFailed, graph.Throttled, graph.ServerError:
			// Temporary: try again next time.
		default:
			e.withLock(func() {
				if rt := e.rt[acct.Key()]; rt != nil {
					rt.userFail = day
				}
			})
		}
		return "", res
	}
	id := ids[e.intn(len(ids))]
	e.withLock(func() {
		if rt := e.rt[acct.Key()]; rt != nil {
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

// saveHomeAccount stores the home account ID a password sign-in returned, which
// the configuration lacks (the first login did not get it written back).
func (e *Engine) saveHomeAccount(ctx context.Context, acct config.Account, tok auth.Token) {
	if !acct.Delegated() || tok.HomeAccountID == "" || tok.HomeAccountID == acct.HomeAccountID || e.o.SaveHomeAccount == nil {
		return
	}
	if err := e.o.SaveHomeAccount(ctx, acct, tok.HomeAccountID); err != nil {
		e.log.Warn("save home account id", "account", acct.Name, "err", err)
		return
	}
	e.withLock(func() {
		if a, ok := e.cfg.FindKey(acct.Key()); ok {
			a.HomeAccountID = tok.HomeAccountID
		}
	})
}
