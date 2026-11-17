import { describe, expect, it } from "vitest";
import type { Settings } from "@/api/types";
import { PACES, PACE_IDS, applyDays, applyPace, detectDays, detectPace } from "./pace";

const sched = (): Settings["schedule"] => ({
  intervalMin: "40m", intervalMax: "2h", apisPerRoundMin: 3, apisPerRoundMax: 8, randomOrder: false,
  workDays: ["mon", "tue", "wed", "thu", "fri"], workStart: "09:00", workEnd: "18:00", periodStart: "2026-01-01", periodEnd: "2026-12-31",
});

describe("detectPace", () => {
  it("recognises every preset after applying it", () => {
    for (const id of PACE_IDS) {
      const s = sched();
      applyPace(s, id);
      expect(detectPace(s)).toBe(id);
    }
  });
  it("treats equal durations written differently as the same", () => {
    const s = sched();
    s.intervalMin = "0h40m";
    s.intervalMax = "120m";
    expect(detectPace(s)).toBe("standard");
  });
  it("is custom when any value differs", () => {
    const s = sched();
    s.apisPerRoundMax = 9;
    expect(detectPace(s)).toBe("custom");
    const t = sched();
    t.intervalMin = "oops";
    expect(detectPace(t)).toBe("custom");
    const u = sched();
    u.apisPerRoundMin = NaN;
    expect(detectPace(u)).toBe("custom");
  });
  it("presets are distinct and ordered", () => {
    expect(new Set(PACE_IDS.map((i) => JSON.stringify(PACES[i]))).size).toBe(3);
  });
});

describe("applyPace", () => {
  it("changes only interval and APIs per round", () => {
    const s = sched();
    const before = structuredClone(s);
    applyPace(s, "frequent");
    expect(s).toEqual({ ...before, intervalMin: "20m", intervalMax: "1h", apisPerRoundMin: 5, apisPerRoundMax: 12 });
  });
});

describe("re-applying the selected choice", () => {
  it("keeps the user's spelling and order", () => {
    const s = sched();
    s.intervalMin = "0h40m";
    s.intervalMax = "120m";
    applyPace(s, "standard");
    expect([s.intervalMin, s.intervalMax]).toEqual(["0h40m", "120m"]);
    s.workDays = ["fri", "mon", "tue", "wed", "thu"];
    applyDays(s, "weekdays");
    expect(s.workDays).toEqual(["fri", "mon", "tue", "wed", "thu"]);
  });
});

describe("days", () => {
  it("detects every day and weekdays regardless of order", () => {
    expect(detectDays(["sun", "mon", "tue", "wed", "thu", "fri", "sat"])).toBe("every");
    expect(detectDays(["fri", "mon", "tue", "wed", "thu"])).toBe("weekdays");
    expect(detectDays(["mon", "sat"])).toBe("custom");
    expect(detectDays([])).toBe("custom");
  });
  it("applies a choice", () => {
    const s = sched();
    applyDays(s, "every");
    expect(detectDays(s.workDays)).toBe("every");
    applyDays(s, "weekdays");
    expect(s.workDays).toEqual(["mon", "tue", "wed", "thu", "fri"]);
  });
});
