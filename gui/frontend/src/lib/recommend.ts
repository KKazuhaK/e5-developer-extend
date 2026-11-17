import type { CategoryAdvice, RecommendResult } from "@/api/types";
import type { Msg } from "@/lib/appsetup";

/**
 * Maps the reason code of an advice to a dictionary entry. A missing permission
 * carries the scope list after a colon. Unknown or empty codes give null.
 */
export function reasonMsg(reason: string): Msg | null {
  const i = reason.indexOf(":");
  const head = i < 0 ? reason : reason.slice(0, i);
  const rest = i < 0 ? "" : reason.slice(i + 1);
  switch (head) {
    case "missing_permission":
      return { key: "rec.reason.missing_permission", params: { scopes: rest.split(",").filter(Boolean).join(" / ") } };
    case "no_mailbox":
      return { key: "rec.reason.no_mailbox" };
    case "no_onedrive":
      return { key: "rec.reason.no_onedrive" };
    case "no_teams_license":
      return { key: "rec.reason.no_teams_license" };
    case "no_sharepoint":
      return { key: "rec.reason.no_sharepoint" };
    case "needs_admin_role":
      return { key: "rec.reason.needs_admin_role" };
    case "not_supported":
      return { key: "rec.reason.not_supported" };
    case "probe_failed":
      return { key: "rec.reason.probe_failed" };
    case "cannot_probe":
      return rest === "user"
        ? { key: "rec.reason.cannot_probe_user" }
        : { key: "rec.reason.cannot_probe_scopes", params: { scopes: rest.replace(/^scopes:/, "").split(",").filter(Boolean).join(" / ") } };
    case "probe_arbitrary_user":
      return { key: "rec.reason.probe_arbitrary_user" };
    default:
      return null;
  }
}

/**
 * The categories to select: every recommended one plus every "maybe" one the account has now.
 * current is the account's selection; empty means it runs on the catalog defaults (the advice's
 * `default` flag), so uncertain defaults stay ticked. Unavailable ones are never kept, and an
 * uncertain one is never dropped. Without a result only the current selection is returned.
 */
export function preselect(all: string[], rec: RecommendResult | null, current: string[] = []): string[] {
  if (!rec) return all.filter((c) => current.includes(c));
  const have = new Set(current);
  return all.filter((c) => {
    const a = rec.categories.find((x) => x.category === c);
    if (!a) return false;
    if (a.status === "recommended") return true;
    return a.status === "maybe" && (current.length ? have.has(c) : a.default);
  });
}

/** True when some category could not be checked because a probe failed (probably temporarily). */
export function hasFailedProbes(rec: RecommendResult | null): boolean {
  return !!rec?.categories.some((c) => c.reason === "probe_failed");
}

/** Advice by category id; empty without a result. */
export function adviceByCategory(rec: RecommendResult | null): Map<string, CategoryAdvice> {
  return new Map((rec?.categories ?? []).map((c) => [c.category, c]));
}

/**
 * How a selection differs from the advice: drop = selected categories found unavailable,
 * add = recommended categories not selected. Both follow the order of all.
 */
export function diffFromRecommended(all: string[], selected: string[], rec: RecommendResult | null): { drop: string[]; add: string[] } {
  if (!rec) return { drop: [], add: [] };
  const status = new Map(rec.categories.map((c) => [c.category, c.status]));
  const sel = new Set(selected);
  return {
    drop: all.filter((c) => sel.has(c) && status.get(c) === "unavailable"),
    add: all.filter((c) => !sel.has(c) && status.get(c) === "recommended"),
  };
}

/** True when both lists hold the same categories, ignoring order and case. */
export function sameCategories(a: string[], b: string[]): boolean {
  const n = (x: string[]) => [...new Set(x.map((c) => c.toLowerCase()))].sort();
  const [p, q] = [n(a), n(b)];
  return p.length === q.length && p.every((c, i) => c === q[i]);
}

/**
 * What to store in the account: exactly the catalog defaults is stored as an empty list, so the
 * account keeps following the defaults (new default categories apply by themselves); anything else
 * is stored as the explicit list, which does not pick up new categories automatically.
 */
export function categoriesToSave(selected: string[], defaults: string[]): string[] {
  return defaults.length > 0 && sameCategories(selected, defaults) ? [] : selected;
}
