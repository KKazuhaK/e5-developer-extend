package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/catalog"
	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
)

// AccountService manages accounts and their sign-in.
type AccountService struct{ b *Backend }

// NewAccountService returns the service.
func NewAccountService(b *Backend) *AccountService { return &AccountService{b} }

// LoginEventName is the event that carries the progress of a sign-in.
func LoginEventName(account string) string { return "login:" + account }

// Get returns the editable fields of an account. The secret is never part of it.
func (s *AccountService) Get(name string) (AccountInput, error) {
	in, err := s.b.Ops.GetAccount(name)
	in.Secret = ""
	return in, err
}

// Add adds an account. For client_secret and password accounts in.Secret is stored in the vault.
func (s *AccountService) Add(in AccountInput) error {
	err := s.b.Ops.AddAccount(context.Background(), in)
	if err == nil {
		s.b.Ops.ReloadEngine()
	}
	return err
}

// Update changes an account. A non-empty in.Secret replaces the stored secret.
func (s *AccountService) Update(in AccountInput) error {
	err := s.b.Ops.UpdateAccount(context.Background(), in)
	if err == nil {
		s.b.Ops.ReloadEngine()
	}
	return err
}

// Rename gives an account a new name; its secret, sign-in and statistics move
// with it. A sign-in in progress under the old name is abandoned. Update the
// other fields afterwards under the new name. The warnings are problems after
// the rename went through (an old vault entry that could not be deleted, state
// that could not be carried over); the caller shows them.
func (s *AccountService) Rename(from, to string) ([]string, error) {
	s.CancelLogin(from)
	warnings, err := s.b.Ops.RenameAccount(context.Background(), from, to)
	if err != nil {
		return []string{}, err
	}
	s.b.Ops.ReloadEngine()
	if warnings == nil {
		warnings = []string{}
	}
	return warnings, nil
}

// Remove deletes an account with its stored secret and sign-in.
func (s *AccountService) Remove(name string) error {
	s.CancelLogin(name)
	_, err := s.b.Ops.RemoveAccount(context.Background(), name)
	if err == nil {
		s.b.Ops.ReloadEngine()
	}
	return err
}

// SetSecret replaces the stored client secret or password.
func (s *AccountService) SetSecret(name, secret string) error {
	return s.b.Ops.SetSecret(context.Background(), name, secret, "")
}

// Pause stops scheduling rounds for an account.
func (s *AccountService) Pause(name string) error {
	_, err := s.b.Ops.Pause(context.Background(), name)
	return err
}

// Resume resumes a paused account or clears an attention state.
func (s *AccountService) Resume(name string) error {
	_, err := s.b.Ops.Resume(context.Background(), name)
	return err
}

// Test gets a token silently and reports what it grants. It shows no sign-in name.
func (s *AccountService) Test(name string) (TestResult, error) {
	o := s.b.Ops
	tok, err := o.TestAccount(context.Background(), name)
	if err != nil {
		return TestResult{}, err
	}
	cfg, _, err := o.LoadConfig()
	if err != nil {
		return TestResult{}, err
	}
	acct, err := o.FindAccount(cfg, name)
	if err != nil {
		return TestResult{}, err
	}
	apis, err := catalog.Load()
	if err != nil {
		return TestResult{}, err
	}
	kind := catalog.Delegated
	if !acct.Delegated() {
		kind = catalog.App
	}
	return TestResult{
		Scopes:     append([]string{}, tok.Scopes...),
		UsableAPIs: len(catalog.Available(apis, kind, tok.Scopes, acct.Categories)),
		TotalAPIs:  len(apis),
		ExpiresOn:  tok.ExpiresOn.Format("2006-01-02T15:04:05Z07:00"),
	}, nil
}

// Recommend probes which API categories the signed-in account can really use
// (read-only requests, a few seconds). An account that is not signed in is not
// an error: NeedsLogin is set and there is no advice.
func (s *AccountService) Recommend(name string) (RecommendResult, error) {
	rec, err := s.b.Ops.RecommendCategories(s.b.bgCtx, name)
	if errors.Is(err, auth.ErrLoginRequired) {
		return RecommendResult{NeedsLogin: true, Categories: []CategoryAdvice{}, Recommended: []string{}}, nil
	}
	if err != nil {
		return RecommendResult{}, err
	}
	return RecommendResult{Categories: nonNilAdvice(rec.Categories), Recommended: rec.Recommended()}, nil
}

func nonNilAdvice(in []CategoryAdvice) []CategoryAdvice {
	if in == nil {
		return []CategoryAdvice{}
	}
	return in
}

// Login starts the sign-in of an account and returns at once. Progress and
// the outcome arrive as LoginEvents on "login:<name>"; the last one is "done"
// or "error". Only one sign-in per account runs at a time.
func (s *AccountService) Login(name string) error { return s.startLogin(name, false) }

// LoginAfterSetup is Login for an account whose app registration automatic
// setup has just created: while Microsoft is still replicating the app it
// waits and tries again for about two minutes, sending "progress" events.
func (s *AccountService) LoginAfterSetup(name string) error { return s.startLogin(name, true) }

func (s *AccountService) startLogin(name string, afterSetup bool) error {
	b := s.b
	if err := b.Ops.CheckAccount(name); err != nil {
		return err
	}
	b.mu.Lock()
	if _, busy := b.logins[name]; busy {
		b.mu.Unlock()
		return fmt.Errorf("a sign-in for %q is already in progress", name)
	}
	ctx, cancel := context.WithCancel(b.bgCtx)
	b.logins[name] = cancel
	b.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			b.mu.Lock()
			delete(b.logins, name)
			b.mu.Unlock()
		}()
		emit := func(ev LoginEvent) { b.Emit(LoginEventName(name), ev) }
		ui := &loginUI{emit: emit, open: b.OpenURL}
		var res ops.LoginResult
		var err error
		if afterSetup {
			res, err = b.Ops.LoginAfterSetup(ctx, name, ui, func(m string) { emit(LoginEvent{Kind: "progress", Message: m}) })
		} else {
			res, err = b.Ops.Login(ctx, name, ui)
		}
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			emit(LoginEvent{Kind: "error", Message: "sign-in cancelled"})
		case err != nil:
			emit(LoginEvent{Kind: "error", Message: err.Error()})
		default:
			b.Ops.ReloadEngine()
			msg := "signed in"
			if !res.Delegated {
				msg = "client secret verified"
			}
			emit(LoginEvent{Kind: "done", Message: msg})
		}
	}()
	return nil
}

// CancelLogin abandons the sign-in in progress, if any.
func (s *AccountService) CancelLogin(name string) {
	s.b.mu.Lock()
	cancel := s.b.logins[name]
	s.b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// loginUI turns the prompts of the sign-in into events.
type loginUI struct {
	emit func(LoginEvent)
	open func(string) error
	once sync.Once
}

var (
	deviceURLRe  = regexp.MustCompile(`https://[^\s"']+`)
	deviceCodeRe = regexp.MustCompile(`(?i)\bcode\s+([A-Z0-9]{6,12})\b`)
)

// DeviceCode shows the code and the address to enter it at. Both are meant to be seen by the user.
func (u *loginUI) DeviceCode(message string) {
	ev := LoginEvent{Kind: "device_code", Message: message}
	if m := deviceURLRe.FindString(message); m != "" {
		ev.VerificationURI = strings.TrimRight(m, ".,)")
	}
	if m := deviceCodeRe.FindStringSubmatch(message); m != nil {
		ev.UserCode = m[1]
	}
	u.emit(ev)
}

// OpenBrowser opens the sign-in page. The address is not put into the event: it carries the
// request state, and the user needs only the hint to look at the browser.
func (u *loginUI) OpenBrowser(url string) error {
	_ = u.open(url)
	u.emit(LoginEvent{Kind: "browser_opened", Message: "complete the sign-in in your browser"})
	return nil
}

var _ auth.LoginUI = (*loginUI)(nil)
