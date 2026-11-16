import { describe, expect, it } from "vitest";
import { dictionaries, translate, translateLoose } from "./index";
import { enOne } from "./en";
import { zh } from "./zh";
import { en } from "./en";

import { AppName } from "@/config";

const placeholders = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();

describe("dictionaries", () => {
  it("every key exists in both languages", () => {
    const z = Object.keys(zh).sort();
    const e = Object.keys(en).sort();
    expect(e.filter((k) => !z.includes(k))).toEqual([]);
    expect(z.filter((k) => !e.includes(k))).toEqual([]);
  });
  it("no empty strings", () => {
    for (const [lang, d] of Object.entries(dictionaries)) {
      for (const [k, v] of Object.entries(d)) expect(v.trim(), `${lang}:${k}`).not.toBe("");
    }
  });
  it("placeholders match between languages", () => {
    for (const k of Object.keys(zh) as (keyof typeof zh)[]) {
      expect(placeholders(en[k]), k).toEqual(placeholders(zh[k]));
    }
  });
  it("never hard-codes the product name", () => {
    for (const d of Object.values(dictionaries)) {
      for (const [k, v] of Object.entries(d)) expect(v.includes(AppName), k).toBe(false);
    }
  });
});

describe("plurals", () => {
  it("uses the English singular only for count 1", () => {
    expect(translate("en", "overview.attention", { count: 1 })).toBe("1 account needs attention");
    expect(translate("en", "overview.attention", { count: 2 })).toBe("2 accounts need attention");
    expect(translate("en", "overview.attention", { count: 0 })).toBe("0 accounts need attention");
    expect(translate("en", "logs.summary", { count: 1, failed: 0 })).toBe("1 entry, 0 failed");
  });
  it("zh has a single form", () => {
    expect(translate("zh", "overview.attention", { count: 1 })).toBe("有 1 个账号需要处理");
  });
  it("singular forms refer to real keys and keep the placeholders", () => {
    for (const [k, v] of Object.entries(enOne)) {
      expect(k in en, k).toBe(true);
      expect(placeholders(v!), k).toEqual(placeholders(en[k as keyof typeof en]));
    }
  });
});

describe("translate", () => {
  it("fills params and the app name", () => {
    expect(translate("zh", "toast.paused", { name: "main" })).toBe("已暂停 main");
    expect(translate("en", "health.serviceDesc")).toContain(AppName);
  });
  it("keeps unknown placeholders visible", () => {
    expect(translate("en", "toast.paused")).toBe("Paused {name}");
  });
  it("translateLoose falls back for unknown keys", () => {
    expect(translateLoose("zh", "cat.mail", "x")).toBe("邮件");
    expect(translateLoose("zh", "cat.unknown", "fallback")).toBe("fallback");
  });
});
