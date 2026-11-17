package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

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
	opts := ops.AppSetupOptions{AppOnly: start.AppOnly, Method: method, Account: start.Name}

	b.mu.Lock()
	if len(b.setups) > 0 {
		b.mu.Unlock()
		return errors.New("an app setup is already in progress")
	}
	ctx, cancel := context.WithCancel(b.bgCtx)
	sess := &setupSession{ctx: ctx, cancel: cancel, choice: make(chan appsetup.Choice, 1), finish: make(chan struct{}), state: "signing_in"}
	b.setups[id] = sess
	b.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			b.mu.Lock()
			delete(b.setups, id)
			b.mu.Unlock()
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
	emit(AppSetupEvent{Kind: "signed_in", Message: "signed in"})

	plan, err := session.Plan(ctx)
	if err != nil {
		fail(err)
		return
	}
	b.mu.Lock()
	sess.plan, sess.state, sess.session = plan, "planned", session
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
	if err != nil {
		fail(err)
		return
	}
	if opts.AppOnly {
		b.Ops.ReloadEngine() // an existing account may have a new expiry date
	}
	b.mu.Lock()
	sess.result, sess.state = res, "done"
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
	sess.state = "applying"
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
	return nil
}

// Cancel abandons the setup at any point. What Microsoft already has stays
// (the next setup reuses it); no account is left behind. After a finished
// application-identity setup whose account was not saved, the new client
// secret is deleted from the app and the vault again; the result says whether
// that worked.
func (s *AppSetupService) Cancel(id string) AppSetupCancelResult {
	b := s.b
	b.mu.Lock()
	sess := b.setups[id]
	var session *ops.AppSetupSession
	var res *appsetup.Result
	if sess != nil && sess.state == "done" {
		session, res, sess.state = sess.session, sess.result, "ending"
	}
	b.mu.Unlock()
	if sess == nil {
		return AppSetupCancelResult{}
	}
	var out AppSetupCancelResult
	if session != nil && res != nil && res.SecretKeyID != "" {
		acted, err := session.DiscardSecret(sess.ctx, res)
		switch {
		case err != nil:
			out.Error = err.Error()
		case acted:
			out.SecretRemoved = true
		}
	}
	sess.cancel()
	return out
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
