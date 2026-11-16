import { describe, expect, it } from "vitest";
import { isPage, pageFromSearch } from "./route";

describe("isPage", () => {
  it("accepts known pages only", () => {
    expect(isPage("health")).toBe(true);
    expect(isPage("Health")).toBe(false);
    expect(isPage("")).toBe(false);
    expect(isPage(undefined)).toBe(false);
    expect(isPage(null)).toBe(false);
    expect(isPage(3)).toBe(false);
    expect(isPage({ page: "logs" })).toBe(false);
  });
});

describe("pageFromSearch", () => {
  it("accepts every known page", () => {
    for (const p of ["overview", "accounts", "logs", "health", "settings", "about"]) {
      expect(pageFromSearch(`?page=${p}`)).toBe(p);
    }
  });
  it("works with other parameters and without the question mark", () => {
    expect(pageFromSearch("?empty=1&page=health")).toBe("health");
    expect(pageFromSearch("page=logs")).toBe("logs");
  });
  it("ignores unknown, empty or missing values", () => {
    expect(pageFromSearch("")).toBeNull();
    expect(pageFromSearch("?page=")).toBeNull();
    expect(pageFromSearch("?page=admin")).toBeNull();
    expect(pageFromSearch("?page=HEALTH")).toBeNull();
    expect(pageFromSearch("?other=health")).toBeNull();
  });
});
