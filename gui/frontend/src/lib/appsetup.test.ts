import { describe, expect, it } from "vitest";
import type { AppSetupPlan } from "@/api/types";
import { actionMsg, authOfIdentity, identityOfAuth, newSetupId, progressMsg, validateAutoStart } from "./appsetup";

const plan = (p: Partial<AppSetupPlan> = {}): AppSetupPlan => ({
  kind: "delegated", tenantId: "t", displayName: "E5 Developer Extend", tags: [], permissions: [], missing: [], existing: null, candidates: [], actions: [], needsChoice: false, ...p,
});

describe("progressMsg", () => {
  it("knows the lines the backend sends", () => {
    expect(progressMsg('creating the app registration "E5 Developer Extend"...')).toEqual({ key: "auto.p.create", params: { name: "E5 Developer Extend" } });
    expect(progressMsg('updating the app registration "e5renew"...')).toEqual({ key: "auto.p.update", params: { name: "e5renew" } });
    expect(progressMsg("creating the service principal...")?.key).toBe("auto.p.sp");
    expect(progressMsg("granting administrator consent...")?.key).toBe("auto.p.consent");
    expect(progressMsg("adding permissions to the administrator consent...")?.key).toBe("auto.p.consentMore");
    expect(progressMsg("granting User.Read.All...")).toEqual({ key: "auto.p.grant", params: { name: "User.Read.All" } });
    expect(progressMsg('creating a client secret "e5extend dev-box 2026-11-16"...')?.key).toBe("auto.p.secret");
  });
  it("recognises the wait for Microsoft in both places it comes from", () => {
    expect(progressMsg("waiting for Microsoft to replicate the new app (service principal), retrying in 3s...")?.key).toBe("auto.p.wait");
    expect(progressMsg("waiting for Microsoft to replicate the new app, retrying sign-in in 5s...")?.key).toBe("auto.p.wait");
  });
  it("leaves other lines alone", () => {
    expect(progressMsg("something new")).toBeNull();
  });
});

describe("actionMsg", () => {
  it("fills in names and counts from the plan", () => {
    expect(actionMsg({ code: "create_app", text: "" }, plan())?.params).toEqual({ name: "E5 Developer Extend" });
    expect(actionMsg({ code: "update_app", text: "" }, plan({ existing: { clientId: "c", displayName: "Mine" } }))?.params).toEqual({ name: "Mine" });
    const c = { clientId: "c", displayName: "e5renew" };
    expect(actionMsg({ code: "adopt_or_create", text: "" }, plan({ candidates: [c, c] }))?.params).toEqual({ n: 2, name: "E5 Developer Extend" });
    expect(actionMsg({ code: "grant_consent", text: "" }, plan())?.key).toBe("auto.action.grant_consent");
    expect(actionMsg({ code: "adopt_app", text: "" }, plan(), "e5renew")?.params).toEqual({ name: "e5renew" });
    expect(actionMsg({ code: "add_tags", text: "add the tags e5extend, e5extend:delegated" }, plan())?.params).toEqual({ tags: "e5extend, e5extend:delegated" });
    expect(actionMsg({ code: "add_permissions", text: "" }, plan({ missing: ["a", "b", "c"] }))?.params).toEqual({ n: 3 });
    for (const code of ["rename_app", "set_notes", "add_redirect_uri", "set_public_client", "create_service_principal", "assign_roles", "create_secret"]) {
      expect(actionMsg({ code, text: "" }, plan()), code).not.toBeNull();
    }
    expect(actionMsg({ code: "future_thing", text: "x" }, plan())).toBeNull();
  });
});

describe("identity", () => {
  it("maps to the account's auth kind and back", () => {
    expect(authOfIdentity("app")).toBe("client_secret");
    expect(authOfIdentity("browser")).toBe("browser");
    expect(identityOfAuth("client_secret")).toBe("app");
    expect(identityOfAuth("password")).toBe("device_code");
  });
});

describe("validateAutoStart", () => {
  it("needs a valid name, and a user for the application identity", () => {
    expect(validateAutoStart({ name: "main", auth: "device_code", username: "" })).toEqual({});
    expect(validateAutoStart({ name: "", auth: "device_code", username: "" })).toEqual({ name: "required" });
    expect(validateAutoStart({ name: "bad name", auth: "browser", username: "" })).toEqual({ name: "name" });
    expect(validateAutoStart({ name: "app1", auth: "client_secret", username: " " })).toEqual({ username: "required" });
  });
});

describe("newSetupId", () => {
  it("fits what the backend accepts", () => {
    expect(newSetupId()).toMatch(/^[A-Za-z0-9_-]{8,64}$/);
  });
});
