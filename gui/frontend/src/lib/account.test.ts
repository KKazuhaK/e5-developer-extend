import { describe, expect, it } from "vitest";
import type { AccountInput } from "@/api/types";
import { EditSaveError, appIdentityName, identityChanged, saveAccountEdit, validateEditForm, isSignInFrequency, reasonHead, secretRequired, needsSecret, needsUsername, summarizeByCategory, validateAccountInput } from "./account";

const GUID = "0b4f6c1e-9a2d-4c7e-8f31-5d6a7b8c9d0e";
const input = (p: Partial<AccountInput> = {}): AccountInput => ({
  name: "main", auth: "device_code", tenant: "contoso.onmicrosoft.com", clientId: GUID, username: "", categories: ["mail"], secretExpires: "", ...p,
});

describe("validateAccountInput", () => {
  it("accepts a device-code account", () => {
    expect(validateAccountInput(input(), "add", "identity")).toEqual({});
  });
  it("checks name, tenant and client id", () => {
    const e = validateAccountInput(input({ name: "bad name!", tenant: " ", clientId: "123" }), "add", "identity");
    expect(e).toEqual({ name: "name", tenant: "required", clientId: "guid" });
    expect(validateAccountInput(input({ name: "" }), "add", "identity").name).toBe("required");
  });
  it("does not check the name when editing", () => {
    expect(validateAccountInput(input({ name: "" }), "edit", "identity")).toEqual({});
  });
  it("requires secret and username for client_secret and password accounts when adding", () => {
    const e = validateAccountInput(input({ auth: "client_secret" }), "add", "identity");
    expect(e.secret).toBe("required");
    expect(e.username).toBe("required");
    expect(validateAccountInput(input({ auth: "client_secret", username: "u@x.com" }), "edit", "identity")).toEqual({});
  });
  it("validates the optional expiry date", () => {
    expect(validateAccountInput(input({ secretExpires: "2026-02-31" }), "add", "identity").secretExpires).toBe("date");
    expect(validateAccountInput(input({ secretExpires: "2027-01-31" }), "add", "identity")).toEqual({});
  });
  it("needs at least one category", () => {
    expect(validateAccountInput(input({ categories: [] }), "add", "categories").categories).toBe("categories");
    expect(validateAccountInput(input(), "add", "categories")).toEqual({});
  });
});

describe("auth helpers", () => {
  it("know which kinds carry a secret", () => {
    expect(needsSecret("client_secret")).toBe(true);
    expect(needsSecret("password")).toBe(true);
    expect(needsSecret("device_code")).toBe(false);
    expect(needsUsername("browser")).toBe(false);
  });
});

describe("catalog summaries", () => {
  const entries = [
    { category: "mail", usable: true, scopes: ["Mail.Read", "Mail.ReadBasic"] },
    { category: "mail", usable: false, scopes: ["MailboxSettings.Read"] },
    { category: "user", usable: true, scopes: ["User.Read", "Mail.Read"] },
  ];
  it("summarizeByCategory counts usable and total", () => {
    expect(summarizeByCategory(entries)).toEqual([
      { category: "mail", usable: 1, total: 2 },
      { category: "user", usable: 1, total: 1 },
    ]);
  });
});

describe("secret requirement across save and edit", () => {
  const withSecret = (p: Partial<AccountInput> = {}) => input({ auth: "client_secret", username: "u@x.com", ...p });
  it("is required for an unsaved account", () => {
    expect(secretRequired("client_secret", null)).toBe(true);
    expect(validateAccountInput(withSecret(), "add", "identity", null).secret).toBe("required");
  });
  it("is optional once saved with the same auth kind (edit, or Back after saving)", () => {
    expect(secretRequired("client_secret", "client_secret")).toBe(false);
    expect(validateAccountInput(withSecret(), "edit", "identity", "client_secret").secret).toBeUndefined();
    expect(validateAccountInput(withSecret(), "add", "identity", "client_secret").secret).toBeUndefined();
  });
  it("is required when an existing account switches to a secret kind", () => {
    expect(secretRequired("password", "device_code")).toBe(true);
    expect(validateAccountInput(withSecret({ auth: "password" }), "edit", "identity", "device_code").secret).toBe("required");
    expect(validateAccountInput(withSecret({ auth: "password", secret: "pw" }), "edit", "identity", "device_code").secret).toBeUndefined();
  });
  it("is never required for kinds without a secret", () => {
    expect(secretRequired("device_code", null)).toBe(false);
    expect(secretRequired("browser", "client_secret")).toBe(false);
  });
});

describe("sign-in frequency cutoff", () => {
  const reason = "sign-in is cut off by a Conditional Access sign-in frequency policy (AADSTS70043); ways out, in order: (a) use the app identity";
  it("is recognised by its code", () => {
    expect(isSignInFrequency(reason)).toBe(true);
    expect(isSignInFrequency("login required; run: e5extend account login x")).toBe(false);
    expect(isSignInFrequency("")).toBe(false);
  });
  it("keeps the head of the reason only", () => {
    expect(reasonHead(reason)).toBe("sign-in is cut off by a Conditional Access sign-in frequency policy (AADSTS70043)");
  });
  it("suggests a valid name for the app identity", () => {
    expect(appIdentityName("main")).toBe("main-app");
  });
  it("keeps the suffix and a different name at 32 characters", () => {
    const long = "x".repeat(32);
    const n = appIdentityName(long);
    expect(n).toHaveLength(32);
    expect(n.endsWith("-app")).toBe(true);
    expect(n).not.toBe(long);
    expect(appIdentityName("x".repeat(28) + "-app")).not.toBe("x".repeat(28) + "-app");
  });
  it("counts up when the name is taken", () => {
    expect(appIdentityName("main", ["main-app"])).toBe("main-app2");
    expect(appIdentityName("main", ["main-app", "main-app2"])).toBe("main-app3");
    const taken = ["x".repeat(28) + "-app"];
    const n = appIdentityName("x".repeat(32), taken);
    expect(n).toHaveLength(32);
    expect(taken).not.toContain(n);
  });
});

describe("validateEditForm", () => {
  it("accepts an unchanged account and keeps the stored secret when the field is empty", () => {
    expect(validateEditForm(input({ auth: "client_secret", username: "u@x.com" }))).toEqual({});
  });
  it("checks every field at once", () => {
    const e = validateEditForm(input({ name: "Bad Name", tenant: "", clientId: "x", auth: "password", username: "", secretExpires: "nope", categories: [] }));
    expect(e).toEqual({ name: "name", tenant: "required", clientId: "guid", username: "required", secretExpires: "date", categories: "categories" });
  });
  it("uses the backend's name rule: lowercase, up to 64", () => {
    expect(validateEditForm(input({ name: "Work" })).name).toBe("name");
    expect(validateEditForm(input({ name: "a".repeat(65) })).name).toBe("name");
    expect(validateEditForm(input({ name: "a".repeat(64) })).name).toBeUndefined();
    expect(validateEditForm(input({ name: " " })).name).toBe("required");
  });
});

describe("identityChanged", () => {
  it("is true for a new tenant, client or user only", () => {
    expect(identityChanged(input(), input({ name: "other", categories: ["x"], secret: "s", secretExpires: "2030-01-01" }))).toBe(false);
    expect(identityChanged(input(), input({ tenant: "other.example" }))).toBe(true);
    expect(identityChanged(input(), input({ clientId: "0b4f6c1e-9a2d-4c7e-8f31-5d6a7b8c9d0f" }))).toBe(true);
    expect(identityChanged(input({ username: "a@x" }), input({ username: "b@x" }))).toBe(true);
    expect(identityChanged(input({ tenant: "t" }), input({ tenant: " t " }))).toBe(false);
  });
});

describe("saveAccountEdit", () => {
  const fake = (fail?: "rename" | "update" | "pause") => {
    const calls: string[] = [];
    return {
      calls,
      api: {
        rename: async (a: string, b: string) => { calls.push(`rename ${a}>${b}`); if (fail === "rename") throw new Error("exists"); return ["old entry kept"]; },
        update: async (i: AccountInput) => { calls.push(`update ${i.name}${i.secret ? " +secret" : ""}${i.categories.length ? "" : " defaults"}`); if (fail === "update") throw new Error("disk"); },
        pause: async (n: string) => { calls.push(`pause ${n}`); if (fail === "pause") throw new Error("busy"); },
        resume: async (n: string) => { calls.push(`resume ${n}`); },
      },
    };
  };
  const unpaused = { was: false, now: false };

  it("sends nothing while a field is invalid", async () => {
    const f = fake();
    const out = await saveAccountEdit(f.api, input(), input({ name: "new", tenant: "" }), unpaused);
    expect("errors" in out && out.errors.tenant).toBe("required");
    expect(f.calls).toEqual([]);
  });
  it("renames first, then updates under the new name", async () => {
    const f = fake();
    const out = await saveAccountEdit(f.api, input(), input({ name: " work ", secret: "s3" }), unpaused);
    expect(f.calls).toEqual(["rename main>work", "update work +secret"]);
    expect("result" in out && out.result).toEqual({ name: "work", renamed: true, signInCleared: false, warnings: ["old entry kept"] });
  });
  it("does not rename when the name is the same, and reports a discarded sign-in", async () => {
    const f = fake();
    const out = await saveAccountEdit(f.api, input(), input({ tenant: "other.example" }), unpaused);
    expect(f.calls).toEqual(["update main"]);
    expect("result" in out && out.result).toEqual({ name: "main", renamed: false, signInCleared: true, warnings: [] });
  });
  it("applies the paused toggle last, under the current name", async () => {
    const f = fake();
    await saveAccountEdit(f.api, input(), input({ name: "work" }), { was: false, now: true });
    expect(f.calls).toEqual(["rename main>work", "update work", "pause work"]);
    const g = fake();
    await saveAccountEdit(g.api, input(), input(), { was: true, now: false });
    expect(g.calls).toEqual(["update main", "resume main"]);
  });
  it("stops at a failed rename without updating", async () => {
    const f = fake("rename");
    await expect(saveAccountEdit(f.api, input(), input({ name: "work" }), unpaused)).rejects.toMatchObject({ message: "exists", account: "main", renamed: false, stage: "rename" });
    expect(f.calls).toEqual(["rename main>work"]);
  });
  it("says the rename went through when the update fails after it", async () => {
    const f = fake("update");
    const err = await saveAccountEdit(f.api, input(), input({ name: "work" }), unpaused).catch((e) => e);
    expect(err).toBeInstanceOf(EditSaveError);
    expect(err).toMatchObject({ message: "disk", account: "work", renamed: true, stage: "update" });
  });
  it("saves a selection equal to the defaults as an empty list, after validating it as ticked", async () => {
    const f = fake();
    const defaults = ["mail", "files"];
    const out = await saveAccountEdit(f.api, input({ categories: ["files", "mail"] }), input({ categories: ["files", "mail"] }), unpaused, defaults);
    expect("errors" in out).toBe(false);
    expect(f.calls).toEqual(["update main defaults"]);
    // An empty selection is still refused.
    const bad = await saveAccountEdit(f.api, input(), input({ categories: [] }), unpaused, defaults);
    expect("errors" in bad && bad.errors.categories).toBe("categories");
    expect(f.calls).toHaveLength(1);
  });
  it("reports a failed pause on its own, after the rename and the update went through", async () => {
    const f = fake("pause");
    const err = await saveAccountEdit(f.api, input(), input({ name: "work" }), { was: false, now: true }).catch((e) => e);
    expect(err).toMatchObject({ message: "busy", account: "work", renamed: true, stage: "paused" });
    expect(f.calls).toEqual(["rename main>work", "update work", "pause work"]);
  });
});
