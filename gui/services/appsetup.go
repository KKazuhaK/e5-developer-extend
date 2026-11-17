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
//	Cancel(id)         abandons the session at any point
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
	cancel context.CancelFunc
	choice chan appsetup.Choice
	// Guarded by Backend.mu.
	state string // "signing_in", "planned", "applying"
	plan  *appsetup.Plan
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
	sess := &setupSession{cancel: cancel, choice: make(chan appsetup.Choice, 1), state: "signing_in"}
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
	sess.plan, sess.state = plan, "planned"
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
	emit(AppSetupEvent{Kind: "result", Message: "done", Result: resultView(res)})
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
	c := appsetup.Choice{Rename: choice.Rename}
	if sess.plan.NeedsChoice() {
		switch {
		case choice.CreateNew && choice.Adopt == "":
			c.CreateNew = true
		case !choice.CreateNew && choice.Adopt != "":
			ok := false
			for _, cand := range sess.plan.Candidates {
				if choice.Adopt == cand.ObjectID || choice.Adopt == cand.ClientID {
					ok = true
				}
			}
			if !ok {
				return errors.New("that app registration is not one of the offered ones")
			}
			c.Adopt = choice.Adopt
		default:
			return appsetup.ErrChoiceRequired
		}
	}
	sess.state = "applying"
	sess.choice <- c
	return nil
}

// Cancel abandons the setup at any point. What Microsoft already has stays
// (the next setup reuses it); no account is left behind.
func (s *AppSetupService) Cancel(id string) {
	s.b.mu.Lock()
	sess := s.b.setups[id]
	s.b.mu.Unlock()
	if sess != nil {
		sess.cancel()
	}
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
		SecretExpires: r.SecretExpires,
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
