import { describe, expect, it } from "vitest";
import type { AccountSummary, TenantSubscriptions } from "@/api/types";
import { dictionaries } from "@/i18n";
import { daysTone, dateOwner, fixPermissionsFrom, statusBadge, validDate, watched } from "./subscription";

const tenant = (p: Partial<TenantSubscriptions> = {}): TenantSubscriptions => ({
  tenant: "contoso.onmicrosoft.com", label: "contoso.onmicrosoft.com", tenantId: "", accounts: ["app", "main"], access: "ok", detail: "",
  checkedAt: null, attemptedAt: null, nextCheckAt: null, subscriptions: [], manualExpires: "", manualDaysLeft: null, manualInUse: false, manualRedundant: false, ...p,
});
const acct = (name: string, auth: AccountSummary["auth"]) => ({ name, auth }) as AccountSummary;

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

describe("fixPermissionsFrom", () => {
  it("starts from an account that is not an application identity", () => {
    const accounts = [acct("app", "client_secret"), acct("main", "device_code")];
    expect(fixPermissionsFrom(tenant(), accounts)).toBe("main");
  });
  it("has none when every account is an application identity", () => {
    expect(fixPermissionsFrom(tenant({ accounts: ["app"] }), [acct("app", "client_secret")])).toBeNull();
    expect(fixPermissionsFrom(tenant({ accounts: ["gone"] }), [])).toBeNull();
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
