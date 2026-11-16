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
}
export interface LoginEvent {
  // pushed through the Wails event "login:<name>"
  kind: "device_code" | "browser_opened" | "done" | "error";
  message: string;
  userCode?: string;
  verificationUri?: string;
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
export interface Settings {
  schedule: {
    intervalMin: string;
    intervalMax: string;
    apisPerRoundMin: number;
    apisPerRoundMax: number;
    randomOrder: boolean;
    workDays: string[];
    workStart: string;
    workEnd: string;
    periodStart: string;
    periodEnd: string;
  };
  network: { proxy: string };
  notify: { staleAfter: string; events: string[]; webhookConfigured: boolean };
  ui: { language: "auto" | "zh" | "en"; theme: "system" | "light" | "dark"; startAtLogin: boolean };
}
export interface ValidationError {
  field: string;
  message: string;
} // returned when save fails
export interface DoctorCheck {
  id: string;
  title: string;
  level: Level;
  detail: string;
  fix: string;
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
