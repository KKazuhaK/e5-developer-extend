import type { APIStat } from "@/api/types";

export type StatKey = "api" | "last" | "ok" | "failed" | "total";
export interface StatSort {
  key: StatKey;
  dir: "asc" | "desc";
}

/** The first click on a column sorts the way people expect: names A-Z, counts large to small. */
export function nextSort(cur: StatSort, key: StatKey): StatSort {
  if (cur.key === key) return { key, dir: cur.dir === "asc" ? "desc" : "asc" };
  return { key, dir: key === "api" || key === "last" ? "asc" : "desc" };
}

/** Whether the last call of the API worked. An API that was never called counts as not worked. */
export const lastWorked = (s: APIStat): boolean => s.lastClass === "ok";

/**
 * Sorts a copy of the statistics. `label` gives the text of the API column (the
 * localized category and endpoint name), so the order follows what is shown.
 * Ties fall back to the label so the order is stable.
 */
export function sortStats(rows: readonly APIStat[], sort: StatSort, label: (s: APIStat) => string): APIStat[] {
  const num = (s: APIStat): number | string => {
    switch (sort.key) {
      case "ok": return s.ok;
      case "failed": return s.failed;
      case "total": return s.total;
      case "last": return lastWorked(s) ? 0 : 1; // worked first when ascending
      default: return label(s);
    }
  };
  const sign = sort.dir === "asc" ? 1 : -1;
  return [...rows].sort((a, b) => {
    const x = num(a);
    const y = num(b);
    const c = typeof x === "number" && typeof y === "number" ? x - y : String(x).localeCompare(String(y));
    return c !== 0 ? c * sign : label(a).localeCompare(label(b));
  });
}
