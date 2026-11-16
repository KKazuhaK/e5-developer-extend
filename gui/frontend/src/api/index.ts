import type { Api } from "./interface";

export type { Api } from "./interface";
export * from "./types";

/** True when built or served with VITE_MOCK=1 (fake in-browser backend). */
export const IS_MOCK = import.meta.env.VITE_MOCK === "1";

// import.meta.env.VITE_MOCK is replaced at build time, so the branch that is
// not taken (and its module) is dropped from the bundle: the real build never
// contains the mock data, the mock build never needs the Wails bindings.
export const api: Api = IS_MOCK
  ? (await import("./mock")).mockApi
  : (await import("./wails")).wailsApi;
