package appsetup

import (
	"errors"
	"fmt"
	"strings"
)

// Kind is the identity the app registration is for.
type Kind string

// The two kinds of app registration.
const (
	// KindDelegated is signed in as a user (device code, browser, password).
	KindDelegated Kind = "delegated"
	// KindApp is the application identity with a client secret.
	KindApp Kind = "app"
)

// Canonical names and tags (see docs/design/app-setup.md, section 2).
const (
	NameDelegated = "E5 Developer Extend"
	NameApp       = "E5 Developer Extend (app-only)"
	TagCommon     = "e5extend"
	TagDelegated  = "e5extend:delegated"
	TagApp        = "e5extend:app"
	// Redirect is the redirect URI of the delegated browser sign-in.
	Redirect = "http://localhost"
)

// legacyNames are the display names, compared case-insensitively, that mark a
// hand-made app registration as a candidate to adopt.
var legacyNames = []string{"E5 Developer Extend", "e5extend", "e5renew", "E5 Renew"}

// DisplayName returns the canonical display name of kind.
func (k Kind) DisplayName() string {
	if k == KindApp {
		return NameApp
	}
	return NameDelegated
}

// Tags returns the canonical tags of kind.
func (k Kind) Tags() []string {
	if k == KindApp {
		return []string{TagCommon, TagApp}
	}
	return []string{TagCommon, TagDelegated}
}

// KindTag is the tag that identifies the kind.
func (k Kind) KindTag() string {
	if k == KindApp {
		return TagApp
	}
	return TagDelegated
}

// AppRef points at an app registration.
type AppRef struct {
	ObjectID    string `json:"objectId"`
	ClientID    string `json:"clientId"`
	DisplayName string `json:"displayName"`
}

// Action is one thing Apply will do (or, in a Result, has done).
type Action struct {
	// Code is stable and meant for interfaces: create_app, adopt_app, rename_app,
	// update_app, create_service_principal, grant_consent, assign_roles, create_secret.
	Code string `json:"code"`
	// Text is an English description.
	Text string `json:"text"`
}

// Plan is what Apply is going to do, computed without changing anything.
type Plan struct {
	Kind        Kind     `json:"kind"`
	TenantID    string   `json:"tenantId"`
	DisplayName string   `json:"displayName"`
	Tags        []string `json:"tags"`
	// Permissions is the full list the app should hold, by name.
	Permissions []string `json:"permissions"`
	// Missing is the part of Permissions the app registration does not list yet
	// (all of them for a new app).
	Missing []string `json:"missing"`
	// Existing is the app registration recognised by its tag; it is reused.
	Existing *AppRef `json:"existing,omitempty"`
	// Candidates are hand-made apps with a familiar name, offered when there is
	// no tagged app: adopt one of them or create a new one.
	Candidates []AppRef `json:"candidates,omitempty"`
	Actions    []Action `json:"actions"`
}

// NeedsChoice reports whether Apply needs a Choice (candidates exist).
func (p *Plan) NeedsChoice() bool { return p.Existing == nil && len(p.Candidates) > 0 }

// Choice carries the user's decisions about existing apps.
type Choice struct {
	// Adopt is the object ID or client ID of the candidate to adopt.
	Adopt string `json:"adopt"`
	// CreateNew creates a new app although candidates exist.
	CreateNew bool `json:"createNew"`
	// Rename renames an adopted app to the canonical name.
	Rename bool `json:"rename"`
}

// Result describes what Apply did. It never contains a secret.
type Result struct {
	Kind        Kind   `json:"kind"`
	TenantID    string `json:"tenantId"`
	ClientID    string `json:"clientId"`
	ObjectID    string `json:"objectId"`
	DisplayName string `json:"displayName"`

	CreatedApp              bool     `json:"createdApp"`
	AdoptedApp              bool     `json:"adoptedApp"`
	Renamed                 bool     `json:"renamed"`
	UpdatedApp              bool     `json:"updatedApp"`
	AddedPermissions        []string `json:"addedPermissions"`
	CreatedServicePrincipal bool     `json:"createdServicePrincipal"`

	// ConsentGranted is true when administrator consent is in place for every
	// recommended permission. When it is false, AdminConsentURL lets someone
	// with the right role grant it, and ConsentError says why it failed.
	ConsentGranted  bool     `json:"consentGranted"`
	ConsentAdded    []string `json:"consentAdded"`
	AdminConsentURL string   `json:"adminConsentUrl,omitempty"`
	ConsentError    string   `json:"consentError,omitempty"`

	// SecretName and SecretExpires (YYYY-MM-DD) describe the client secret
	// created for KindApp; the secret itself went to the SecretSink.
	SecretName    string `json:"secretName,omitempty"`
	SecretExpires string `json:"secretExpires,omitempty"`
}

// AdminConsentURL is the link an administrator opens to grant consent.
func AdminConsentURL(tenant, clientID string) string {
	return "https://login.microsoftonline.com/" + tenant + "/adminconsent?client_id=" + clientID
}

// Errors.
var (
	// ErrInsufficientRole means the signed-in account lacks a directory role.
	ErrInsufficientRole = errors.New("insufficient directory role")
	// ErrUnknownPermission means a recommended permission is not in Microsoft Graph.
	ErrUnknownPermission = errors.New("unknown Graph permission")
	// ErrChoiceRequired means existing apps need a decision (see Plan.Candidates).
	ErrChoiceRequired = errors.New("choose whether to adopt an existing app registration or create a new one")
)

// RoleError is a 403 from Graph with the roles that would have allowed it.
type RoleError struct {
	Doing string   // "create or update the app registration"
	Roles []string // any one of these
}

// Manual points at the manual setup guide.
const Manual = "docs/user/app-registration.md"

func (e *RoleError) Error() string {
	return fmt.Sprintf("this account is not allowed to %s: it needs the role %s. "+
		"Sign in again with an administrator account, or set the app up by hand (%s)",
		e.Doing, strings.Join(e.Roles, " or "), Manual)
}

func (e *RoleError) Unwrap() error { return ErrInsufficientRole }

var (
	rolesApp   = []string{"Application Administrator", "Cloud Application Administrator", "Global Administrator"}
	rolesGrant = []string{"Privileged Role Administrator", "Global Administrator"}
)

// UnknownPermissionError lists permission names Microsoft Graph does not know.
type UnknownPermissionError struct {
	Kind  Kind
	Names []string
}

func (e *UnknownPermissionError) Error() string {
	what := "delegated permission"
	if e.Kind == KindApp {
		what = "application permission"
	}
	return fmt.Sprintf("Microsoft Graph has no %s named %s; nothing was changed", what, strings.Join(e.Names, ", "))
}

func (e *UnknownPermissionError) Unwrap() error { return ErrUnknownPermission }
