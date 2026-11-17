import { describe, expect, it } from "vitest";
import type { UpdateInfo, UpdateProgress, UpdateResult } from "@/api/types";
import { dictionaries } from "@/i18n";
import {
  DISMISS_PREFIX, bannerVisible, dismiss, errorKey, formatBytes, isDismissed, percent, reasonKey, reduceProgress, stageKey, stepKey,
  type InstallState, type KeyValueStore,
} from "./update";

const info = (over: Partial<UpdateInfo> = {}): UpdateInfo => ({
  current: "0.2.0", latest: "0.3.0", available: true, prerelease: false, url: "https://example.test/v0.3.0", channel: "stable",
  dev: false, checkEnabled: true, checkedAt: null, kind: "linux-standalone", mode: "auto", reason: "", ...over,
});
const result = (over: Partial<UpdateResult> = {}): UpdateResult => ({
  upToDate: false, version: "0.3.0", steps: [], manual: [], done: true, exitNow: false, restartService: true, restartApp: false,
  service: "", serviceNote: "", ...over,
});
const progress = (over: Partial<UpdateProgress>): UpdateProgress => ({ stage: "download", file: "", done: 0, total: 0, result: null, code: "", error: "", ...over });

function memory(): KeyValueStore & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return { data, get: (k) => data.get(k) ?? null, set: (k, v) => void data.set(k, v) };
}

describe("dismissing a version", () => {
  it("remembers 'later' per version", () => {
    const s = memory();
    expect(isDismissed(s, "0.3.0")).toBe(false);
    dismiss(s, "0.3.0");
    expect(isDismissed(s, "0.3.0")).toBe(true);
    expect(isDismissed(s, "0.4.0")).toBe(false); // the next release is news again
    expect(s.data.has(DISMISS_PREFIX + "0.3.0")).toBe(true);
  });
  it("never dismisses the empty version", () => {
    const s = memory();
    dismiss(s, "");
    expect(s.data.size).toBe(0);
    expect(isDismissed(s, "")).toBe(false);
  });
});

describe("bannerVisible", () => {
  it("shows for a new version nobody put off", () => {
    expect(bannerVisible(info(), false)).toBe(true);
  });
  it("hides when dismissed, up to date, a development build or not loaded", () => {
    expect(bannerVisible(info(), true)).toBe(false);
    expect(bannerVisible(info({ available: false }), false)).toBe(false);
    expect(bannerVisible(info({ dev: true }), false)).toBe(false);
    expect(bannerVisible(null, false)).toBe(false);
  });
});

describe("progress numbers", () => {
  it("computes a percentage and tolerates an unknown size", () => {
    expect(percent(50, 200)).toBe(25);
    expect(percent(300, 200)).toBe(100);
    expect(percent(0, 0)).toBeNull();
    expect(percent(5, -1)).toBeNull();
  });
  it("formats sizes", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(2048)).toBe("2 KB");
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MB");
  });
});

describe("reduceProgress", () => {
  it("follows the stages to a result", () => {
    let s: InstallState = { phase: "idle" };
    s = reduceProgress(s, progress({ stage: "download", done: 10, total: 100 }));
    expect(s).toMatchObject({ phase: "running", progress: { stage: "download", done: 10 } });
    s = reduceProgress(s, progress({ stage: "verify" }));
    expect(s).toMatchObject({ phase: "running", progress: { stage: "verify" } });
    s = reduceProgress(s, progress({ stage: "done", result: result() }));
    expect(s).toMatchObject({ phase: "done", result: { version: "0.3.0" } });
  });
  it("keeps the end when a late report arrives", () => {
    const done = reduceProgress({ phase: "running", progress: null }, progress({ stage: "done", result: result() }));
    expect(reduceProgress(done, progress({ stage: "install" }))).toBe(done);
    const err = reduceProgress({ phase: "running", progress: null }, progress({ stage: "error", code: "checksum", error: "bad" }));
    expect(err).toMatchObject({ phase: "error", code: "checksum", message: "bad" });
    expect(reduceProgress(err, progress({ stage: "download" }))).toBe(err);
  });
  it("falls back to 'other' for an error without a code and to idle for a done without a result", () => {
    expect(reduceProgress({ phase: "idle" }, progress({ stage: "error", error: "x" }))).toMatchObject({ code: "other" });
    expect(reduceProgress({ phase: "idle" }, progress({ stage: "done" }))).toEqual({ phase: "idle" });
  });
});

describe("keys exist in both languages", () => {
  const keys = [
    ...["download", "verify", "install", "x"].map(stageKey),
    ...["no_signature", "no_key", "bad_signature", "checksum", "manual_docker", "manual_dev", "manual_no_key", "manual_unsupported"].map((c) => errorKey(c)!),
    ...["docker", "dev", "no_key", "unsupported", "whatever"].map(reasonKey),
    ...["downloaded", "verified", "replaced", "package_installed", "installer_started", "manual"].map((c) => stepKey(c)!),
  ];
  it("every key the helpers can return is translated", () => {
    for (const k of keys) {
      expect(k, "a helper returned nothing").toBeTruthy();
      expect(k in dictionaries.zh, `zh: ${k}`).toBe(true);
      expect(k in dictionaries.en, `en: ${k}`).toBe(true);
    }
  });
  it("unknown codes have no key", () => {
    expect(errorKey("other")).toBeNull();
    expect(stepKey("future_step")).toBeNull();
  });
});
