// The front/back contract, verbatim from docs/design/gui.md section 3.
// Keep in sync with the Go services in gui/services.

export type Level = "ok" | "warn" | "fail";
export type AuthKind = "device_code" | "browser" | "client_secret" | "password";
export type AccountStatus = "ok" | "needs_attention" | "paused" | "period_ended" | "not_signed_in";

export interface Snapshot {
  engine: { running: boolean; heartbeatAgeSec: number | null; host: "gui" | "service" | "none" };
  accounts: AccountSummary[];
  generatedAt: string; // RFC3339
}
export interface AccountSummary {
  name: string;
  auth: AuthKind;
  tenant: string;
  status: AccountStatus;
  reason: string; // human readable, may be empty
  fix: string; // suggested action, may be empty
  nextRun: string | null;
  lastRun: string | null;
  lastSuccess: string | null;
  todayOk: number;
  todayFailed: number;
  /** Calls Microsoft throttled today; they are no failures. */
  todayThrottled: number;
  /** APIs left out for 24 hours after a 403 or 404. */
  pausedApis: number;
  /** When the engine next probes an automatically paused account; null when none is due. */
  autoResumeAt: string | null;
  autoResumeTries: number;
  usableApis: number;
  totalApis: number;
  secretExpires: string | null;
  paused: boolean;
}
export interface AccountInput {
  // shared by add and edit
  name: string;
  auth: AuthKind;
  tenant: string;
  clientId: string;
  username: string;
  categories: string[];
  secretExpires: string;
  secret?: string; // only sent on add or replace; the backend never returns it
  /** Take the secret automatic app setup already put in the vault under this name (app identity). */
  useStoredSecret?: boolean;
}
export interface LoginEvent {
  // pushed through the Wails event "login:<name>"
  kind: "device_code" | "browser_opened" | "progress" | "done" | "error";
  message: string;
  userCode?: string;
  verificationUri?: string;
}

// ---- automatic app registration (docs/design/app-setup.md section 7) ----
export interface AppSetupStart {
  name: string; // account name
  appOnly: boolean; // client secret identity; otherwise `auth` is the account's own sign-in
  auth: "device_code" | "browser";
  username: string; // user an app identity reads
  method: "device_code" | "browser"; // how the administrator signs in
}
export interface AppSetupApp { clientId: string; displayName: string }
export interface AppSetupAction { code: string; text: string }
export interface AppSetupPlan {
  kind: "delegated" | "app";
  tenantId: string;
  displayName: string;
  tags: string[];
  permissions: string[];
  missing: string[];
  existing: AppSetupApp | null;
  candidates: AppSetupApp[];
  actions: AppSetupAction[];
  needsChoice: boolean;
}
export interface AppSetupChoice { adopt: string; createNew: boolean; rename: boolean }
export interface AppSetupResult {
  kind: "delegated" | "app";
  tenantId: string;
  clientId: string;
  displayName: string;
  createdApp: boolean;
  adoptedApp: boolean;
  renamed: boolean;
  consentGranted: boolean;
  adminConsentUrl: string;
  consentError: string;
  secretExpires: string; // YYYY-MM-DD, app identity only; the secret itself stays in the vault
  secretCreated: boolean; // a client secret was created and stored in the vault
  removedSecrets: number; // this computer's earlier secrets deleted from the app
  notRemovedSecrets: number; // older ones left alone because they might belong to another account
}
/** What cancelling undid: a client secret nobody uses is deleted again; error names the one still live if that failed. */
export interface AppSetupCancelResult { secretRemoved: boolean; error: string }
export type AppSetupErrorCode = "forbidden" | "blocked" | "unknown_permission" | "cancelled" | "other";
export interface AppSetupEvent {
  // pushed through the Wails event "appsetup:<id>"
  kind: "device_code" | "browser_opened" | "signed_in" | "plan" | "progress" | "result" | "error";
  message: string;
  userCode?: string;
  verificationUri?: string;
  plan?: AppSetupPlan;
  result?: AppSetupResult;
  code?: AppSetupErrorCode;
  roles?: string[];
}
export interface LogEntry {
  ts: string;
  account: string;
  api: string;
  status: number;
  class: string;
  latencyMs: number;
  event?: string;
  detail?: string;
}
/** Calls of one API: of one account, or added up over all accounts. */
export interface APIStat {
  id: string;
  category: string;
  name: string;
  ok: number;
  failed: number;
  total: number;
  lastClass: string;
  lastStatus: number;
  lastAt: string | null;
}
export interface Settings {
  schedule: {
    intervalMin: string;
    intervalMax: string;
    apisPerRoundMin: number;
    apisPerRoundMax: number;
    randomOrder: boolean;
    autoPause: boolean;
    workDays: string[];
    workStart: string;
    workEnd: string;
    periodStart: string;
    periodEnd: string;
  };
  network: { proxy: string };
  notify: {
    staleAfter: string;
    events: string[];
    /** The channel "webhook" exists (read only). */
    webhookConfigured: boolean;
    /** How many notification channels exist (read only). */
    channels: number;
    /** Duration such as "6h"; "0" turns it off. */
    cooldown: string;
    /** "23:00-08:00", or "" for none. */
    quietHours: string;
    /** "21:00" sends the daily summary at that time; "" turns it off. */
    dailySummaryAt: string;
    spikeRounds: number;
    /** 0..1 */
    spikeRate: number;
    spikeMinFailures: number;
  };
  ui: { language: "auto" | "zh" | "en"; theme: "system" | "light" | "dark"; startAtLogin: boolean };
}
export type ChannelType =
  | "telegram" | "email" | "wecom" | "feishu" | "dingtalk" | "discord" | "slack" | "bark" | "ntfy" | "serverchan" | "webhook";
export type AlertLevel = "info" | "warning" | "critical";
/** A notification channel as it is edited. Secrets go in `secrets` and are never returned. */
export interface ChannelInput {
  id: string;
  type: ChannelType;
  minLevel: "" | AlertLevel;
  events: string[];
  language: "" | "zh" | "en";
  template: string;
  chatId: string;
  apiBase: string;
  smtpHost: string;
  smtpPort: number;
  security: "" | "starttls" | "tls" | "none";
  username: string;
  from: string;
  to: string[];
  server: string;
  topic: string;
  format: "" | "json" | "template";
  /** Exceptions kept from earlier setups: a webhook that may use http (the app can switch it off, never on) and a mail password sent without encryption (config.toml only). */
  allowHttp: boolean;
  allowPlainAuth: boolean;
  /** New secret values by field; a missing or empty one keeps what is stored. */
  secrets: Record<string, string>;
  /** Optional secret fields to remove. */
  clearSecrets: string[];
}
export interface SecretStatus {
  name: string;
  required: boolean;
  set: boolean;
}
export interface ChannelResult {
  at: string;
  ok: boolean;
  error?: string;
}
export type ChannelWarning = "server_http" | "email_plain" | "legacy" | "allow_http";
/** A channel for display: settings, which secrets are stored, how the last delivery went. */
export interface ChannelView extends ChannelInput {
  secretFields: SecretStatus[];
  legacy: boolean;
  lastResult: ChannelResult | null;
  warnings: ChannelWarning[];
}
export interface ChannelTest {
  id: string;
  ok: boolean;
  error: string;
}
export interface MessagePreview {
  title: string;
  body: string;
  html: string;
  format: "html" | "markdown" | "text" | "json";
}
export interface EmailPreset {
  name: string;
  host: string;
  port: number;
  security: "starttls" | "tls";
  note: string;
}
export interface ValidationError {
  field: string;
  message: string;
} // returned when save fails
export interface DoctorCheck {
  id: string;
  title: string;
  level: Level;
  /** English text; shown as is for a code this version cannot translate. */
  detail: string;
  fix: string;
  /** detail and fix as codes with values, translated in the UI language. */
  parts?: DoctorMsg[];
  fixParts?: DoctorMsg[];
}
export interface DoctorMsg {
  code: string;
  params: Record<string, string>;
}
export interface CatalogEntry {
  id: string;
  category: string;
  name: string;
  usable: boolean;
  scopes: string[];
}
export interface ServiceInfo {
  platform: string;
  installed: boolean;
  enabled: string;
  running: string;
  details: string[];
}

// ---- additions that the generated Go services define (not in spec §3) ----

/** AccountService.Test result. */
export interface TestResult {
  scopes: string[];
  usableApis: number;
  totalApis: number;
  expiresOn: string; // token expiry, RFC3339 or empty
}
/** AccountService.Recommend: the verdict on one API category. */
export type AdviceStatus = "recommended" | "unavailable" | "maybe";
export interface CategoryAdvice {
  category: string;
  status: AdviceStatus;
  /** "", or a code: no_mailbox, no_onedrive, no_teams_license, no_sharepoint, needs_admin_role, optional, not_supported, probe_failed, probe_arbitrary_user, missing_permission:<scopes> */
  reason: string;
  /** True when the category runs for an account without a categories setting (catalog default). */
  default: boolean;
}
/** AccountService.Recommend result. With needsLogin there is no advice. */
export interface RecommendResult {
  needsLogin: boolean;
  categories: CategoryAdvice[];
  recommended: string[];
}
/** HealthService.InstallService / UninstallService result. */
export interface ServiceResult {
  actions: string[]; // what was done
  warnings: string[];
  manual: string[]; // steps the user must do by hand
}
/** Payload of the Wails event "run:done". */
export interface RunDone {
  error: string; // empty when the round went through
}

/** A remark about an export or import: a code the interface translates, and its arguments. */
export interface BackupNote {
  code: string;
  args: string[];
}
/** Summary of a backup export. Names and counts only; never a secret or the webhook address. */
export interface BackupSummary {
  path: string;
  accounts: string[];
  vaultEntries: number;
  includesLogs: boolean;
  logFiles: number;
  /** English text, for the command line; the interface shows noteCodes. */
  notes: string[];
  noteCodes: BackupNote[];
}
export type ImportMode = "default" | "merge" | "replace";
/** Summary of a backup import. */
export interface ImportSummary {
  mode: ImportMode;
  accounts: string[];
  vaultEntries: number;
  sourceOS: string;
  sourceVersion: string;
  created: string;
  newMasterKey: boolean;
  /** English text, for the command line; the interface shows skippedCodes and noteCodes. */
  skipped: string[];
  notes: string[];
  skippedCodes: BackupNote[];
  noteCodes: BackupNote[];
  /** English next step for the command line; the interface has its own text. */
  hint: string;
}
/** A refusal or failure of an export or import, as a code the interface translates. */
export interface BackupProblem {
  code: string;
  names: string[];
  /** English detail from the backend; shown only for some codes. */
  detail: string;
}
/** BackupService results; cancelled is true when the user closed the file dialog. */
export interface BackupExportResult {
  cancelled: boolean;
  summary: BackupSummary;
  problem: BackupProblem | null;
}
export interface BackupImportResult {
  cancelled: boolean;
  summary: ImportSummary;
  problem: BackupProblem | null;
}

/** What a tenant's subscription check could do: read them, was refused (permission), failed, or has not run. */
export type SubscriptionAccess = "ok" | "no_permission" | "error" | "unchecked";
export interface RenewalView {
  from: string; // YYYY-MM-DD
  to: string; // YYYY-MM-DD
  seenAt: string; // RFC3339
}
export interface SubscriptionView {
  id: string;
  sku: string;
  /** False for subscriptions the settings do not watch (no alerts for them). */
  watched: boolean;
  /** Enabled, Warning, Suspended, LockedOut or Deleted. */
  status: string;
  isTrial: boolean;
  licenses: number;
  /** End date YYYY-MM-DD (UTC); empty when Microsoft gave none. */
  expires: string;
  daysLeft: number | null;
  checkedAt: string;
  renewals: RenewalView[];
}
export interface TenantSubscriptions {
  tenant: string;
  label: string;
  tenantId: string;
  accounts: string[];
  access: SubscriptionAccess;
  /** English reason of an "error" access; empty otherwise. */
  detail: string;
  checkedAt: string | null;
  attemptedAt: string | null;
  nextCheckAt: string | null;
  subscriptions: SubscriptionView[];
  /** The end date written by hand (YYYY-MM-DD), "" for none. */
  manualExpires: string;
  manualDaysLeft: number | null;
  /** The hand-written date stands in for missing data. */
  manualInUse: boolean;
  /** Real data exists, so the hand-written date can be removed. */
  manualRedundant: boolean;
  /** The account whose app registration lacks the permission, its identity and the command line that repairs it. */
  fixAccount: string;
  fixKind: "delegated" | "app";
  fixCommand: string;
  /** SKU prefixes that alert (subscription.skus; the default is DEVELOPERPACK). */
  watchedSkus: string[];
}
export interface SubscriptionRefresh {
  tenants: TenantSubscriptions[];
  /** A running engine had not answered yet; the tenants show what it saved before. */
  queued: boolean;
}

// ---- updates (docs/design/update.md) ----

/** What is known about the newest release and what this installation can do about it. */
export interface UpdateInfo {
  current: string;
  /** The newest version the channel offers; "" when none is known yet. */
  latest: string;
  available: boolean;
  prerelease: boolean;
  /** The page of that release (or the releases page). */
  url: string;
  channel: "stable" | "prerelease";
  /** A development build is never offered an upgrade. */
  dev: boolean;
  /** config [update] check: the daily automatic check. */
  checkEnabled: boolean;
  checkedAt: string | null; // RFC3339
  /** How the program was installed, and whether it can upgrade itself ("auto") or only point to the release page ("manual"). */
  kind: string;
  mode: "auto" | "manual";
  /** Why it is manual: docker, dev, no_key or unsupported. */
  reason: string;
}
/** One thing an upgrade did; code is stable (downloaded, verified, replaced, package_installed, installer_started, manual), text is English. */
export interface UpdateStep {
  code: string;
  arg?: string;
  text: string;
}
export interface UpdateResult {
  /** There was nothing newer to install. */
  upToDate: boolean;
  version: string;
  steps: UpdateStep[];
  /** Commands the person has to run because the program could not (no administrator rights, a folder it may not write to). */
  manual: string[];
  done: boolean;
  /** A setup program was started to replace this app: the window closes by itself. */
  exitNow: boolean;
  restartService: boolean;
  /** The tray and the window still run the old program. */
  restartApp: boolean;
  keepDir?: string;
  service: "" | "restarted" | "not_installed" | "not_running" | "failed";
  serviceNote: string;
}
/** A report of a running upgrade (event "update:progress"). */
export interface UpdateProgress {
  stage: "download" | "verify" | "install" | "done" | "error";
  file: string;
  done: number;
  total: number;
  result: UpdateResult | null;
  /** On "error": no_signature, no_key, bad_signature, checksum, manual_docker, manual_dev, manual_no_key, manual_unsupported or other. */
  code: string;
  error: string;
}
