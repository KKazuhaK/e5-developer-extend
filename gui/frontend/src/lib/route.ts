export const PAGES = ["overview", "accounts", "logs", "health", "settings", "about"] as const;
export type PageId = (typeof PAGES)[number];

export function isPage(v: unknown): v is PageId {
  return typeof v === "string" && (PAGES as readonly string[]).includes(v);
}

/** Reads ?page=<name> from a location.search string; null unless it is a known page. */
export function pageFromSearch(search: string): PageId | null {
  const v = new URLSearchParams(search).get("page");
  return isPage(v) ? v : null;
}
