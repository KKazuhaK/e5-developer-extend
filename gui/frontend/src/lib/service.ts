import type { ServiceInfo } from "@/api/types";

export type ServiceState = "unsupported" | "notInstalled" | "running" | "stopped";

/**
 * Reduces HealthService.ServiceInfo to one state. The backend reports
 * platform "unsupported" when the OS has no background job support, and
 * running as "yes" / "no" / "unknown".
 */
export function serviceState(info: Pick<ServiceInfo, "platform" | "installed" | "running">): ServiceState {
  if (info.platform === "unsupported") return "unsupported";
  if (!info.installed) return "notInstalled";
  return info.running === "yes" ? "running" : "stopped";
}
