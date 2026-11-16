export type Lang = "zh" | "en";

const MIN = 60_000;
const HOUR = 3_600_000;
const DAY = 86_400_000;

const REL = {
  zh: {
    now: "刚刚",
    soon: "即将",
    past: (n: number, u: string) => `${n} ${u}前`,
    future: (n: number, u: string) => `${n} ${u}后`,
    unit: { m: "分钟", h: "小时", d: "天" },
    none: "—",
  },
  en: {
    now: "just now",
    soon: "any moment",
    past: (n: number, u: string) => `${n} ${u}${n === 1 ? "" : "s"} ago`,
    future: (n: number, u: string) => `in ${n} ${u}${n === 1 ? "" : "s"}`,
    unit: { m: "min", h: "hour", d: "day" },
    none: "—",
  },
} as const;

/**
 * "3 分钟前" / "in 2 hours". Under a minute is "刚刚" (past) or "即将" (future).
 * Null or unparsable input yields an em dash.
 */
export function formatRelative(iso: string | null | undefined, lang: Lang, now: number = Date.now()): string {
  const r = REL[lang];
  if (!iso) return r.none;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return r.none;
  const diff = t - now;
  const abs = Math.abs(diff);
  if (abs < MIN) return diff <= 0 ? r.now : r.soon;
  let n: number;
  let unit: string;
  if (abs < HOUR) {
    n = Math.floor(abs / MIN);
    unit = r.unit.m;
  } else if (abs < DAY) {
    n = Math.floor(abs / HOUR);
    unit = r.unit.h;
  } else {
    n = Math.floor(abs / DAY);
    unit = r.unit.d;
  }
  return diff < 0 ? r.past(n, unit) : r.future(n, unit);
}

const p2 = (n: number) => String(n).padStart(2, "0");

/** "14:20" today, "11-18 09:00" otherwise (local time). */
export function formatWhen(iso: string | null | undefined, now: number = Date.now()): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const n = new Date(now);
  const hm = `${p2(d.getHours())}:${p2(d.getMinutes())}`;
  const same = d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate();
  return same ? hm : `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${hm}`;
}

/** "11-16 14:20:05" local time, for the log table. */
export function formatLogTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
}

export function formatLatency(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)} s`;
}

/** Whole days from now until the date (negative when past). Null for empty or invalid. */
export function daysUntil(date: string | null | undefined, now: number = Date.now()): number | null {
  if (!date) return null;
  const t = Date.parse(date);
  if (Number.isNaN(t)) return null;
  return Math.ceil((t - now) / DAY);
}

/** Resolves the "auto" language setting from a navigator.language-like value. */
export function resolveLang(pref: "auto" | Lang, systemLang: string | undefined): Lang {
  if (pref !== "auto") return pref;
  return (systemLang ?? "").toLowerCase().startsWith("zh") ? "zh" : "en";
}
