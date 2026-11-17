import { describe, expect, it } from "vitest";
import type { RecommendResult } from "@/api/types";
import { adviceByCategory, preselect, reasonMsg } from "./recommend";

const rec: RecommendResult = {
  needsLogin: false,
  recommended: ["user", "files"],
  categories: [
    { category: "mail", status: "unavailable", reason: "no_mailbox" },
    { category: "user", status: "recommended", reason: "" },
    { category: "reports", status: "maybe", reason: "needs_admin_role" },
    { category: "files", status: "recommended", reason: "" },
  ],
};

describe("reasonMsg", () => {
  it("maps every known code", () => {
    for (const code of ["no_mailbox", "no_onedrive", "no_teams_license", "no_sharepoint", "needs_admin_role", "not_supported", "probe_failed"]) {
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
  it("ticks only recommended categories, in the order of all", () => {
    expect(preselect(["mail", "files", "user", "reports"], rec)).toEqual(["files", "user"]);
  });
  it("ignores recommended categories this version does not list", () => {
    expect(preselect(["user"], rec)).toEqual(["user"]);
  });
  it("ticks everything without a result", () => {
    expect(preselect(["a", "b"], null)).toEqual(["a", "b"]);
  });
  it("ticks nothing when nothing is recommended", () => {
    expect(preselect(["mail"], { needsLogin: false, recommended: [], categories: [{ category: "mail", status: "unavailable", reason: "no_mailbox" }] })).toEqual([]);
  });
});

describe("adviceByCategory", () => {
  it("indexes by category", () => {
    expect(adviceByCategory(rec).get("mail")?.reason).toBe("no_mailbox");
    expect(adviceByCategory(null).size).toBe(0);
  });
});
