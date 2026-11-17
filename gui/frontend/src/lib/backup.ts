import type { BackupNote, BackupProblem } from "@/api/types";
import type { DictKey } from "@/i18n";

/** The backend requires at least this many characters (code points) in a backup passphrase. */
export const MIN_PASSPHRASE = 8;

export type PassphraseCode = "empty" | "short" | "mismatch";

/** Checks an export passphrase and its confirmation; null when both are fine. */
export function checkExportPassphrase(pass: string, confirm: string): PassphraseCode | null {
  if (pass === "") return "empty";
  if ([...pass].length < MIN_PASSPHRASE) return "short";
  if (pass !== confirm) return "mismatch";
  return null;
}

/** Problem codes the backend sends; each has a `backup.problem.<code>` text. */
export const PROBLEM_CODES = [
  "not_backup", "newer_version", "bad_file", "wrong_passphrase", "too_large", "payload_too_large", "short_passphrase",
  "target_has_data", "merge_conflict", "file_exists", "local_config_broken", "invalid_backup", "save_failed",
  "rollback_failed", "rollback_failed_merge",
] as const;
/** Note codes of exports and imports; each has a `backup.note.<code>` text (skipped ones too). */
export const NOTE_CODES = [
  "state_damaged", "gui_damaged", "config_set_aside", "stale_not_removed", "stats_not_imported", "merge_settings", "merge_logs",
] as const;
/** Parts a `stats_not_imported` note names; each has a `backup.part.<name>` text. */
export const PART_CODES = ["state", "gui", "logs"] as const;

const known = (list: readonly string[], code: string) => list.includes(code);

export const problemKey = (p: BackupProblem) => (known(PROBLEM_CODES, p.code) ? (`backup.problem.${p.code}` as DictKey) : null);
export const noteKey = (n: BackupNote) => (known(NOTE_CODES, n.code) ? (`backup.note.${n.code}` as DictKey) : null);
export const partKey = (name: string) => (known(PART_CODES, name) ? (`backup.part.${name}` as DictKey) : null);

/** Codes whose English detail is worth showing under the translated text. */
export const SHOW_DETAIL = new Set(["invalid_backup", "save_failed", "rollback_failed", "rollback_failed_merge"]);

/** The import modes the interface offers, given what is known about this machine. */
export type ModeChoice = "default" | "choose" | "replaceOnly";
export function modeChoice(hasData: boolean | null, probeFailed: boolean): ModeChoice {
  if (probeFailed) return "replaceOnly";
  return hasData ? "choose" : "default";
}
