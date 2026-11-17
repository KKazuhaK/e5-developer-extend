import { describe, expect, it } from "vitest";
import type { Snapshot } from "@/api/types";
import { serviceState } from "./service";

const eng = (e: Partial<Snapshot["engine"]>): Snapshot["engine"] => ({ running: true, heartbeatAgeSec: 5, host: "none", ...e });
const inst = (running: string) => ({ platform: "windows", installed: true, running });

describe("serviceState", () => {
  it("maps backend answers", () => {
    expect(serviceState({ platform: "unsupported", installed: false, running: "unknown" })).toBe("unsupported");
    expect(serviceState({ platform: "linux", installed: false, running: "no" })).toBe("notInstalled");
    expect(serviceState(inst("yes"))).toBe("running");
    expect(serviceState(inst("no"))).toBe("stopped");
  });
  it("trusts a definite no even when an engine is running", () => {
    expect(serviceState(inst("no"), eng({ host: "service" }))).toBe("stopped");
  });
  it("unknown + engine hosted by the service is running", () => {
    expect(serviceState(inst("unknown"), eng({ host: "service" }))).toBe("running");
  });
  it("unknown + the tray hosts the engine stays unknown, never stopped", () => {
    expect(serviceState(inst("unknown"), eng({ host: "gui" }))).toBe("unknown");
  });
  it("unknown + no engine or no snapshot stays unknown", () => {
    expect(serviceState(inst("unknown"), eng({ running: false, host: "none", heartbeatAgeSec: null }))).toBe("unknown");
    expect(serviceState(inst("unknown"))).toBe("unknown");
    expect(serviceState(inst("unknown"), null)).toBe("unknown");
  });
  it("unknown + a fresh heartbeat from a non-tray host is running, a stale one is not", () => {
    expect(serviceState(inst("unknown"), eng({ host: "none", heartbeatAgeSec: 30 }))).toBe("running");
    expect(serviceState(inst("unknown"), eng({ host: "none", heartbeatAgeSec: 600 }))).toBe("unknown");
  });
});
