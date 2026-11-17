import { describe, expect, it } from "vitest";
import { doctorKey, doctorMessage, doctorTitle } from "./doctor";
import { dictionaries, translate } from "@/i18n";
import codes from "./doctor-codes.json";

describe("doctorTitle", () => {
  it("maps fixed ids", () => {
    for (const id of ["config", "vault", "engine", "service", "linger", "clock", "network", "notify"]) {
      expect(doctorTitle(id)?.key).toBe(`doctor.${id}`);
    }
  });
  it("maps per-account ids and keeps dots in names", () => {
    expect(doctorTitle("account.lab.token")).toEqual({ key: "doctor.account.token", params: { name: "lab" } });
    expect(doctorTitle("account.a.b.secret")?.params?.name).toBe("a.b");
    const s = doctorTitle("account.main.scopes")!;
    expect(translate("zh", s.key, s.params)).toBe("main 的权限");
    expect(translate("en", s.key, s.params)).toBe("main permissions");
  });
  it("maps the subscription check of a tenant, whose name has dots", () => {
    const s = doctorTitle("subscription.contoso.onmicrosoft.com")!;
    expect(s).toEqual({ key: "doctor.subscription", params: { name: "contoso.onmicrosoft.com" } });
    expect(translate("zh", s.key, s.params)).toBe("contoso.onmicrosoft.com 的订阅");
    expect(doctorTitle("subscription.account:main")?.params?.name).toBe("main");
  });
  it("returns null for unknown ids", () => {
    expect(doctorTitle("future.check")).toBeNull();
    expect(doctorTitle("account.lab.other")).toBeNull();
  });
});

describe("doctor message codes", () => {
  // doctor-codes.json is kept in step with internal/doctor by a Go test.
  it("every code the backend can emit has a zh and an en text", () => {
    expect(codes.length).toBeGreaterThan(100);
    for (const code of codes) {
      for (const lang of ["zh", "en"] as const) {
        expect(doctorKey(code) in dictionaries[lang], `${lang}: ${code}`).toBe(true);
      }
    }
  });
  it("no dx key is left without a backend code", () => {
    const known = new Set(codes.map(doctorKey));
    const dx = Object.keys(dictionaries.zh).filter((k) => k.startsWith("dx."));
    expect(dx.filter((k) => !known.has(k))).toEqual([]);
  });
  it("translates codes with values, durations and relative days in the UI language", () => {
    const m = [{ code: "token.ok", params: { seconds: "3540" } }];
    expect(doctorMessage("zh", m, "x")).toBe("令牌正常，还可使用 59 分钟");
    expect(doctorMessage("en", m, "x")).toBe("token ok, valid for 59m");
    const sku = [{ code: "sub.skuEnds", params: { sku: "E5", status: "Enabled", date: "2027-01-01", offset: "3" } }];
    expect(doctorMessage("zh", sku, "x")).toBe("E5 Enabled，2027-01-01 到期（3 天后）");
    expect(doctorMessage("en", sku, "x")).toBe("E5 Enabled, ends 2027-01-01 (in 3 days)");
    expect(doctorMessage("zh", [{ code: "activity.last", params: { seconds: "1140" } }], "x")).toBe("上次成功调用在 19 分钟前");
  });
  it("joins several messages with the language's separator", () => {
    const m = [
      { code: "vault.secretMissing", params: { account: "a" } },
      { code: "vault.noMasterKey", params: { account: "b" } },
    ];
    expect(doctorMessage("zh", m, "x")).toBe("a：缺少密钥；b：没有可用的主密钥");
    expect(doctorMessage("en", m, "x")).toBe("a: the secret is missing; b: no master key available");
  });
  it("falls back to the backend text for unknown codes or no codes", () => {
    expect(doctorMessage("zh", [{ code: "ok.maybe" }, { code: "config.missing" }], "English text")).toBe("English text");
    expect(doctorMessage("zh", [], "English text")).toBe("English text");
    expect(doctorMessage("zh", undefined, "English text")).toBe("English text");
  });
  it("maps the title of an account's API check", () => {
    expect(doctorTitle("account.main.apis")?.key).toBe("doctor.account.apis");
  });
});
