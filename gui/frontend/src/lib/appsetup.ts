import type { AccountInput, AppSetupAction, AppSetupPlan } from "@/api/types";
import type { DictKey, TParams } from "@/i18n";

/** A translatable message: the dictionary key and its parameters. */
export interface Msg { key: DictKey; params?: TParams }

/**
 * Maps a progress line of the backend (English text) to a dictionary entry.
 * Unknown lines return null and are shown as they are.
 */
export function progressMsg(line: string): Msg | null {
  const quoted = /"([^"]*)"/.exec(line)?.[1] ?? "";
  if (/replicate/i.test(line)) return { key: "auto.p.wait" };
  if (/^creating the app registration/i.test(line)) return { key: "auto.p.create", params: { name: quoted } };
  if (/^updating the app registration/i.test(line)) return { key: "auto.p.update", params: { name: quoted } };
  if (/^creating the service principal/i.test(line)) return { key: "auto.p.sp" };
  if (/^granting administrator consent/i.test(line)) return { key: "auto.p.consent" };
  if (/^adding permissions to the administrator consent/i.test(line)) return { key: "auto.p.consentMore" };
  if (/^creating a client secret/i.test(line)) return { key: "auto.p.secret" };
  const grant = /^granting (\S+?)\.\.\.$/i.exec(line);
  if (grant) return { key: "auto.p.grant", params: { name: grant[1] } };
  return null;
}

/**
 * Maps an action of the plan (stable code) to a dictionary entry; null for a code this version does not know.
 * adoptName is the display name of the app the user would adopt, for "adopt_app".
 */
export function actionMsg(a: AppSetupAction, plan: AppSetupPlan, adoptName?: string): Msg | null {
  switch (a.code) {
    case "create_app":
      return { key: "auto.action.create_app", params: { name: plan.displayName } };
    case "update_app":
      return { key: "auto.action.update_app", params: { name: plan.existing?.displayName ?? plan.displayName } };
    case "adopt_or_create":
      return { key: "auto.action.adopt_or_create", params: { n: plan.candidates.length, name: plan.displayName } };
    case "adopt_app":
      return { key: "auto.action.adopt_app", params: { name: adoptName ?? plan.candidates[0]?.displayName ?? "" } };
    case "rename_app":
      return { key: "auto.action.rename_app", params: { name: plan.displayName } };
    case "set_notes":
      return { key: "auto.action.set_notes" };
    case "add_tags":
      return { key: "auto.action.add_tags", params: { tags: /add the tags (.+)$/i.exec(a.text)?.[1] ?? plan.tags.join(", ") } };
    case "add_redirect_uri":
      return { key: "auto.action.add_redirect_uri" };
    case "set_public_client":
      return { key: "auto.action.set_public_client" };
    case "add_permissions":
      return { key: "auto.action.add_permissions", params: { n: plan.missing.length } };
    case "create_service_principal":
      return { key: "auto.action.create_service_principal" };
    case "grant_consent":
      return { key: "auto.action.grant_consent" };
    case "assign_roles":
      return { key: "auto.action.assign_roles" };
    case "create_secret": {
      // "...; then delete this computer's N earlier secret(s) from the app"
      const old = /then delete this computer's (\d+) earlier/i.exec(a.text)?.[1];
      return old ? { key: "auto.action.create_secretReplace", params: { n: Number(old) } } : { key: "auto.action.create_secret" };
    }
    default:
      return null;
  }
}

/** The identity choices of the automatic path. "app" is the client secret identity. */
export type AutoIdentity = "device_code" | "browser" | "app";

export const authOfIdentity = (i: AutoIdentity): AccountInput["auth"] => (i === "app" ? "client_secret" : i);
export const identityOfAuth = (a: AccountInput["auth"]): AutoIdentity => (a === "client_secret" ? "app" : a === "browser" ? "browser" : "device_code");

const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$/;

/** What the first step of the automatic path needs: a name, and a user for the application identity. */
export function validateAutoStart(i: Pick<AccountInput, "name" | "auth" | "username">): Partial<Record<"name" | "username", "required" | "name">> {
  const e: Partial<Record<"name" | "username", "required" | "name">> = {};
  const name = i.name.trim();
  if (!NAME_RE.test(name)) e.name = name ? "name" : "required";
  if (i.auth === "client_secret" && !i.username.trim()) e.username = "required";
  return e;
}

/** A fresh session id: letters, digits and dashes, as the backend wants. */
export function newSetupId(): string {
  const c = globalThis.crypto;
  if (c?.randomUUID) return c.randomUUID();
  return `s-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
}
