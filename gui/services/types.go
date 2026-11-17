package services

import "github.com/KKazuhaK/e5-developer-extend/internal/ops"

// The types below mirror the TypeScript contract. Types shared with ops are
// aliases, so there is one definition and one set of JSON tags.

// Snapshot is the state of the engine and every account.
type Snapshot = ops.Snapshot

// AccountSummary is one account in a Snapshot.
type AccountSummary = ops.AccountSummary

// AccountInput is used to add and edit an account. Secret is only ever
// received; it is never part of a result.
type AccountInput = ops.AccountInput

// ValidationError is a rejected setting.
type ValidationError = ops.ValidationError

// LoginEvent is pushed on the event "login:<name>".
type LoginEvent struct {
	// Kind is "device_code", "browser_opened", "progress" (a wait the user should
	// know about, e.g. Microsoft replicating a new app), "done" or "error".
	Kind            string `json:"kind"`
	Message         string `json:"message"`
	UserCode        string `json:"userCode,omitempty"`
	VerificationURI string `json:"verificationUri,omitempty"`
}

// AppSetupStart says which account automatic app setup is for. Nothing is
// created for the account yet; the account is added afterwards by the wizard.
type AppSetupStart struct {
	// Name is the account name (for the application identity it names the vault entry of the secret).
	Name string `json:"name"`
	// AppOnly sets up the client-secret identity; otherwise Auth is the account's own sign-in: device_code or browser.
	AppOnly bool   `json:"appOnly"`
	Auth    string `json:"auth"`
	// Username is the user an application identity reads; it is only checked here.
	Username string `json:"username"`
	// Method is how the administrator signs in: device_code (default) or browser.
	Method string `json:"method"`
}

// AppSetupEvent is pushed on the event "appsetup:<id>". Kind is
// "device_code", "browser_opened" (the administrator's sign-in), "signed_in",
// "plan" (Plan is set), "progress" (Message), "result" (Result is set) or
// "error". An error has a Code: "forbidden" (Roles lists the roles that would
// have allowed it), "blocked" (the tenant refuses Microsoft Graph Command Line
// Tools), "unknown_permission", "cancelled" or "other"; Message is English text.
type AppSetupEvent struct {
	Kind            string          `json:"kind"`
	Message         string          `json:"message"`
	UserCode        string          `json:"userCode,omitempty"`
	VerificationURI string          `json:"verificationUri,omitempty"`
	Plan            *AppSetupPlan   `json:"plan,omitempty"`
	Result          *AppSetupResult `json:"result,omitempty"`
	Code            string          `json:"code,omitempty"`
	Roles           []string        `json:"roles,omitempty"`
}

// AppSetupApp is an app registration the plan mentions.
type AppSetupApp struct {
	ClientID    string `json:"clientId"`
	DisplayName string `json:"displayName"`
}

// AppSetupAction is one thing the setup will do. Code is stable (create_app,
// adopt_or_create, update_app, create_service_principal, grant_consent,
// assign_roles, create_secret); Text is English.
type AppSetupAction struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// AppSetupPlan is what the setup is going to do; nothing has changed yet.
type AppSetupPlan struct {
	Kind        string   `json:"kind"` // "delegated" or "app"
	TenantID    string   `json:"tenantId"`
	DisplayName string   `json:"displayName"`
	Tags        []string `json:"tags"`
	// Permissions is the full list the app holds afterwards; Missing is the part it does not list yet.
	Permissions []string `json:"permissions"`
	Missing     []string `json:"missing"`
	// Existing is an app registration found by its tag; it is reused.
	Existing *AppSetupApp `json:"existing"`
	// Candidates are hand-made apps: when NeedsChoice, adopt one or create a new one.
	Candidates  []AppSetupApp    `json:"candidates"`
	Actions     []AppSetupAction `json:"actions"`
	NeedsChoice bool             `json:"needsChoice"`
}

// AppSetupChoice is the user's decision about existing apps.
type AppSetupChoice struct {
	// Adopt is the client ID of the candidate to adopt; CreateNew creates a new app instead.
	Adopt     string `json:"adopt"`
	CreateNew bool   `json:"createNew"`
	// Rename gives an adopted app the canonical name.
	Rename bool `json:"rename"`
}

// AppSetupResult is what the setup did. It never holds a secret.
type AppSetupResult struct {
	Kind        string `json:"kind"`
	TenantID    string `json:"tenantId"`
	ClientID    string `json:"clientId"`
	DisplayName string `json:"displayName"`
	CreatedApp  bool   `json:"createdApp"`
	AdoptedApp  bool   `json:"adoptedApp"`
	Renamed     bool   `json:"renamed"`
	// ConsentGranted is false when someone with the right role still has to
	// open AdminConsentURL; ConsentError says why it failed.
	ConsentGranted  bool   `json:"consentGranted"`
	AdminConsentURL string `json:"adminConsentUrl"`
	ConsentError    string `json:"consentError"`
	// SecretExpires (YYYY-MM-DD) is the expiry of the client secret stored in the vault, for the application identity.
	SecretExpires string `json:"secretExpires"`
	// SecretCreated says a client secret was created and stored in the vault (never shown).
	SecretCreated bool `json:"secretCreated"`
	// RemovedSecrets counts this computer's earlier secrets deleted from the app;
	// NotRemovedSecrets counts older ones left alone because they might belong to another account.
	RemovedSecrets    int `json:"removedSecrets"`
	NotRemovedSecrets int `json:"notRemovedSecrets"`
}

// AppSetupCancelResult says what Cancel undid. SecretRemoved is true when a
// client secret created for the abandoned setup was deleted again. Error is
// set when that failed; it names the secret (name and key ID, never the
// value) that is still live in Entra.
type AppSetupCancelResult struct {
	SecretRemoved bool   `json:"secretRemoved"`
	Error         string `json:"error"`
}

// LogEntry is one line of the call log.
type LogEntry struct {
	TS        string `json:"ts"`
	Account   string `json:"account"`
	API       string `json:"api"`
	Status    int    `json:"status"`
	Class     string `json:"class"`
	LatencyMs int64  `json:"latencyMs"`
	Event     string `json:"event,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// ScheduleSettings, NetworkSettings and NotifySettings are the settings of the config file.
type (
	ScheduleSettings = ops.ScheduleSettings
	NetworkSettings  = ops.NetworkSettings
	NotifySettings   = ops.NotifySettings
)

// UISettings only concern the interface; they live in gui.json.
type UISettings struct {
	// Language is "auto", "zh" or "en".
	Language string `json:"language"`
	// Theme is "system", "light" or "dark".
	Theme        string `json:"theme"`
	StartAtLogin bool   `json:"startAtLogin"`
}

// Settings is everything the settings page edits.
type Settings struct {
	Schedule ScheduleSettings `json:"schedule"`
	Network  NetworkSettings  `json:"network"`
	Notify   NotifySettings   `json:"notify"`
	UI       UISettings       `json:"ui"`
}

// DoctorCheck is one health check result.
type DoctorCheck struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Level  string `json:"level"` // "ok", "warn" or "fail"
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// ServiceInfo describes the background job.
type ServiceInfo struct {
	Platform  string   `json:"platform"`
	Installed bool     `json:"installed"`
	Enabled   string   `json:"enabled"` // "yes", "no" or "unknown"
	Running   string   `json:"running"`
	Details   []string `json:"details"`
}

// ServiceResult says what installing or removing the background job did.
type ServiceResult struct {
	Actions  []string `json:"actions"`
	Warnings []string `json:"warnings"`
	// Manual lists commands the user has to run themselves.
	Manual []string `json:"manual"`
}

// CatalogEntry is one API call of the built-in catalog.
type CatalogEntry struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Name     string   `json:"name"`
	Usable   bool     `json:"usable"`
	Scopes   []string `json:"scopes"`
}

// TestResult is what a successful account test shows.
type TestResult struct {
	// Scopes are the permissions the token carries.
	Scopes     []string `json:"scopes"`
	UsableAPIs int      `json:"usableApis"`
	TotalAPIs  int      `json:"totalApis"`
	ExpiresOn  string   `json:"expiresOn"`
}

// BackupSummary and ImportSummary say what an export or import did; they hold
// account names and counts, never a secret or the webhook address.
type (
	BackupSummary = ops.BackupSummary
	ImportSummary = ops.ImportSummary
)

// BackupProblem is a refusal or failure of an export or import that the
// interface translates. Code is one of the ops.Code* values or a code for an
// error of the file itself (not_backup, wrong_passphrase, ...). Names are
// account names or other items the message is about; Detail is English text
// from the backend for the cases that carry one.
type BackupProblem struct {
	Code   string   `json:"code"`
	Names  []string `json:"names"`
	Detail string   `json:"detail"`
}

// BackupExportResult is the outcome of Export. Cancelled is true when the
// user closed the file dialog; nothing was written then. Problem is set when
// the export was refused; nothing was written then either.
type BackupExportResult struct {
	Cancelled bool           `json:"cancelled"`
	Summary   BackupSummary  `json:"summary"`
	Problem   *BackupProblem `json:"problem"`
}

// BackupImportResult is the outcome of Import.
type BackupImportResult struct {
	Cancelled bool           `json:"cancelled"`
	Summary   ImportSummary  `json:"summary"`
	Problem   *BackupProblem `json:"problem"`
}
