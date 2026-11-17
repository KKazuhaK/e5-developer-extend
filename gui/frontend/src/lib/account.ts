import type { AccountInput, AuthKind } from "@/api/types";
import { isValidDate } from "@/lib/settings";

export const AUTH_KINDS: AuthKind[] = ["device_code", "browser", "client_secret", "password"];

/**
 * Whether the secret must be entered. It is required when the account has no
 * stored secret of this kind yet: not saved at all (savedAuth null), or saved
 * with a different auth kind. Saved with the same kind, the stored one is kept
 * when the field is left empty.
 */
export function secretRequired(auth: AuthKind, savedAuth: AuthKind | null): boolean {
  return needsSecret(auth) && auth !== savedAuth;
}

export type AccountErrorCode = "required" | "name" | "guid" | "date" | "categories";

/** Which auth kinds carry a secret (client secret or password). */
export const needsSecret = (a: AuthKind) => a === "client_secret" || a === "password";
/** Which auth kinds need a user name (UPN): app permissions read /users/{user}, password sign-in needs it. */
export const needsUsername = (a: AuthKind) => a === "client_secret" || a === "password";
/** Which auth kinds sign in interactively (the login step shows progress). */
export const isInteractive = (a: AuthKind) => a === "device_code" || a === "browser";

const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$/;
const GUID_RE = /^[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$/;

/** Validates the fields of wizard step 2 (and categories for step 3). Returns field -> code. */
export function validateAccountInput(
  i: AccountInput, mode: "add" | "edit", step: "identity" | "categories", savedAuth?: AuthKind | null,
): Partial<Record<keyof AccountInput | "tenant", AccountErrorCode>> {
  const e: Partial<Record<keyof AccountInput, AccountErrorCode>> = {};
  if (step === "categories") {
    if (i.categories.length === 0) e.categories = "categories";
    return e;
  }
  if (mode === "add" && !NAME_RE.test(i.name.trim())) e.name = i.name.trim() ? "name" : "required";
  if (!i.tenant.trim()) e.tenant = "required";
  if (!i.clientId.trim()) e.clientId = "required";
  else if (!GUID_RE.test(i.clientId.trim())) e.clientId = "guid";
  if (needsUsername(i.auth) && !i.username.trim()) e.username = "required";
  // savedAuth undefined keeps the old rule: required when adding.
  const required = savedAuth === undefined ? mode === "add" && needsSecret(i.auth) : secretRequired(i.auth, savedAuth);
  if (required && !i.secret) e.secret = "required";
  if (i.secretExpires && !isValidDate(i.secretExpires)) e.secretExpires = "date";
  return e;
}

/**
 * Union of scopes of the usable APIs, sorted. This is "what was granted" as far as the catalog can tell.
 * An all-of group counts only when it is the entry's sole way in: next to any-of scopes a usable entry
 * does not say which of the two ways the account has.
 */
export function grantedScopes(entries: { usable: boolean; scopes: string[]; scopeSets?: string[][] }[]): string[] {
  const s = new Set<string>();
  for (const e of entries) {
    if (!e.usable) continue;
    e.scopes.forEach((x) => s.add(x));
    if (e.scopes.length === 0) e.scopeSets?.forEach((g) => g.forEach((x) => s.add(x)));
  }
  return [...s].sort();
}

export function summarizeByCategory(entries: { category: string; usable: boolean }[]): { category: string; usable: number; total: number }[] {
  const m = new Map<string, { usable: number; total: number }>();
  for (const e of entries) {
    const c = m.get(e.category) ?? { usable: 0, total: 0 };
    c.total++;
    if (e.usable) c.usable++;
    m.set(e.category, c);
  }
  return [...m].map(([category, v]) => ({ category, ...v }));
}

/** The engine's fixed wording for a refresh token ended by a Conditional Access sign-in frequency policy carries this code. */
export const SIGN_IN_FREQUENCY_CODE = "AADSTS70043";
export const isSignInFrequency = (reason: string | null | undefined): boolean => !!reason && reason.includes(SIGN_IN_FREQUENCY_CODE);
/** The reason without the long command-line hint that follows its first "; ". */
export const reasonHead = (reason: string): string => reason.split("; ")[0];
/**
 * Name suggested for the app identity that replaces `name`: `<name>-app`, with the base cut so that
 * the suffix survives the 32-character limit, and a counter when that name is already taken.
 */
export function appIdentityName(name: string, existing: readonly string[] = []): string {
  const taken = new Set([name, ...existing]);
  const make = (suffix: string) => name.slice(0, 32 - suffix.length) + suffix;
  let candidate = make("-app");
  for (let n = 2; taken.has(candidate); n++) candidate = make(`-app${n}`);
  return candidate;
}
