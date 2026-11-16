import { describe, expect, it } from "vitest";
import { doctorTitle } from "./doctor";
import { translate } from "@/i18n";

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
  it("returns null for unknown ids", () => {
    expect(doctorTitle("future.check")).toBeNull();
    expect(doctorTitle("account.lab.other")).toBeNull();
  });
});
