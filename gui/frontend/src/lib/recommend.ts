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
    default:
      return null;
  }
}

/** The categories to tick by default: the recommended ones that exist, in the order of all. */
export function preselect(all: string[], rec: RecommendResult | null): string[] {
  if (!rec) return [...all];
  const ok = new Set(rec.categories.filter((c) => c.status === "recommended").map((c) => c.category));
  return all.filter((c) => ok.has(c));
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
