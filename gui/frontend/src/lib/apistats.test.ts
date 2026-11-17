import { describe, expect, it } from "vitest";
import type { APIStat } from "@/api/types";
import { nextSort, sortStats } from "./apistats";

const row = (id: string, ok: number, failed: number, lastClass = "ok"): APIStat => ({
  id, category: "user", name: id, ok, failed, total: ok + failed, lastClass, lastStatus: lastClass === "ok" ? 200 : 403, lastAt: null,
});
const rows = [row("b", 5, 0), row("a", 1, 4, "forbidden"), row("c", 9, 1)];
const label = (s: APIStat) => s.id;

describe("sortStats", () => {
  it("sorts by name, counts and last result, both ways", () => {
    expect(sortStats(rows, { key: "api", dir: "asc" }, label).map((r) => r.id)).toEqual(["a", "b", "c"]);
    expect(sortStats(rows, { key: "total", dir: "desc" }, label).map((r) => r.id)).toEqual(["c", "a", "b"]);
    expect(sortStats(rows, { key: "failed", dir: "desc" }, label).map((r) => r.id)).toEqual(["a", "c", "b"]);
    expect(sortStats(rows, { key: "ok", dir: "asc" }, label).map((r) => r.id)).toEqual(["a", "b", "c"]);
    expect(sortStats(rows, { key: "last", dir: "desc" }, label)[0].id).toBe("a");
  });
  it("does not change its input and keeps ties in label order", () => {
    const copy = structuredClone(rows);
    const tied = sortStats([row("z", 1, 0), row("y", 1, 0)], { key: "ok", dir: "desc" }, label);
    expect(tied.map((r) => r.id)).toEqual(["y", "z"]);
    sortStats(rows, { key: "ok", dir: "desc" }, label);
    expect(rows).toEqual(copy);
  });
});

describe("nextSort", () => {
  it("starts names ascending and counts descending, then toggles", () => {
    const cur = { key: "total", dir: "desc" } as const;
    expect(nextSort(cur, "api")).toEqual({ key: "api", dir: "asc" });
    expect(nextSort(cur, "ok")).toEqual({ key: "ok", dir: "desc" });
    expect(nextSort(cur, "total")).toEqual({ key: "total", dir: "asc" });
  });
});
