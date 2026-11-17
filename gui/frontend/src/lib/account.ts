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

/** Union of scopes of the usable APIs, sorted. This is "what was granted" as far as the catalog can tell. */
export function grantedScopes(entries: { usable: boolean; scopes: string[] }[]): string[] {
  const s = new Set<string>();
  for (const e of entries) if (e.usable) e.scopes.forEach((x) => s.add(x));
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

/** What the backend accepts as an account name (it also refuses a few reserved device names). */
const EDIT_NAME_RE = /^[a-z0-9_-]{1,64}$/;

/** Validates the whole edit form at once. The sign-in kind cannot change, so a stored secret is kept when the field is empty. */
export function validateEditForm(i: AccountInput): Partial<Record<keyof AccountInput, AccountErrorCode>> {
  const e = validateAccountInput(i, "edit", "identity", i.auth);
  const name = i.name.trim();
  if (!name) e.name = "required";
  else if (!EDIT_NAME_RE.test(name)) e.name = "name";
  if (i.categories.length === 0) e.categories = "categories";
  return e;
}

/** True when saving would discard the saved sign-in: the tenant, the app or the user changed. */
export function identityChanged(orig: AccountInput, next: AccountInput): boolean {
  return orig.tenant.trim() !== next.tenant.trim() || orig.clientId.trim() !== next.clientId.trim() || orig.username.trim() !== next.username.trim();
}

/** The calls the edit form makes, in the order it makes them. */
export interface EditApi {
  rename(from: string, to: string): Promise<void>;
  update(input: AccountInput): Promise<void>;
  pause(name: string): Promise<void>;
  resume(name: string): Promise<void>;
}

export interface EditSaveResult {
  /** The name the account has now. */
  name: string;
  renamed: boolean;
  signInCleared: boolean;
}

/** A save that failed. `name` is what the account is called now: the new name when the rename went through. */
export class EditSaveError extends Error {
  constructor(message: string, readonly name: string, readonly renamed: boolean) {
    super(message);
  }
}

/**
 * Saves the edit form: rename first when the name changed, then the update under the (new) name, then the
 * paused state when it changed. Nothing is sent when a field is invalid (errors are returned instead).
 */
export async function saveAccountEdit(
  api: EditApi, original: AccountInput, input: AccountInput, paused: { was: boolean; now: boolean },
): Promise<{ errors: ReturnType<typeof validateEditForm> } | { result: EditSaveResult }> {
  const errors = validateEditForm(input);
  if (Object.keys(errors).length) return { errors };
  const payload: AccountInput = {
    ...input, name: input.name.trim(), tenant: input.tenant.trim(), clientId: input.clientId.trim(), username: input.username.trim(),
    secret: input.secret || undefined,
  };
  const renamed = payload.name !== original.name;
  let current = original.name;
  const fail = (e: unknown): never => {
    throw new EditSaveError(e instanceof Error ? e.message : String(e), current, current !== original.name);
  };
  if (renamed) {
    try {
      await api.rename(original.name, payload.name);
    } catch (e) {
      fail(e);
    }
    current = payload.name;
  }
  try {
    await api.update(payload);
    if (paused.was !== paused.now) await (paused.now ? api.pause(current) : api.resume(current));
  } catch (e) {
    fail(e);
  }
  return { result: { name: current, renamed, signInCleared: identityChanged(original, payload) } };
}
