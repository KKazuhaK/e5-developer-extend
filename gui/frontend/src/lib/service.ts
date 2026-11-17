import type { ServiceInfo, Snapshot } from "@/api/types";

/** "unknown": installed, but the platform cannot say whether it runs (Windows) and the engine does not settle it. */
export type ServiceState = "unsupported" | "notInstalled" | "running" | "stopped" | "unknown";

/** A heartbeat younger than this counts as a live engine. */
export const FRESH_HEARTBEAT_SEC = 120;

/**
 * Reduces HealthService.ServiceInfo to one state. The backend reports
 * platform "unsupported" when the OS has no background job support, and
 * running as "yes" / "no" / "unknown" (the Windows Task Scheduler never
 * says). For "unknown" the engine decides: the service runs when it hosts
 * the engine, or when a fresh heartbeat comes from something that is not the
 * tray. Otherwise it stays "unknown", which must not be shown as stopped.
 */
export function serviceState(
  info: Pick<ServiceInfo, "platform" | "installed" | "running">,
  engine?: Snapshot["engine"] | null,
): ServiceState {
  if (info.platform === "unsupported") return "unsupported";
  if (!info.installed) return "notInstalled";
  if (info.running === "yes") return "running";
  if (info.running === "no") return "stopped";
  if (engine?.running) {
    if (engine.host === "service") return "running";
    if (engine.host !== "gui" && engine.heartbeatAgeSec !== null && engine.heartbeatAgeSec < FRESH_HEARTBEAT_SEC) return "running";
  }
  return "unknown";
}
