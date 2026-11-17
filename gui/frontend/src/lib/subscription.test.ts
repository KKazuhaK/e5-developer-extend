import { describe, expect, it } from "vitest";
import type { TenantSubscriptions } from "@/api/types";
import { dictionaries } from "@/i18n";
import { daysTone, dateOwner, statusBadge, validDate, watched } from "./subscription";

const tenant = (p: Partial<TenantSubscriptions> = {}): TenantSubscriptions => ({
  tenant: "contoso.onmicrosoft.com", label: "contoso.onmicrosoft.com", tenantId: "", accounts: ["app", "main"], access: "ok", detail: "",
  checkedAt: null, attemptedAt: null, nextCheckAt: null, subscriptions: [], manualExpires: "", manualDaysLeft: null, manualInUse: false, manualRedundant: false, fixAccount: "app", fixKind: "delegated", fixCommand: "", watchedSkus: ["DEVELOPERPACK"], ...p,
});

describe("statusBadge", () => {
  it("has a translated label for every Graph status", () => {
    for (const s of ["Enabled", "Warning", "Suspended", "LockedOut", "Deleted"]) {
      const b = statusBadge(s);
      expect(b.label, s).not.toBeNull();
      expect(b.label! in dictionaries.zh && b.label! in dictionaries.en, s).toBe(true);
    }
  });
  it("is green only for Enabled and red for the states that cut service", () => {
    expect(statusBadge("Enabled").tone).toBe("success");
    expect(statusBadge("Warning").tone).toBe("warning");
    for (const s of ["Suspended", "LockedOut", "Deleted"]) expect(statusBadge(s).tone).toBe("danger");
    expect(statusBadge("Something").label).toBeNull();
  });
});

describe("daysTone", () => {
  it("follows the alert thresholds", () => {
    expect(daysTone(null)).toBe("muted");
    expect(daysTone(-3)).toBe("danger");
    expect(daysTone(7)).toBe("danger");
    expect(daysTone(8)).toBe("warning");
    expect(daysTone(30)).toBe("warning");
    expect(daysTone(31)).toBe("success");
  });
});

describe("watched", () => {
  it("keeps only the subscriptions that alert", () => {
    const sub = (id: string, w: boolean) => ({ id, sku: id, watched: w, status: "Enabled", isTrial: false, licenses: 1, expires: "", daysLeft: null, checkedAt: "", renewals: [] });
    expect(watched(tenant({ subscriptions: [sub("a", true), sub("b", false)] })).map((s) => s.id)).toEqual(["a"]);
  });
});

describe("validDate", () => {
  it("accepts real dates only", () => {
    expect(validDate("2027-02-14")).toBe(true);
    expect(validDate("2027-02-30")).toBe(false);
    expect(validDate("2027-2-1")).toBe(false);
    expect(validDate("")).toBe(false);
  });
  it("names the first account as the owner of the date", () => {
    expect(dateOwner(tenant())).toBe("app");
  });
});
