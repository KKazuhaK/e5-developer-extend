/** The product display name. The final name is undecided: change it here only. */
export const AppName = "e5renew";

export const PROJECT_URL = "https://github.com/KKazuhaK/e5-renew";
export const DOCS_URL = `${PROJECT_URL}/blob/main/docs/user/app-registration.md`;
export const ISSUES_URL = `${PROJECT_URL}/issues`;
export const APP_VERSION = import.meta.env.VITE_APP_VERSION || "dev";

/** Snapshot poll interval while the document is visible. */
export const SNAPSHOT_POLL_MS = 3000;
/** Log auto refresh interval. */
export const LOG_POLL_MS = 5000;
