import { dictionaries, translate, type DictKey, type TParams } from "@/i18n";
import { formatDuration, type Lang } from "@/lib/time";

/** One message of a health check: a code from the backend and its values. */
export interface DoctorMsg {
  code: string;
  params?: Record<string, string> | null;
}

/** The dictionary key of a message code. */
export const doctorKey = (code: string): string => `dx.${code}`;

/** "3 天前" / "today" / "in 3 days", for a number of days from today. */
function whenPhrase(lang: Lang, offset: number): string {
  if (offset < 0) return translate(lang, "doctor.when.ago", { n: -offset });
  if (offset === 0) return translate(lang, "doctor.when.today");
  if (offset === 1) return translate(lang, "doctor.when.tomorrow");
  return translate(lang, "doctor.when.in", { n: offset });
}

/**
 * Fills the values a message carries: "seconds" becomes {dur} and {age} in
 * the UI language, "offset" (days from today) becomes {when}, and "count"
 * becomes a number so English picks the right plural.
 */
function messageParams(lang: Lang, params: Record<string, string> | null | undefined): TParams {
  const out: TParams = { ...(params ?? {}) };
  if (params?.count !== undefined && params.count !== "" && !Number.isNaN(Number(params.count))) out.count = Number(params.count);
  if (params?.seconds !== undefined) {
    const s = Math.max(0, Number(params.seconds) || 0);
    out.dur = formatDuration(s, lang);
    // An age reads like the CLI's: whole minutes once it is a minute or more,
    // and never negative when the clock is a little off.
    out.age = formatDuration(s >= 60 ? Math.round(s / 60) * 60 : s, lang);
  }
  if (params?.offset !== undefined) out.when = whenPhrase(lang, Number(params.offset));
  return out;
}

/**
 * Translates the messages of a check's detail or fix and joins them. When
 * there are none, or the backend sent a code this version has no text for, the
 * backend's own English text (fallback) is shown instead, whole.
 */
export function doctorMessage(lang: Lang, msgs: DoctorMsg[] | null | undefined, fallback: string): string {
  if (!msgs || msgs.length === 0) return fallback;
  const dict = dictionaries[lang];
  const out: string[] = [];
  for (const m of msgs) {
    const key = doctorKey(m.code);
    if (!(key in dict)) return fallback;
    out.push(translate(lang, key as DictKey, messageParams(lang, m.params)));
  }
  return out.join(lang === "zh" ? "；" : "; ");
}

export interface TitleSpec {
  key: DictKey;
  params?: Record<string, string>;
}

const FIXED: Record<string, DictKey> = {
  config: "doctor.config", vault: "doctor.vault", engine: "doctor.engine", service: "doctor.service",
  linger: "doctor.linger", clock: "doctor.clock", network: "doctor.network", notify: "doctor.notify", update: "doctor.update",
};
const PER_ACCOUNT: Record<string, DictKey> = {
  token: "doctor.account.token", scopes: "doctor.account.scopes", activity: "doctor.account.activity",
  state: "doctor.account.state", secret: "doctor.account.secret", apis: "doctor.account.apis",
};

/**
 * Maps a DoctorCheck.id to a localized title spec: the fixed ids, or
 * "account.<name>.<token|scopes|activity|state|secret>" or "subscription.<tenant>". Null for unknown ids
 * (the caller then shows the backend's English title).
 */
export function doctorTitle(id: string): TitleSpec | null {
  if (FIXED[id]) return { key: FIXED[id] };
  const m = /^account\.(.+)\.(token|scopes|activity|state|secret|apis)$/.exec(id);
  if (m) return { key: PER_ACCOUNT[m[2]], params: { name: m[1] } };
  const sub = /^subscription\.(.+)$/.exec(id);
  if (sub) return { key: "doctor.subscription", params: { name: sub[1].replace(/^account:/, "") } };
  return null;
}
