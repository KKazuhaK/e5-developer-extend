import type { UpdateInfo, UpdateProgress, UpdateResult } from "@/api/types";
import type { DictKey } from "@/i18n";

/** The browser-storage key that remembers "Later" for one version. */
export const DISMISS_PREFIX = "update.dismissed.";

export interface KeyValueStore {
  get(key: string): string | null;
  set(key: string, value: string): void;
}

export const isDismissed = (s: KeyValueStore, version: string): boolean => version !== "" && s.get(DISMISS_PREFIX + version) === "1";
export const dismiss = (s: KeyValueStore, version: string): void => {
  if (version) s.set(DISMISS_PREFIX + version, "1");
};

/** The banner is shown for a new version that this person has not put off. */
export const bannerVisible = (info: UpdateInfo | null, dismissed: boolean): boolean => !!info && info.available && !info.dev && !dismissed;

/** How far a download is, 0 to 100; null when its size is unknown. */
export function percent(done: number, total: number): number | null {
  if (!(total > 0)) return null;
  return Math.max(0, Math.min(100, Math.round((done / total) * 100)));
}

/** "12.3 MB" for a byte count. */
export function formatBytes(n: number): string {
  if (!(n >= 0)) return "";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

export type InstallState =
  | { phase: "idle" }
  | { phase: "running"; progress: UpdateProgress | null }
  | { phase: "done"; result: UpdateResult }
  | { phase: "error"; code: string; message: string; result: UpdateResult | null };

/** Folds a report of the backend into the state of the upgrade. */
export function reduceProgress(state: InstallState, p: UpdateProgress): InstallState {
  switch (p.stage) {
    case "done":
      return p.result ? { phase: "done", result: p.result } : { phase: "idle" };
    case "error":
      return { phase: "error", code: p.code || "other", message: p.error, result: p.result };
    default:
      // A late report of an earlier stage must not move the bar back after the end.
      if (state.phase === "done" || state.phase === "error") return state;
      return { phase: "running", progress: p };
  }
}

const STAGE_KEYS: Record<string, DictKey> = {
  download: "update.stage.download",
  verify: "update.stage.verify",
  install: "update.stage.install",
};
/** The dictionary key of a stage; the download stage is the default before the first report. */
export const stageKey = (stage: string | undefined): DictKey => STAGE_KEYS[stage ?? ""] ?? "update.stage.download";

const ERROR_KEYS: Record<string, DictKey> = {
  no_signature: "update.error.no_signature",
  no_key: "update.error.no_key",
  bad_signature: "update.error.bad_signature",
  checksum: "update.error.checksum",
  manual_docker: "update.reason.docker",
  manual_dev: "update.reason.dev",
  manual_no_key: "update.error.no_key",
  manual_unsupported: "update.reason.unsupported",
};
/** The dictionary key that explains a failed upgrade, or null when only the backend's text is known. */
export const errorKey = (code: string): DictKey | null => ERROR_KEYS[code] ?? null;

const REASON_KEYS: Record<string, DictKey> = {
  docker: "update.reason.docker",
  dev: "update.reason.dev",
  no_key: "update.reason.no_key",
  unsupported: "update.reason.unsupported",
};
/** Why this installation cannot upgrade itself. */
export const reasonKey = (reason: string): DictKey => REASON_KEYS[reason] ?? "update.reason.unsupported";

const STEP_KEYS: Record<string, DictKey> = {
  downloaded: "update.step.downloaded",
  verified: "update.step.verified",
  replaced: "update.step.replaced",
  package_installed: "update.step.package_installed",
  installer_started: "update.step.installer_started",
  manual: "update.step.manual",
};
/** The dictionary key of a step, or null for a code this version has no text for (show the backend's English). */
export const stepKey = (code: string): DictKey | null => STEP_KEYS[code] ?? null;
