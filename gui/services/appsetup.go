package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/appsetup"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
)

// AppSetupService creates the app registration of a new account for the
// user (docs/design/app-setup.md). A setup is a session with an ID chosen by
// the frontend, so it can listen on "appsetup:<id>" before starting:
//
//	Begin(id, start)   signs the administrator in (events device_code / browser_opened),
//	                   then sends "signed_in" and "plan"
//	Apply(id, choice)  carries the plan out ("progress" events), ends with "result"
//	Preview(id, choice) lists what a choice would do, changing nothing
//	Finish(id)         ends the session once the wizard has saved the account
//	Cancel(id)         abandons the session at any point; an application
//	                   identity's secret that nobody uses is deleted again
//
// Any failure ends the session with an "error" event. The administrator's
// token never leaves the process: events carry neither tokens, secrets nor
// the administrator's sign-in name. A client secret goes from Graph straight
// into the vault under the account name.
type AppSetupService struct{ b *Backend }

// NewAppSetupService returns the service.
func NewAppSetupService(b *Backend) *AppSetupService { return &AppSetupService{b} }

// AppSetupEventName is the event that carries the progress of the setup with this ID.
func AppSetupEventName(id string) string { return "appsetup:" + id }

var setupIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// setupSession is the part of a running setup that Apply and Cancel reach.
type setupSession struct {
	ctx    context.Context
	cancel context.CancelFunc
	choice chan appsetup.Choice
	finish chan struct{}
	// Guarded by Backend.mu.
	state   string // "signing_in", "planned", "applying", "done", "ending"
	plan    *appsetup.Plan
	session *ops.AppSetupSession
	result  *appsetup.Result
	touched time.Time // last time the user did something with it
}

// Begin starts the setup of the account start.Name and returns at once.
func (s *AppSetupService) Begin(id string, start AppSetupStart) error {
	b := s.b
	if !setupIDRe.MatchString(id) {
		return errors.New("invalid setup id")
	}
	method := start.Method
	if method == "" {
		method = config.AuthDeviceCode
	}
	if method != config.AuthDeviceCode && method != config.AuthBrowser {
		return fmt.Errorf("the administrator signs in with %s or %s, not %q", config.AuthDeviceCode, config.AuthBrowser, method)
	}
	acctAuth := start.Auth
	if start.AppOnly {
		acctAuth = config.AuthClientSecret
		if strings.TrimSpace(start.Username) == "" {
			return errors.New("an application identity reads one user's data: enter that user's name (UPN)")
		}
	} else if acctAuth != config.AuthDeviceCode && acctAuth != config.AuthBrowser {
		return fmt.Errorf("automatic setup is for %s, %s or %s accounts", config.AuthDeviceCode, config.AuthBrowser, config.AuthClientSecret)
	}
	// Fail before anything is created when the account could not be added anyway.
	if err := b.Ops.PrepareAddAccount(AccountInput{Name: start.Name, Auth: acctAuth, Username: start.Username}); err != nil {
		return err
	}
	return s.launch(id, ops.AppSetupOptions{AppOnly: start.AppOnly, Method: method, Account: start.Name})
}

// Repair starts the repair of an existing account's app registration: the
// missing permissions (and consent) are added to the app the account signs in
// with, and nothing else changes: no account is added, no client secret is
// created or deleted, and any other app registration is refused. The events
// are those of Begin; after "result" the caller ends it with Finish.
func (s *AppSetupService) Repair(id, account, method string) error {
	if !setupIDRe.MatchString(id) {
		return errors.New("invalid setup id")
	}
	if method == "" {
		method = config.AuthDeviceCode
	}
	if method != config.AuthDeviceCode && method != config.AuthBrowser {
		return fmt.Errorf("the administrator signs in with %s or %s, not %q", config.AuthDeviceCode, config.AuthBrowser, method)
	}
	if err := s.b.Ops.CheckAccount(account); err != nil {
		return err
	}
	return s.launch(id, ops.AppSetupOptions{Repair: true, Method: method, Account: account})
}

// launch registers the session and runs the setup in the background.
func (s *AppSetupService) launch(id string, opts ops.AppSetupOptions) error {
	b := s.b
	b.mu.Lock()
	if _, taken := b.setups[id]; taken {
		b.mu.Unlock()
		return errors.New("this setup id is already in use")
	}
	for _, other := range b.setups {
		if other.ctx.Err() == nil { // one that was cancelled is only finishing up
			b.mu.Unlock()
			return errors.New("an app setup is already in progress")
		}
	}
	ctx, cancel := context.WithCancel(b.bgCtx)
	sess := &setupSession{ctx: ctx, cancel: cancel, choice: make(chan appsetup.Choice, 1), finish: make(chan struct{}), state: "signing_in", touched: b.now()}
	b.setups[id] = sess
	b.mu.Unlock()
	b.startReaper()

	b.setupWG.Add(1)
	go func() {
		defer func() {
			cancel()
			b.mu.Lock()
			if b.setups[id] == sess { // only its own entry
				delete(b.setups, id)
			}
			b.mu.Unlock()
			b.setupWG.Done()
		}()
		s.run(ctx, id, opts, sess)
	}()
	return nil
}

func (s *AppSetupService) run(ctx context.Context, id string, opts ops.AppSetupOptions, sess *setupSession) {
	b := s.b
	emit := func(ev AppSetupEvent) { b.Emit(AppSetupEventName(id), ev) }
	fail := func(err error) { emit(setupErrorEvent(ctx, err)) }
	ui := &loginUI{open: b.OpenURL, emit: func(le LoginEvent) {
		emit(AppSetupEvent{Kind: le.Kind, Message: le.Message, UserCode: le.UserCode, VerificationURI: le.VerificationURI})
	}}
	progress := func(m string) { emit(AppSetupEvent{Kind: "progress", Message: m}) }

	session, err := b.Ops.BeginAppSetup(ctx, opts, ui, progress)
	if err != nil {
		fail(err)
		return
	}
	defer session.Close()
	// However this ends, a client secret nobody was given is deleted again.
	// (Cancel and Finish take that over by moving the state to "ending".)
	defer s.b.discardIfUnused(sess)
	emit(AppSetupEvent{Kind: "signed_in", Message: "signed in"})

	plan, err := session.Plan(ctx)
	if err != nil {
		fail(err)
		return
	}
	b.mu.Lock()
	sess.plan, sess.state, sess.session, sess.touched = plan, "planned", session, b.now()
	b.mu.Unlock()
	emit(AppSetupEvent{Kind: "plan", Plan: planView(plan)})

	var choice appsetup.Choice
	select {
	case choice = <-sess.choice:
	case <-ctx.Done():
		fail(ctx.Err())
		return
	}
	res, err := session.Apply(ctx, choice)
	if res != nil && res.SecretKeyID != "" {
		// A secret exists even if Apply then failed or was cancelled: remember it so it can be undone.
		b.mu.Lock()
		sess.result = res
		b.mu.Unlock()
	}
	if b.afterApply != nil {
		b.afterApply(sess)
	}
	if err != nil {
		fail(err)
		return
	}
	if opts.AppOnly {
		b.Ops.ReloadEngine() // an existing account may have a new expiry date
	}
	b.mu.Lock()
	sess.result, sess.state, sess.touched = res, "done", b.now()
	b.mu.Unlock()
	emit(AppSetupEvent{Kind: "result", Message: "done", Result: resultView(res)})
	// The session (and with it the right to delete an unused secret) lives
	// until the wizard has saved the account (Finish) or gives up (Cancel).
	select {
	case <-sess.finish:
	case <-ctx.Done():
	}
}

// chosen turns the user's decision into the backend's, for a plan that needs one.
func chosen(plan *appsetup.Plan, choice AppSetupChoice) (appsetup.Choice, error) {
	c := appsetup.Choice{Rename: choice.Rename}
	if !plan.NeedsChoice() {
		return c, nil
	}
	switch {
	case choice.CreateNew && choice.Adopt == "":
		c.CreateNew = true
	case !choice.CreateNew && choice.Adopt != "":
		for _, cand := range plan.Candidates {
			if choice.Adopt == cand.ObjectID || choice.Adopt == cand.ClientID {
				c.Adopt = choice.Adopt
				return c, nil
			}
		}
		return c, errors.New("that app registration is not one of the offered ones")
	default:
		return c, appsetup.ErrChoiceRequired
	}
	return c, nil
}

// Apply carries out the plan Begin sent, with the user's decisions about
// existing apps. It returns at once; "progress" and "result" or "error" follow.
func (s *AppSetupService) Apply(id string, choice AppSetupChoice) error {
	b := s.b
	b.mu.Lock()
	defer b.mu.Unlock()
	sess := b.setups[id]
	if sess == nil {
		return errors.New("this app setup is not running any more")
	}
	if sess.state != "planned" {
		return errors.New("the app setup is not waiting for a decision")
	}
	c, err := chosen(sess.plan, choice)
	if err != nil {
		return err
	}
	sess.state, sess.touched = "applying", b.now()
	sess.choice <- c
	return nil
}

// Preview returns the plan as it would be for choice: when existing apps need
// a decision, the concrete actions of adopting the chosen one (with or without
// the rename) or of creating a new app. It writes nothing. With no choice yet
// it returns the plan as sent.
func (s *AppSetupService) Preview(id string, choice AppSetupChoice) (*AppSetupPlan, error) {
	b := s.b
	b.mu.Lock()
	sess := b.setups[id]
	if sess == nil || sess.state != "planned" {
		b.mu.Unlock()
		return nil, errors.New("the app setup is not waiting for a decision")
	}
	plan, session := sess.plan, sess.session
	sess.touched = b.now()
	b.mu.Unlock()
	if !plan.NeedsChoice() || (choice.Adopt == "" && !choice.CreateNew) {
		return planView(plan), nil
	}
	c, err := chosen(plan, choice)
	if err != nil {
		return nil, err
	}
	p, err := session.PlanFor(sess.ctx, c)
	if err != nil {
		return nil, err
	}
	return planView(p), nil
}

// Finish ends a setup whose result the wizard has used (the account is
// saved). Nothing is undone.
func (s *AppSetupService) Finish(id string) error {
	b := s.b
	b.mu.Lock()
	defer b.mu.Unlock()
	sess := b.setups[id]
	if sess == nil || sess.state != "done" {
		return errors.New("this app setup has not finished")
	}
	sess.state = "ending"
	close(sess.finish)
	sess.cancel()
	return nil
}

// Cancel abandons the setup at any point. What Microsoft already has stays
// (the next setup reuses it); no account is left behind. After a finished
// application-identity setup whose account was not saved, the new client
// secret is deleted from the app and the vault again; the result says whether
// that worked.
func (s *AppSetupService) Cancel(id string) AppSetupCancelResult { return s.b.cancelSetup(id) }

// cancelSetup is Cancel; the idle expiry and Close use it as well.
func (b *Backend) cancelSetup(id string) AppSetupCancelResult {
	b.mu.Lock()
	sess := b.setups[id]
	b.mu.Unlock()
	if sess == nil {
		return AppSetupCancelResult{}
	}
	out := b.discardIfUnused(sess)
	sess.cancel()
	return out
}

// discardIfUnused deletes the client secret of a finished application-identity
// setup whose account was not saved, once (the state becomes "ending").
func (b *Backend) discardIfUnused(sess *setupSession) AppSetupCancelResult {
	b.mu.Lock()
	session, res := sess.session, sess.result
	take := sess.state != "ending" && session != nil && res != nil && res.SecretKeyID != ""
	if sess.state != "ending" && (sess.state == "done" || take) {
		sess.state = "ending"
	}
	b.mu.Unlock()
	var out AppSetupCancelResult
	if !take {
		return out
	}
	acted, err := session.DiscardSecret(sess.ctx, res)
	switch {
	case err != nil:
		out.Error = err.Error()
	case acted:
		out.SecretRemoved = true
	}
	return out
}

// expireSetups cancels sessions that waited for the user (a plan, a result)
// longer than the idle limit, undoing an unused secret.
func (b *Backend) expireSetups() {
	limit := b.SetupIdle
	if limit <= 0 {
		limit = 30 * time.Minute
	}
	now := b.now()
	var ids []string
	b.mu.Lock()
	for id, sess := range b.setups {
		if (sess.state == "planned" || sess.state == "done") && sess.ctx.Err() == nil && now.Sub(sess.touched) > limit {
			ids = append(ids, id)
		}
	}
	b.mu.Unlock()
	for _, id := range ids {
		b.cancelSetup(id)
	}
}

func (b *Backend) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// startReaper runs expireSetups every minute until the backend closes.
func (b *Backend) startReaper() {
	b.reaperOnce.Do(func() {
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					b.expireSetups()
				case <-b.bgCtx.Done():
					return
				}
			}
		}()
	})
}

func setupErrorEvent(ctx context.Context, err error) AppSetupEvent {
	ev := AppSetupEvent{Kind: "error", Code: "other", Message: err.Error()}
	var role *appsetup.RoleError
	switch {
	case errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled):
		ev.Code, ev.Message = "cancelled", "app setup cancelled"
	case errors.Is(err, ops.ErrBootstrapBlocked):
		ev.Code = "blocked"
	case errors.As(err, &role):
		ev.Code, ev.Roles = "forbidden", append([]string{}, role.Roles...)
	case errors.Is(err, appsetup.ErrUnknownPermission):
		ev.Code = "unknown_permission"
	}
	return ev
}

func planView(p *appsetup.Plan) *AppSetupPlan {
	v := &AppSetupPlan{
		Kind: string(p.Kind), TenantID: p.TenantID, DisplayName: p.DisplayName,
		Tags: nonNil(p.Tags), Permissions: nonNil(p.Permissions), Missing: nonNil(p.Missing),
		Candidates: []AppSetupApp{}, Actions: []AppSetupAction{}, NeedsChoice: p.NeedsChoice(),
	}
	if p.Existing != nil {
		v.Existing = &AppSetupApp{ClientID: p.Existing.ClientID, DisplayName: p.Existing.DisplayName}
	}
	for _, c := range p.Candidates {
		v.Candidates = append(v.Candidates, AppSetupApp{ClientID: c.ClientID, DisplayName: c.DisplayName})
	}
	for _, a := range p.Actions {
		v.Actions = append(v.Actions, AppSetupAction{Code: a.Code, Text: a.Text})
	}
	return v
}

func resultView(r *appsetup.Result) *AppSetupResult {
	return &AppSetupResult{
		Kind: string(r.Kind), TenantID: r.TenantID, ClientID: r.ClientID, DisplayName: r.DisplayName,
		CreatedApp: r.CreatedApp, AdoptedApp: r.AdoptedApp, Renamed: r.Renamed,
		ConsentGranted: r.ConsentGranted, AdminConsentURL: r.AdminConsentURL, ConsentError: r.ConsentError,
		SecretExpires: r.SecretExpires, SecretCreated: r.SecretKeyID != "",
		RemovedSecrets: len(r.RemovedSecrets), NotRemovedSecrets: len(r.NotRemovedSecrets),
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
