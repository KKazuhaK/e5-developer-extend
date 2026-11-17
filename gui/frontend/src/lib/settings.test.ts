import { describe, expect, it } from "vitest";
import type { Settings } from "@/api/types";
import {
  errorsByField, isValidDate, isValidProxy, isValidTime, isValidWebhook, normalizeField, parseDuration, validateSettings,
} from "./settings";

const base = (): Settings => ({
  schedule: {
    intervalMin: "20m", intervalMax: "45m", apisPerRoundMin: 3, apisPerRoundMax: 6, randomOrder: true, autoPause: true,
    workDays: ["mon", "tue"], workStart: "09:00", workEnd: "18:00", periodStart: "", periodEnd: "",
  },
  network: { proxy: "" },
  notify: {
    staleAfter: "72h", events: [], webhookConfigured: false, channels: 0, cooldown: "6h", quietHours: "", dailySummaryAt: "",
    spikeRounds: 3, spikeRate: 0.5, spikeMinFailures: 5,
  },
  ui: { language: "auto", theme: "system", startAtLogin: false },
});

describe("parseDuration", () => {
  it("parses Go style durations", () => {
    expect(parseDuration("20m")).toBe(20 * 60_000);
    expect(parseDuration("1h30m")).toBe(90 * 60_000);
    expect(parseDuration("72h")).toBe(72 * 3_600_000);
    expect(parseDuration("90s")).toBe(90_000);
    expect(parseDuration("1.5h")).toBe(90 * 60_000);
  });
  it("rejects garbage", () => {
    for (const s of ["", "  ", "20", "m", "1h30", "abc", "-5m", "5 m"]) expect(parseDuration(s)).toBeNull();
  });
});

describe("format validators", () => {
  it("time", () => {
    expect(isValidTime("09:30")).toBe(true);
    expect(isValidTime("23:59")).toBe(true);
    expect(isValidTime("24:00")).toBe(false);
    expect(isValidTime("9:30")).toBe(false);
    expect(isValidTime("")).toBe(false);
  });
  it("date", () => {
    expect(isValidDate("2026-11-16")).toBe(true);
    expect(isValidDate("2026-02-30")).toBe(false);
    expect(isValidDate("2026-2-3")).toBe(false);
    expect(isValidDate("")).toBe(false);
  });
  it("proxy allows empty and known schemes only", () => {
    expect(isValidProxy("")).toBe(true);
    expect(isValidProxy("http://127.0.0.1:7890")).toBe(true);
    expect(isValidProxy("socks5://host:1080")).toBe(true);
    expect(isValidProxy("http://***@host:8080")).toBe(true); // redacted form round-trips
    expect(isValidProxy("ftp://host")).toBe(false);
    expect(isValidProxy("127.0.0.1:7890")).toBe(false);
  });
  it("webhook must be https", () => {
    expect(isValidWebhook("https://example.com/hook")).toBe(true);
    expect(isValidWebhook("http://example.com/hook")).toBe(false);
    expect(isValidWebhook("nonsense")).toBe(false);
  });
});

describe("validateSettings", () => {
  it("accepts valid settings", () => {
    expect(validateSettings(base())).toEqual({});
  });
  it("accepts all-day windows with equal times", () => {
    const s = base();
    s.schedule.workStart = s.schedule.workEnd = "00:00";
    expect(validateSettings(s)).toEqual({});
  });
  it("checks intervals", () => {
    const s = base();
    s.schedule.intervalMin = "1m";
    expect(validateSettings(s)["schedule.intervalMin"]).toBe("minInterval");
    s.schedule.intervalMin = "1h";
    expect(validateSettings(s)["schedule.intervalMin"]).toBe("intervalOrder");
    s.schedule.intervalMin = "x";
    expect(validateSettings(s)["schedule.intervalMin"]).toBe("duration");
  });
  it("checks APIs per round", () => {
    const s = base();
    s.schedule.apisPerRoundMin = 9;
    expect(validateSettings(s)["schedule.apisPerRoundMin"]).toBe("apisOrder");
    s.schedule.apisPerRoundMax = 0;
    expect(validateSettings(s)["schedule.apisPerRoundMax"]).toBe("posInt");
    s.schedule.apisPerRoundMin = NaN;
    expect(validateSettings(s)["schedule.apisPerRoundMin"]).toBe("posInt");
  });
  it("checks the work window and days", () => {
    const s = base();
    s.schedule.workEnd = "08:00";
    expect(validateSettings(s)["schedule.workEnd"]).toBe("timeOrder");
    s.schedule.workStart = "bad";
    expect(validateSettings(s)["schedule.workStart"]).toBe("time");
    s.schedule.workDays = [];
    expect(validateSettings(s)["schedule.workDays"]).toBe("days");
  });
  it("checks the period", () => {
    const s = base();
    s.schedule.periodStart = "2026-12-01";
    s.schedule.periodEnd = "2026-11-01";
    expect(validateSettings(s)["schedule.periodEnd"]).toBe("periodOrder");
    s.schedule.periodEnd = "2026-13-01";
    expect(validateSettings(s)["schedule.periodEnd"]).toBe("date");
  });
  it("checks proxy and stale duration", () => {
    const s = base();
    s.network.proxy = "not a url";
    s.notify.staleAfter = "soon";
    const e = validateSettings(s);
    expect(e["network.proxy"]).toBe("proxy");
    expect(e["notify.staleAfter"]).toBe("duration");
  });
});

describe("backend ValidationError field mapping", () => {
  it("normalizes config paths and camelCase alike", () => {
    expect(normalizeField("schedule.interval_min")).toBe("schedule.intervalMin");
    expect(normalizeField("schedule.apis_per_round_max")).toBe("schedule.apisPerRoundMax");
    expect(normalizeField("schedule.work_days[2]")).toBe("schedule.workDays");
    expect(normalizeField("notify.events[0]")).toBe("notify.events");
    expect(normalizeField("schedule.workStart")).toBe("schedule.workStart");
    expect(normalizeField("network.proxy")).toBe("network.proxy");
  });
  it("keeps the first message per field", () => {
    const m = errorsByField([
      { field: "schedule.work_days[0]", message: "a" },
      { field: "schedule.work_days[1]", message: "b" },
      { field: "network.proxy", message: "c" },
    ]);
    expect(m).toEqual({ "schedule.workDays": "a", "network.proxy": "c" });
  });
});

describe("validateSettings: alert noise", () => {
  const withNotify = (patch: Partial<Settings["notify"]>): Settings => ({ ...base(), notify: { ...base().notify, ...patch } });
  it("accepts the defaults, \"0\" and an empty quiet window", () => {
    expect(validateSettings(base())).toEqual({});
    expect(validateSettings(withNotify({ cooldown: "0" }))).toEqual({});
    expect(validateSettings(withNotify({ quietHours: "23:00-08:00" }))).toEqual({});
  });
  it("flags bad cooldown, quiet hours and spike values", () => {
    const e = validateSettings(withNotify({ cooldown: "soon", quietHours: "late", spikeRounds: 0, spikeRate: 1.5, spikeMinFailures: 0 }));
    expect(e["notify.cooldown"]).toBe("cooldown");
    expect(e["notify.quietHours"]).toBe("quiet");
    expect(e["notify.spikeRounds"]).toBe("spikeRounds");
    expect(e["notify.spikeRate"]).toBe("spikeRate");
    expect(e["notify.spikeMinFailures"]).toBe("spikeMin");
  });
  it("accepts an empty or valid daily summary time and flags the rest", () => {
    expect(validateSettings(withNotify({ dailySummaryAt: "21:00" }))).toEqual({});
    expect(validateSettings(withNotify({ dailySummaryAt: "" }))).toEqual({});
    expect(validateSettings(withNotify({ dailySummaryAt: "9pm" }))["notify.dailySummaryAt"]).toBe("time");
    expect(validateSettings(withNotify({ dailySummaryAt: "24:00" }))["notify.dailySummaryAt"]).toBe("time");
  });
  it("rejects a quiet window whose ends are equal", () => {
    expect(validateSettings(withNotify({ quietHours: "08:00-08:00" }))["notify.quietHours"]).toBe("quiet");
  });
});
