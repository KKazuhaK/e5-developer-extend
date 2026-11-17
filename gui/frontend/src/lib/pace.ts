import type { Settings } from "@/api/types";
import { WEEKDAYS, parseDuration } from "./settings";

export type PaceId = "light" | "standard" | "frequent";
export type PaceChoice = PaceId | "custom";

interface PaceSpec {
  intervalMin: string;
  intervalMax: string;
  apisPerRoundMin: number;
  apisPerRoundMax: number;
}

/** Standard equals config.Default() in internal/config/config.go. */
export const PACES: Record<PaceId, PaceSpec> = {
  light: { intervalMin: "2h", intervalMax: "4h", apisPerRoundMin: 2, apisPerRoundMax: 5 },
  standard: { intervalMin: "40m", intervalMax: "2h", apisPerRoundMin: 3, apisPerRoundMax: 8 },
  frequent: { intervalMin: "20m", intervalMax: "1h", apisPerRoundMin: 5, apisPerRoundMax: 12 },
};
export const PACE_IDS = Object.keys(PACES) as PaceId[];

type Schedule = Settings["schedule"];

const sameDuration = (a: string, b: string) => {
  const x = parseDuration(a);
  return x !== null && x === parseDuration(b);
};

/** Which preset the schedule matches; "custom" when none does (durations compare by value, so 120m equals 2h). */
export function detectPace(s: Schedule): PaceChoice {
  for (const id of PACE_IDS) {
    const p = PACES[id];
    if (
      sameDuration(s.intervalMin, p.intervalMin) && sameDuration(s.intervalMax, p.intervalMax) &&
      s.apisPerRoundMin === p.apisPerRoundMin && s.apisPerRoundMax === p.apisPerRoundMax
    ) return id;
  }
  return "custom";
}

/** Fields applyPace and applyDays may overwrite, as the validators name them. */
export const PACE_FIELDS = ["schedule.intervalMin", "schedule.intervalMax", "schedule.apisPerRoundMin", "schedule.apisPerRoundMax"];
export const DAYS_FIELDS = ["schedule.workDays"];

/**
 * Writes a preset into the schedule; touches only the interval and
 * APIs-per-round fields. Choosing the preset that already matches changes
 * nothing, so "2h" is not rewritten as "120m" or the other way round.
 */
export function applyPace(s: Schedule, id: PaceId): void {
  if (detectPace(s) === id) return;
  Object.assign(s, PACES[id]);
}

export type DaysChoice = "every" | "weekdays" | "custom";
const WORKDAYS = WEEKDAYS.slice(0, 5);

export function detectDays(days: readonly string[]): DaysChoice {
  const has = (d: string) => days.includes(d);
  if (days.length === 7 && WEEKDAYS.every(has)) return "every";
  if (days.length === 5 && WORKDAYS.every(has)) return "weekdays";
  return "custom";
}

export function applyDays(s: Schedule, c: Exclude<DaysChoice, "custom">): void {
  if (detectDays(s.workDays) === c) return;
  s.workDays = c === "every" ? [...WEEKDAYS] : [...WORKDAYS];
}
