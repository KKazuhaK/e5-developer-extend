import { describe, expect, it } from "vitest";
import type { AccountStatus, Level } from "@/api/types";
import { compareStatus, engineState, engineTone, isFailedLog, levelBadge, needsAction, statusBadge } from "./status";

const STATUSES: AccountStatus[] = ["ok", "needs_attention", "paused", "period_ended", "not_signed_in"];

describe("statusBadge", () => {
  it("maps every status to a label key of the same name", () => {
    for (const s of STATUSES) expect(statusBadge(s).label).toBe(`status.${s}`);
  });
  it("uses tones that match severity", () => {
    expect(statusBadge("ok").tone).toBe("success");
    expect(statusBadge("needs_attention").tone).toBe("danger");
    expect(statusBadge("not_signed_in").tone).toBe("warning");
    expect(statusBadge("paused").tone).toBe("muted");
    expect(statusBadge("period_ended").tone).toBe("muted");
  });
  it("never relies on colour alone: icons are distinct for different tones", () => {
    const byTone = new Map<string, Set<string>>();
    for (const s of STATUSES) {
      const b = statusBadge(s);
      byTone.set(b.tone, (byTone.get(b.tone) ?? new Set()).add(b.icon));
    }
    const icons = STATUSES.map((s) => statusBadge(s).icon);
    expect(new Set(icons).size).toBe(STATUSES.length);
  });
});

describe("levelBadge", () => {
  it("maps ok, warn and fail to distinct icons", () => {
    const levels: Level[] = ["ok", "warn", "fail"];
    expect(new Set(levels.map((l) => levelBadge(l).icon)).size).toBe(3);
    expect(levelBadge("fail").tone).toBe("danger");
    expect(levelBadge("warn").label).toBe("level.warn");
  });
});

describe("needsAction and ordering", () => {
  it("flags only problems that the user must act on", () => {
    expect(STATUSES.filter(needsAction)).toEqual(["needs_attention", "not_signed_in"]);
  });
  it("sorts problems first", () => {
    const sorted = [...STATUSES].sort(compareStatus);
    expect(sorted[0]).toBe("needs_attention");
    expect(sorted[sorted.length - 1]).toBe("ok");
  });
});

describe("engineState", () => {
  it("distinguishes gui, service and stopped", () => {
    expect(engineState({ running: true, heartbeatAgeSec: 3, host: "gui" })).toBe("running_gui");
    expect(engineState({ running: true, heartbeatAgeSec: 3, host: "service" })).toBe("running_service");
    expect(engineState({ running: false, heartbeatAgeSec: null, host: "none" })).toBe("stopped");
    expect(engineState({ running: true, heartbeatAgeSec: 1, host: "none" })).toBe("stopped");
    expect(engineTone("stopped")).toBe("muted");
  });
});

describe("isFailedLog", () => {
  it("flags non-ok calls", () => {
    expect(isFailedLog({ status: 200, class: "ok" })).toBe(false);
    expect(isFailedLog({ status: 429, class: "throttled" })).toBe(true);
    expect(isFailedLog({ status: 401, class: "" })).toBe(true);
    expect(isFailedLog({ status: 0, class: "network" })).toBe(true);
  });
  it("never flags engine events", () => {
    expect(isFailedLog({ status: 0, class: "", event: "paused" })).toBe(false);
  });
});

import { displayStatus, isHealthy } from "./status";

describe("waiting for first run", () => {
  const acc = (status: AccountStatus, lastRun: string | null, lastSuccess: string | null) => ({ status, lastRun, lastSuccess });
  it("ok with no run and no success is waiting, not healthy", () => {
    const a = acc("ok", null, null);
    expect(displayStatus(a)).toBe("waiting");
    expect(isHealthy(a)).toBe(false);
    const b = statusBadge(displayStatus(a));
    expect(b.tone).toBe("info");
    expect(b.label).toBe("status.waiting");
    expect(b.icon).toBe("clock");
  });
  it("ok that has run is healthy", () => {
    expect(displayStatus(acc("ok", "2026-11-16T10:00:00Z", null))).toBe("ok");
    expect(isHealthy(acc("ok", null, "2026-11-16T10:00:00Z"))).toBe(true);
  });
  it("other statuses are unchanged", () => {
    for (const s of ["needs_attention", "paused", "period_ended", "not_signed_in"] as AccountStatus[]) {
      expect(displayStatus(acc(s, null, null))).toBe(s);
    }
  });
});
