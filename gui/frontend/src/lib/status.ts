import type { AccountStatus, Level, Snapshot } from "@/api/types";

export type Tone = "success" | "warning" | "danger" | "muted" | "info";
/** Icon names are mapped to lucide components in components/StatusBadge.tsx. */
export type IconName = "clock" | "check" | "alert" | "pause" | "calendar-x" | "log-in" | "x" | "info" | "power";

export interface BadgeSpec {
  tone: Tone;
  icon: IconName;
  /** i18n key */
  label: `status.${DisplayStatus}` | `level.${Level}`;
}

/** AccountStatus plus "waiting": status ok, but the engine has not run the account yet. */
export type DisplayStatus = AccountStatus | "waiting";

/**
 * The status to show. "ok" with no run and no success yet is not "healthy":
 * nothing has proven it works, so it is shown as waiting for the first run.
 */
export function displayStatus(a: { status: AccountStatus; lastRun: string | null; lastSuccess: string | null }): DisplayStatus {
  return a.status === "ok" && a.lastRun == null && a.lastSuccess == null ? "waiting" : a.status;
}

/** True only for accounts that are ok and have actually run. */
export const isHealthy = (a: Parameters<typeof displayStatus>[0]) => displayStatus(a) === "ok";

/** Account status to badge. Never relies on colour alone: every tone has its own icon and text. */
export function statusBadge(status: DisplayStatus): BadgeSpec {
  switch (status) {
    case "waiting":
      return { tone: "info", icon: "clock", label: "status.waiting" };
    case "ok":
      return { tone: "success", icon: "check", label: "status.ok" };
    case "needs_attention":
      return { tone: "danger", icon: "alert", label: "status.needs_attention" };
    case "paused":
      return { tone: "muted", icon: "pause", label: "status.paused" };
    case "period_ended":
      return { tone: "muted", icon: "calendar-x", label: "status.period_ended" };
    case "not_signed_in":
      return { tone: "warning", icon: "log-in", label: "status.not_signed_in" };
  }
}

export function levelBadge(level: Level): BadgeSpec {
  switch (level) {
    case "ok":
      return { tone: "success", icon: "check", label: "level.ok" };
    case "warn":
      return { tone: "warning", icon: "alert", label: "level.warn" };
    case "fail":
      return { tone: "danger", icon: "x", label: "level.fail" };
  }
}

/** Statuses that need the user to do something (drives the banner and nav badge). */
export function needsAction(status: AccountStatus): boolean {
  return status === "needs_attention" || status === "not_signed_in";
}

export type EngineState = "running_gui" | "running_service" | "stopped";

export function engineState(e: Snapshot["engine"]): EngineState {
  if (!e.running || e.host === "none") return "stopped";
  return e.host === "service" ? "running_service" : "running_gui";
}

export function engineTone(s: EngineState): Tone {
  return s === "stopped" ? "muted" : "success";
}

/** Ordering for lists: problems first. */
const ORDER: Record<AccountStatus, number> = { needs_attention: 0, not_signed_in: 1, period_ended: 2, paused: 3, ok: 4 };
export function compareStatus(a: AccountStatus, b: AccountStatus): number {
  return ORDER[a] - ORDER[b];
}

/** A log row counts as failed when it is a call (not an engine event) that did not succeed. */
export function isFailedLog(e: { status: number; class: string; event?: string }): boolean {
  if (e.event) return false;
  if (e.class && e.class !== "ok") return true;
  return e.status === 0 || e.status >= 400;
}
