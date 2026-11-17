import { describe, expect, it } from "vitest";
import { checkExportPassphrase, modeChoice, noteKey, problemKey, partKey, NOTE_CODES, PROBLEM_CODES, PART_CODES } from "./backup";
import { dictionaries } from "@/i18n";
import { translate, type DictKey } from "@/i18n";

describe("checkExportPassphrase", () => {
  it("accepts a long enough matching passphrase", () => {
    expect(checkExportPassphrase("12345678", "12345678")).toBeNull();
  });
  it("rejects empty, short and mismatching input, in that order", () => {
    expect(checkExportPassphrase("", "")).toBe("empty");
    expect(checkExportPassphrase("1234567", "1234567")).toBe("short");
    expect(checkExportPassphrase("12345678", "1234567x")).toBe("mismatch");
  });
  it("counts characters, not bytes", () => {
    expect(checkExportPassphrase("密码密码密码密码", "密码密码密码密码")).toBeNull();
    expect(checkExportPassphrase("密码密码密码", "密码密码密码")).toBe("short");
  });
});

describe("backend codes are translated", () => {
  it("has a text in both languages for every problem, note and step code", () => {
    const keys = [
      ...PROBLEM_CODES.map((c) => problemKey({ code: c, names: [], detail: "" })),
      ...NOTE_CODES.map((c) => noteKey({ code: c, args: [] })),
      ...PART_CODES.map(partKey),
    ];
    for (const k of keys) {
      expect(k).not.toBeNull();
      for (const [lang, d] of Object.entries(dictionaries)) expect(d[k as DictKey], `${lang}:${k}`).toBeTruthy();
    }
  });
  it("leaves unknown codes to the caller", () => {
    expect(problemKey({ code: "something_new", names: [], detail: "" })).toBeNull();
    expect(noteKey({ code: "something_new", args: [] })).toBeNull();
  });
  it("never shows a command line flag to the user", () => {
    for (const [lang, d] of Object.entries(dictionaries)) {
      for (const [k, v] of Object.entries(d)) {
        if (k.startsWith("backup.")) expect(v, `${lang}:${k}`).not.toMatch(/--|`/);
      }
    }
  });
  it("fills in the names of a merge conflict", () => {
    expect(translate("en", "backup.problem.merge_conflict", { names: "a, b" })).toContain("a, b");
    expect(translate("zh", "backup.problem.merge_conflict", { names: "a, b" })).toContain("a, b");
  });
});

describe("modeChoice", () => {
  it("offers only Replace when this machine cannot be inspected", () => {
    expect(modeChoice(null, true)).toBe("replaceOnly");
    expect(modeChoice(false, true)).toBe("replaceOnly");
  });
  it("lets the user choose only when there is data", () => {
    expect(modeChoice(true, false)).toBe("choose");
    expect(modeChoice(false, false)).toBe("default");
    expect(modeChoice(null, false)).toBe("default");
  });
});
