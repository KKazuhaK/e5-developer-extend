import type { AlertLevel, ChannelInput, ChannelType } from "@/api/types";
import { parseDuration } from "./settings";

export const CHANNEL_TYPES: ChannelType[] = [
  "telegram", "discord", "slack", "wecom", "feishu", "dingtalk", "bark", "ntfy", "serverchan", "email", "webhook",
];
/** The add dialog lists the types in these groups. */
export const CHANNEL_GROUPS: { id: "im" | "cn" | "push" | "other"; types: ChannelType[] }[] = [
  { id: "im", types: ["telegram", "discord", "slack"] },
  { id: "cn", types: ["wecom", "feishu", "dingtalk"] },
  { id: "push", types: ["bark", "ntfy", "serverchan"] },
  { id: "other", types: ["email", "webhook"] },
];
export const LEVELS: AlertLevel[] = ["info", "warning", "critical"];
/** Events a channel can be limited to, in the order the UI lists them. */
export const CHANNEL_EVENTS = [
  "needs_attention", "recovered", "auto_paused", "stale", "secret_expiring", "period_ended", "failure_spike", "all_failing",
  "subscription_expiring", "subscription_status", "subscription_renewed",
] as const;
/** What the preview can show: every event plus the test message and the quiet-hours summary. */
export const PREVIEW_EVENTS = [...CHANNEL_EVENTS, "test", "digest"] as const;

export type PlainKey =
  | "chatId" | "apiBase" | "smtpHost" | "smtpPort" | "security" | "username" | "from" | "to" | "server" | "topic" | "format";
export interface PlainField {
  key: PlainKey;
  kind: "text" | "number" | "select" | "list";
  required?: boolean;
  options?: string[];
  placeholder?: string;
}
/** The non-secret fields each type shows, in order. */
export const PLAIN_FIELDS: Record<ChannelType, PlainField[]> = {
  telegram: [{ key: "chatId", kind: "text", required: true, placeholder: "123456789" }, { key: "apiBase", kind: "text", placeholder: "https://" }],
  email: [
    { key: "smtpHost", kind: "text", required: true, placeholder: "smtp.example.com" },
    { key: "smtpPort", kind: "number", placeholder: "587" },
    { key: "security", kind: "select", options: ["starttls", "tls", "none"] },
    { key: "username", kind: "text" },
    { key: "from", kind: "text", required: true, placeholder: "alerts@example.com" },
    { key: "to", kind: "list", required: true, placeholder: "me@example.com, you@example.com" },
  ],
  wecom: [], feishu: [], dingtalk: [], discord: [], slack: [],
  bark: [{ key: "server", kind: "text", placeholder: "https://api.day.app" }],
  ntfy: [{ key: "server", kind: "text", placeholder: "https://ntfy.sh" }, { key: "topic", kind: "text", required: true }],
  serverchan: [],
  webhook: [{ key: "format", kind: "select", options: ["json", "template"] }],
};
/** The secret fields each type stores in the vault. */
export const SECRET_FIELDS: Record<ChannelType, { name: string; required: boolean }[]> = {
  telegram: [{ name: "bot_token", required: true }],
  email: [{ name: "password", required: false }],
  wecom: [{ name: "url", required: true }],
  feishu: [{ name: "url", required: true }, { name: "secret", required: false }],
  dingtalk: [{ name: "url", required: true }, { name: "secret", required: false }],
  discord: [{ name: "url", required: true }],
  slack: [{ name: "url", required: true }],
  bark: [{ name: "device_key", required: true }],
  ntfy: [{ name: "token", required: false }],
  serverchan: [{ name: "sendkey", required: true }],
  webhook: [{ name: "url", required: true }],
};

export function emptyChannel(type: ChannelType): ChannelInput {
  return {
    id: "", type, minLevel: "", events: [], language: "", template: "", chatId: "", apiBase: "", smtpHost: "", smtpPort: 0,
    security: type === "email" ? "starttls" : "", username: "", from: "", to: [], server: "", topic: "",
    format: type === "webhook" ? "json" : "", allowHttp: false, allowPlainAuth: false, secrets: {}, clearSecrets: [],
  };
}

/** A suggested id for a new channel of a type: the type, or the type with the next free number. */
export function suggestId(type: ChannelType, taken: string[]): string {
  if (!taken.includes(type)) return type;
  for (let i = 2; i < 100; i++) if (!taken.includes(`${type}-${i}`)) return `${type}-${i}`;
  return type;
}

export const ID_RE = /^[a-z0-9-]{1,32}$/;

export type ChannelCode = "id" | "idTaken" | "required" | "https" | "email" | "topic" | "port";
const EMAIL_RE = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/;
/** Pulls the address out of "Name <a@b.c>" as well as "a@b.c". */
const mailAddress = (s: string) => /<([^>]+)>\s*$/.exec(s.trim())?.[1] ?? s.trim();

function isHttpsUrl(s: string): boolean {
  try {
    const u = new URL(s.trim());
    return u.protocol === "https:" && !!u.host;
  } catch {
    return false;
  }
}

/**
 * Client side pre-check, mirroring the backend, so most mistakes are caught
 * before the round trip. Keys are field names as the form uses them
 * ("id", "chatId", "secrets.url"). The backend stays the authority.
 * `secretSet` says which secret fields are already stored (an edit may leave them empty).
 */
export function validateChannel(c: ChannelInput, opts: { creating: boolean; takenIds: string[]; secretSet: string[] }): Record<string, ChannelCode> {
  const e: Record<string, ChannelCode> = {};
  if (opts.creating) {
    if (!ID_RE.test(c.id)) e.id = "id";
    else if (opts.takenIds.includes(c.id)) e.id = "idTaken";
  }
  for (const f of PLAIN_FIELDS[c.type]) {
    if (!f.required) continue;
    const v = c[f.key];
    if (Array.isArray(v) ? v.length === 0 : typeof v === "string" ? !v.trim() : false) e[f.key] = "required";
  }
  if (c.type === "ntfy" && c.topic && !/^[A-Za-z0-9_-]{1,64}$/.test(c.topic)) e.topic = "topic";
  if (c.smtpPort < 0 || c.smtpPort > 65535 || !Number.isInteger(c.smtpPort)) e.smtpPort = "port";
  if (c.type === "email") {
    if (c.from.trim() && !EMAIL_RE.test(mailAddress(c.from))) e.from = "email";
    if (c.to.some((a) => !EMAIL_RE.test(mailAddress(a)))) e.to = "email";
  }
  if (c.type === "telegram" && c.apiBase.trim() && !isHttpsUrl(c.apiBase)) e.apiBase = "https";
  for (const f of SECRET_FIELDS[c.type]) {
    const v = (c.secrets[f.name] ?? "").trim();
    const stored = opts.secretSet.includes(f.name) && !c.clearSecrets.includes(f.name);
    if (!v && f.required && !stored) e[`secrets.${f.name}`] = "required";
    else if (v && f.name === "url" && !isHttpsUrl(v)) e[`secrets.${f.name}`] = "https";
  }
  return e;
}

/** Splits "a@b.c, d@e.f" (commas, semicolons, spaces or new lines) into a list. */
export function splitList(s: string): string[] {
  return s.split(/[,;\s]+/).map((x) => x.trim()).filter(Boolean);
}

/** "" or HH:MM-HH:MM with different ends. */
export function isValidQuietHours(s: string): boolean {
  if (!s) return true;
  const m = /^([01]\d|2[0-3]):([0-5]\d)-([01]\d|2[0-3]):([0-5]\d)$/.exec(s);
  return !!m && `${m[1]}${m[2]}` !== `${m[3]}${m[4]}`;
}

/** "0" or a positive Go duration. */
export function isValidCooldown(s: string): boolean {
  if (s.trim() === "0") return true;
  const d = parseDuration(s);
  return d !== null && d > 0;
}

/** Joins the two ends of a quiet window; an empty end gives "". */
export function quietHours(from: string, to: string): string {
  return from && to ? `${from}-${to}` : "";
}
export function splitQuietHours(s: string): [string, string] {
  const m = /^(\d\d:\d\d)-(\d\d:\d\d)$/.exec(s);
  return m ? [m[1], m[2]] : ["", ""];
}
