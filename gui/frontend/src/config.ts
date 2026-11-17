/** The product display name; gui/main.go has the same constant. */
export const AppName = "E5 Developer Extend";

export const PROJECT_URL = "https://github.com/KKazuhaK/e5-developer-extend";
export const DOCS_URL = `${PROJECT_URL}/blob/main/docs/user/app-registration.md`;
export const FAQ_URL = `${PROJECT_URL}/blob/main/docs/user/faq.md`;
export const LEGACY_RETIREMENT_URL = "https://learn.microsoft.com/en-us/office/developer-program/legacy-subscription-retirement-faq";
export const ISSUES_URL = `${PROJECT_URL}/issues`;
export const APP_VERSION = import.meta.env.VITE_APP_VERSION || "dev";

/** Snapshot poll interval while the document is visible. */
export const SNAPSHOT_POLL_MS = 3000;
/** Log auto refresh interval. */
export const LOG_POLL_MS = 5000;
