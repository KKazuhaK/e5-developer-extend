import { describe, expect, it } from "vitest";
import type { AccountInput } from "@/api/types";
import { appIdentityName, isSignInFrequency, reasonHead, secretRequired, grantedScopes, needsSecret, needsUsername, summarizeByCategory, validateAccountInput } from "./account";

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
  it("grantedScopes unions the scopes of usable APIs only", () => {
    expect(grantedScopes(entries)).toEqual(["Mail.Read", "Mail.ReadBasic", "User.Read"]);
  });
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
