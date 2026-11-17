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

/** Writes a preset into the schedule; touches only the interval and APIs-per-round fields. */
export function applyPace(s: Schedule, id: PaceId): void {
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
  s.workDays = c === "every" ? [...WEEKDAYS] : [...WORKDAYS];
}
