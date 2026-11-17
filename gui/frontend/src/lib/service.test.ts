import { describe, expect, it } from "vitest";
import { serviceState } from "./service";

describe("serviceState", () => {
  it("maps backend answers", () => {
    expect(serviceState({ platform: "unsupported", installed: false, running: "unknown" })).toBe("unsupported");
    expect(serviceState({ platform: "linux", installed: false, running: "no" })).toBe("notInstalled");
    expect(serviceState({ platform: "linux", installed: true, running: "yes" })).toBe("running");
    expect(serviceState({ platform: "linux", installed: true, running: "no" })).toBe("stopped");
    expect(serviceState({ platform: "linux", installed: true, running: "unknown" })).toBe("stopped");
  });
});
