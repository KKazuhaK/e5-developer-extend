import { describe, expect, it } from "vitest";
import { daysUntil, formatLatency, formatRelative, formatWhen, resolveLang } from "./time";

const NOW = new Date(2026, 10, 16, 14, 0, 0).getTime();
const ago = (ms: number) => new Date(NOW - ms).toISOString();
const MIN = 60_000, H = 3_600_000, D = 86_400_000;

describe("formatRelative", () => {
  it("zh past", () => {
    expect(formatRelative(ago(10_000), "zh", NOW)).toBe("刚刚");
    expect(formatRelative(ago(3 * MIN), "zh", NOW)).toBe("3 分钟前");
    expect(formatRelative(ago(5 * H + 20 * MIN), "zh", NOW)).toBe("5 小时前");
    expect(formatRelative(ago(2 * D), "zh", NOW)).toBe("2 天前");
  });
  it("zh future", () => {
    expect(formatRelative(ago(-20_000), "zh", NOW)).toBe("即将");
    expect(formatRelative(ago(-12 * MIN), "zh", NOW)).toBe("12 分钟后");
    expect(formatRelative(ago(-3 * H), "zh", NOW)).toBe("3 小时后");
  });
  it("en past and future with plurals", () => {
    expect(formatRelative(ago(30_000), "en", NOW)).toBe("just now");
    expect(formatRelative(ago(MIN), "en", NOW)).toBe("1 min ago");
    expect(formatRelative(ago(2 * H), "en", NOW)).toBe("2 hours ago");
    expect(formatRelative(ago(H), "en", NOW)).toBe("1 hour ago");
    expect(formatRelative(ago(-D), "en", NOW)).toBe("in 1 day");
    expect(formatRelative(ago(-3 * D), "en", NOW)).toBe("in 3 days");
  });
  it("handles empty and invalid input", () => {
    expect(formatRelative(null, "zh", NOW)).toBe("—");
    expect(formatRelative("", "en", NOW)).toBe("—");
    expect(formatRelative("not a date", "en", NOW)).toBe("—");
  });
  it("boundaries round down", () => {
    expect(formatRelative(ago(59 * MIN + 59_000), "zh", NOW)).toBe("59 分钟前");
    expect(formatRelative(ago(H), "zh", NOW)).toBe("1 小时前");
  });
});

describe("formatWhen", () => {
  it("shows only the time for today", () => {
    expect(formatWhen(new Date(2026, 10, 16, 9, 5).toISOString(), NOW)).toBe("09:05");
  });
  it("adds the date for other days", () => {
    expect(formatWhen(new Date(2026, 10, 18, 23, 40).toISOString(), NOW)).toBe("11-18 23:40");
  });
  it("dash for null", () => expect(formatWhen(null, NOW)).toBe("—"));
});

describe("misc time helpers", () => {
  it("formatLatency", () => {
    expect(formatLatency(87.4)).toBe("87 ms");
    expect(formatLatency(1250)).toBe("1.25 s");
    expect(formatLatency(15_300)).toBe("15.3 s");
  });
  it("daysUntil", () => {
    expect(daysUntil(new Date(NOW + 12 * D).toISOString(), NOW)).toBe(12);
    expect(daysUntil(new Date(NOW - 2 * D).toISOString(), NOW)).toBe(-2);
    expect(daysUntil("", NOW)).toBeNull();
    expect(daysUntil("nope", NOW)).toBeNull();
  });
  it("resolveLang follows the system only for auto", () => {
    expect(resolveLang("auto", "zh-CN")).toBe("zh");
    expect(resolveLang("auto", "zh-Hant-TW")).toBe("zh");
    expect(resolveLang("auto", "en-US")).toBe("en");
    expect(resolveLang("auto", "fr")).toBe("en");
    expect(resolveLang("auto", undefined)).toBe("en");
    expect(resolveLang("en", "zh-CN")).toBe("en");
    expect(resolveLang("zh", "en-US")).toBe("zh");
  });
});
