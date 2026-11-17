package services

import "github.com/KKazuhaK/e5-renew/internal/ops"

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
	// Kind is "device_code", "browser_opened", "done" or "error".
	Kind            string `json:"kind"`
	Message         string `json:"message"`
	UserCode        string `json:"userCode,omitempty"`
	VerificationURI string `json:"verificationUri,omitempty"`
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
