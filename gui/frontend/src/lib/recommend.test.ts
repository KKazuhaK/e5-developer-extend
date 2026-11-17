import { describe, expect, it } from "vitest";
import type { RecommendResult } from "@/api/types";
import { adviceByCategory, diffFromRecommended, hasFailedProbes, preselect, reasonMsg } from "./recommend";

const rec: RecommendResult = {
  needsLogin: false,
  recommended: ["user", "files"],
  categories: [
    { category: "mail", status: "unavailable", reason: "no_mailbox", default: true },
    { category: "user", status: "recommended", reason: "", default: true },
    { category: "reports", status: "maybe", reason: "needs_admin_role", default: true },
    { category: "files", status: "recommended", reason: "", default: true },
  ],
};

describe("reasonMsg", () => {
  it("maps every known code", () => {
    for (const code of ["no_mailbox", "no_onedrive", "no_teams_license", "no_sharepoint", "needs_admin_role", "not_supported", "probe_failed", "probe_arbitrary_user"]) {
      expect(reasonMsg(code)?.key, code).toBe(`rec.reason.${code}`);
    }
  });
  it("splits the scope list of a missing permission", () => {
    expect(reasonMsg("missing_permission:Mail.ReadBasic,Mail.Read")).toEqual({
      key: "rec.reason.missing_permission",
      params: { scopes: "Mail.ReadBasic / Mail.Read" },
    });
  });
  it("returns null for empty and unknown codes", () => {
    expect(reasonMsg("")).toBeNull();
    expect(reasonMsg("something_new")).toBeNull();
  });
});

describe("preselect", () => {
  const r2: RecommendResult = {
    needsLogin: false,
    recommended: ["user"],
    categories: [
      { category: "user", status: "recommended", reason: "", default: true },
      { category: "mail", status: "unavailable", reason: "no_mailbox", default: true },
      { category: "teams", status: "maybe", reason: "probe_failed", default: true },
      { category: "reports", status: "maybe", reason: "needs_admin_role", default: false },
    ],
  };
  const all = ["mail", "user", "teams", "reports"];
  it("ticks recommended ones and maybe ones that are catalog defaults when nothing is selected", () => {
    expect(preselect(all, r2)).toEqual(["user", "teams"]);
  });
  it("keeps only the maybe categories the account has now", () => {
    expect(preselect(all, r2, ["reports", "mail"])).toEqual(["user", "reports"]);
    expect(preselect(all, r2, ["user"])).toEqual(["user"]);
  });
  it("never keeps an unavailable category", () => {
    expect(preselect(all, r2, ["mail"])).not.toContain("mail");
  });
  it("ignores categories this version does not list", () => {
    expect(preselect(["user"], r2)).toEqual(["user"]);
  });
  it("without a result returns the current selection only", () => {
    expect(preselect(["a", "b"], null)).toEqual([]);
    expect(preselect(["a", "b"], null, ["b"])).toEqual(["b"]);
  });
  it("notices failed probes", () => {
    expect(hasFailedProbes(r2)).toBe(true);
    expect(hasFailedProbes({ ...r2, categories: r2.categories.slice(0, 2) })).toBe(false);
    expect(hasFailedProbes(null)).toBe(false);
  });
});

describe("adviceByCategory", () => {
  it("indexes by category", () => {
    expect(adviceByCategory(rec).get("mail")?.reason).toBe("no_mailbox");
    expect(adviceByCategory(null).size).toBe(0);
  });
});

describe("diffFromRecommended", () => {
  const all = ["mail", "user", "reports", "files"];
  it("is empty when the selection follows the advice", () => {
    expect(diffFromRecommended(all, ["user", "files"], rec)).toEqual({ drop: [], add: [] });
  });
  it("names unavailable categories to drop and recommended ones missing", () => {
    expect(diffFromRecommended(all, ["mail", "user"], rec)).toEqual({ drop: ["mail"], add: ["files"] });
  });
  it("does not flag maybe categories and needs a result", () => {
    expect(diffFromRecommended(all, ["user", "files", "reports"], rec)).toEqual({ drop: [], add: [] });
    expect(diffFromRecommended(all, ["mail"], null)).toEqual({ drop: [], add: [] });
  });
});
