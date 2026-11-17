import type { Settings, ValidationError } from "@/api/types";

export const WEEKDAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"] as const;
export const NOTIFY_EVENTS = [
  "needs_attention", "recovered", "auto_paused", "stale", "secret_expiring", "period_ended", "failure_spike", "all_failing",
  "subscription_expiring", "subscription_status", "subscription_renewed", "daily_summary",
] as const;
/** Shortest allowed interval_min in the engine config. */
export const MIN_INTERVAL_MS = 5 * 60_000;

const UNIT_MS: Record<string, number> = { ns: 1e-6, us: 1e-3, µs: 1e-3, ms: 1, s: 1000, m: 60_000, h: 3_600_000 };

/** Parses a Go duration such as "20m", "1h30m", "90s". Returns milliseconds or null. */
export function parseDuration(input: string): number | null {
  const s = input.trim();
  if (!s) return null;
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/gy;
  let total = 0;
  let pos = 0;
  let m: RegExpExecArray | null;
  while (pos < s.length && (m = re.exec(s))) {
    total += parseFloat(m[1]) * UNIT_MS[m[2]];
    pos = re.lastIndex;
  }
  return pos === s.length && pos > 0 ? total : null;
}

export function isValidTime(s: string): boolean {
  return /^([01]\d|2[0-3]):[0-5]\d$/.test(s);
}

/** YYYY-MM-DD that is a real calendar date. */
export function isValidDate(s: string): boolean {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s);
  if (!m) return false;
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  const dt = new Date(Date.UTC(y, mo - 1, d));
  return dt.getUTCFullYear() === y && dt.getUTCMonth() === mo - 1 && dt.getUTCDate() === d;
}

export function isValidProxy(s: string): boolean {
  if (!s) return true;
  try {
    const u = new URL(s);
    return ["http:", "https:", "socks5:", "socks5h:"].includes(u.protocol) && !!u.host;
  } catch {
    return false;
  }
}

/** Webhook must be an https URL. */
export function isValidWebhook(s: string): boolean {
  try {
    const u = new URL(s.trim());
    return u.protocol === "https:" && !!u.host;
  } catch {
    return false;
  }
}

/**
 * Maps a backend field path to the form field id. The backend may send config
 * style paths ("schedule.interval_min", "schedule.work_days[1]") or camelCase
 * ones; both resolve to "schedule.intervalMin" / "schedule.workDays".
 */
export function normalizeField(field: string): string {
  return field
    .replace(/\[\d+\]/g, "")
    .replace(/_([a-z])/g, (_, c: string) => c.toUpperCase());
}

export function errorsByField(errs: ValidationError[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const e of errs) {
    const k = normalizeField(e.field);
    if (!out[k]) out[k] = e.message;
  }
  return out;
}

/** Message codes understood by the UI; the i18n layer turns them into text. */
export type LocalCode =
  | "duration" | "minInterval" | "intervalOrder" | "posInt" | "apisOrder" | "time" | "timeOrder"
  | "date" | "periodOrder" | "proxy" | "days" | "cooldown" | "quiet" | "spikeRounds" | "spikeRate" | "spikeMin";

/**
 * Client side pre-check, mirroring internal/config/validate.go so most
 * mistakes are caught before a round trip. Returns field -> code. The backend
 * remains the authority and its errors are shown as well.
 */
export function validateSettings(s: Settings): Record<string, LocalCode> {
  const e: Record<string, LocalCode> = {};
  const sc = s.schedule;
  const dmin = parseDuration(sc.intervalMin);
  const dmax = parseDuration(sc.intervalMax);
  if (dmin === null) e["schedule.intervalMin"] = "duration";
  else if (dmin < MIN_INTERVAL_MS) e["schedule.intervalMin"] = "minInterval";
  if (dmax === null || dmax <= 0) e["schedule.intervalMax"] = "duration";
  if (dmin !== null && dmax !== null && !e["schedule.intervalMin"] && dmin > dmax) e["schedule.intervalMin"] = "intervalOrder";

  if (!Number.isInteger(sc.apisPerRoundMax) || sc.apisPerRoundMax < 1) e["schedule.apisPerRoundMax"] = "posInt";
  if (!Number.isInteger(sc.apisPerRoundMin) || sc.apisPerRoundMin < 1) e["schedule.apisPerRoundMin"] = "posInt";
  else if (!e["schedule.apisPerRoundMax"] && sc.apisPerRoundMin > sc.apisPerRoundMax) e["schedule.apisPerRoundMin"] = "apisOrder";

  if (sc.workDays.length === 0) e["schedule.workDays"] = "days";
  if (!isValidTime(sc.workStart)) e["schedule.workStart"] = "time";
  if (!isValidTime(sc.workEnd)) e["schedule.workEnd"] = "time";
  if (!e["schedule.workStart"] && !e["schedule.workEnd"] && sc.workEnd < sc.workStart) e["schedule.workEnd"] = "timeOrder";

  if (sc.periodStart && !isValidDate(sc.periodStart)) e["schedule.periodStart"] = "date";
  if (sc.periodEnd && !isValidDate(sc.periodEnd)) e["schedule.periodEnd"] = "date";
  if (!e["schedule.periodStart"] && !e["schedule.periodEnd"] && sc.periodStart && sc.periodEnd && sc.periodEnd < sc.periodStart)
    e["schedule.periodEnd"] = "periodOrder";

  if (!isValidProxy(s.network.proxy)) e["network.proxy"] = "proxy";

  const stale = parseDuration(s.notify.staleAfter);
  if (stale === null || stale <= 0) e["notify.staleAfter"] = "duration";

  const n = s.notify;
  if (n.cooldown.trim() !== "0" && ((parseDuration(n.cooldown) ?? 0) <= 0)) e["notify.cooldown"] = "cooldown";
  if (n.quietHours && !/^([01]\d|2[0-3]):[0-5]\d-([01]\d|2[0-3]):[0-5]\d$/.test(n.quietHours)) e["notify.quietHours"] = "quiet";
  else if (n.quietHours && n.quietHours.slice(0, 5) === n.quietHours.slice(6)) e["notify.quietHours"] = "quiet";
  if (n.dailySummaryAt && !isValidTime(n.dailySummaryAt)) e["notify.dailySummaryAt"] = "time";
  if (!Number.isInteger(n.spikeRounds) || n.spikeRounds < 1 || n.spikeRounds > 50) e["notify.spikeRounds"] = "spikeRounds";
  if (!(n.spikeRate > 0 && n.spikeRate <= 1)) e["notify.spikeRate"] = "spikeRate";
  if (!Number.isInteger(n.spikeMinFailures) || n.spikeMinFailures < 1) e["notify.spikeMinFailures"] = "spikeMin";
  return e;
}
