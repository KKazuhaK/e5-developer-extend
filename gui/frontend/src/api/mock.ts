// In-browser fake backend implementing the contract in ./interface.ts.
// Used when VITE_MOCK=1. Text that comes from the real backend (reasons, fixes)
// is Chinese here, as the Go side will produce; health check details are codes
// the UI translates, with English text beside them, as the real backend sends.
import type { Api } from "./interface";
import { doctorMessage } from "@/lib/doctor";
import type {
  AccountInput, AppSetupAction, CategoryAdvice, AppSetupCancelResult, AppSetupChoice, AppSetupEvent, AppSetupPlan, AppSetupResult, AppSetupStart, BackupImportResult, AccountStatus, AccountSummary, CatalogEntry, DoctorCheck, LogEntry, APIStat, LoginEvent, RunDone, ServiceInfo,
  ChannelInput, ChannelType, ChannelView, ChannelTest, MessagePreview, EmailPreset, SecretStatus,
  Settings, Snapshot, SubscriptionView, TenantSubscriptions, UpdateInfo, UpdateProgress, UpdateResult, ValidationError,
} from "./types";

type Host = "gui" | "service" | "none";

// ---- controls (used by MockPanel and by ?host=none&empty=1 in the URL) ----
interface Controls { host: Host; empty: boolean; slow: boolean }
const q = new URLSearchParams(typeof location === "undefined" ? "" : location.search);
// ?demo=1 is a clean, made-up data set (a Contoso tenant, healthy accounts, one alert channel, the
// background service running) used to take the screenshots in docs/images (see scripts/screenshots).
// ?theme=light|dark picks the theme instead of following the system.
const demo = q.get("demo") === "1";
let controls: Controls = {
  host: (["gui", "service", "none"].includes(q.get("host") ?? "") ? q.get("host") : demo ? "service" : "gui") as Host,
  empty: q.get("empty") === "1",
  slow: q.get("slow") === "1",
};
// ?recommend=full|nomailbox|noonedrive|error picks what the API recommendation says.
const recommendVariant = (["full", "nomailbox", "noonedrive", "error"].includes(q.get("recommend") ?? "") ? q.get("recommend") : "full") as "full" | "nomailbox" | "noonedrive" | "error";
const controlListeners = new Set<() => void>();
export const mockControls = {
  get: () => controls,
  set(patch: Partial<Controls>) {
    controls = { ...controls, ...patch };
    controlListeners.forEach((l) => l());
  },
  subscribe(l: () => void) {
    controlListeners.add(l);
    return () => void controlListeners.delete(l);
  },
};

// ---- helpers ----
const MIN = 60_000;
const rand = (a: number, b: number) => a + Math.floor(Math.random() * (b - a + 1));
const iso = (t: number) => new Date(t).toISOString();
type DxMsg = [code: string, params?: Record<string, string>];
/** A health check as the backend sends it: codes with values, and their English text. */
function dx(id: string, title: string, level: DoctorCheck["level"], detail: DxMsg[], fix: DxMsg[] = []): DoctorCheck {
  const parts = detail.map(([code, params]) => ({ code, params: params ?? {} }));
  const fixParts = fix.map(([code, params]) => ({ code, params: params ?? {} }));
  return { id, title, level, detail: doctorMessage("en", parts, ""), fix: doctorMessage("en", fixParts, ""), parts, fixParts };
}

const wait = (ms: number) => new Promise<void>((r) => setTimeout(r, controls.slow ? ms * 6 : ms));
const lag = () => wait(rand(60, 180));
function hash(s: string): number {
  let h = 2166136261;
  for (const c of s) h = Math.imul(h ^ c.charCodeAt(0), 16777619);
  return h >>> 0;
}

// ---- accounts ----
interface MockAccount {
  input: AccountInput;
  status: AccountStatus;
  reason: string;
  fix: string;
  lastRunAt: number | null;
  lastSuccessAt: number | null;
  nextRunAt: number | null;
  todayOk: number;
  todayFailed: number;
  todayThrottled: number;
  pausedApis: number;
  /** Minutes until the next probe of an automatically paused account. */
  probeInMin: number | null;
  probes: number;
}

const now0 = Date.now();
const day = (n: number) => new Date(now0 + n * 86_400_000).toISOString().slice(0, 10);
const CATS_ALL = ["user", "mail", "calendar", "files", "org", "group", "sites", "teams", "tasks", "notes", "contacts", "reports", "apps"];

function mk(
  name: string, auth: AccountInput["auth"], tenant: string, status: AccountStatus, extra: Partial<MockAccount> & { secretExpires?: string; categories?: string[] } = {},
): MockAccount {
  const ok = status === "ok";
  return {
    input: {
      name, auth, tenant, clientId: `${hash(name).toString(16).padStart(8, "0")}-4e1b-47c0-9a35-5c1f0b8d2a7e`,
      username: auth === "client_secret" || auth === "browser" ? "" : `${name}@${tenant}`,
      categories: extra.categories ?? CATS_ALL.slice(0, 8), secretExpires: extra.secretExpires ?? "",
    },
    status,
    reason: extra.reason ?? "",
    fix: extra.fix ?? "",
    lastRunAt: ok ? now0 - rand(8, 40) * MIN : extra.lastRunAt ?? null,
    lastSuccessAt: ok ? now0 - rand(8, 40) * MIN : extra.lastSuccessAt ?? null,
    nextRunAt: ok ? now0 + rand(4, 28) * MIN : null,
    todayOk: extra.todayOk ?? (ok ? rand(14, 32) : 0),
    todayFailed: extra.todayFailed ?? 0,
    todayThrottled: extra.todayThrottled ?? 0,
    pausedApis: extra.pausedApis ?? 0,
    probeInMin: extra.probeInMin ?? null,
    probes: extra.probes ?? 0,
  };
}

let accounts: MockAccount[] = [];
function seedAccounts() {
  if (demo) {
    accounts = [
      mk("contoso", "device_code", "contoso.onmicrosoft.com", "ok", { categories: CATS_ALL.slice(0, 10), todayOk: 27 }),
      mk("contoso-app", "client_secret", "contoso.onmicrosoft.com", "ok", { categories: CATS_ALL.slice(0, 9), todayOk: 31, secretExpires: day(331) }),
    ];
    return;
  }
  accounts = [
    mk("main", "device_code", "contoso.onmicrosoft.com", "ok", { categories: CATS_ALL.slice(0, 10), todayFailed: 1, todayThrottled: 3, pausedApis: 2 }),
    mk("dev-app", "client_secret", "7d3f2a10-55c1-4c88-b1d4-0a3e9c61f2b8", "ok", { secretExpires: day(12), categories: CATS_ALL }),
    mk("sales-demo", "browser", "fabrikam.onmicrosoft.com", "ok", { categories: CATS_ALL.slice(0, 6) }),
    mk("lab", "device_code", "labtenant.onmicrosoft.com", "needs_attention", {
      reason: "刷新令牌已失效（invalid_grant）：密码被重置或会话被撤销",
      fix: "重新登录该账号以获取新的令牌",
      lastRunAt: now0 - 5 * 3_600_000, lastSuccessAt: now0 - 31 * 3_600_000, todayFailed: 6,
    }),
    mk("e5-dev", "device_code", "e5dev.onmicrosoft.com", "needs_attention", {
      reason: "sign-in is cut off by a Conditional Access sign-in frequency policy (AADSTS70043); ways out, in order: (a) use the app identity ...",
      fix: "",
      lastRunAt: now0 - 2 * 3_600_000, lastSuccessAt: now0 - 4 * 3_600_000, todayFailed: 2,
    }),
    mk("flaky", "client_secret", "flaky.onmicrosoft.com", "paused", {
      reason: "3 rounds in a row failed (last error: server_error)", fix: "it is tried again automatically, or fix the cause and run: e5extend account resume flaky",
      lastRunAt: now0 - 4 * 3_600_000, lastSuccessAt: now0 - 20 * 3_600_000, todayFailed: 9, probeInMin: 140, probes: 1,
    }),
    mk("archive", "password", "old-corp.onmicrosoft.com", "paused", {
      reason: "已手动暂停", lastRunAt: now0 - 3 * 86_400_000, lastSuccessAt: now0 - 3 * 86_400_000, todayOk: 0,
    }),
    mk("new-tenant", "device_code", "newdev.onmicrosoft.com", "not_signed_in", {
      reason: "尚未登录", fix: "完成一次登录后才会开始调用",
    }),
  ];
}
if (!controls.empty) seedAccounts();


// ---- updates ----
// ?update=none|available|downloading|error|manual|restart picks what the update check finds and how an upgrade ends:
// available (a new version, the upgrade works), downloading (the upgrade is stuck at 42%), error (the checksum does
// not match), manual (a Docker image: only the release page), restart (it works and asks for a restart), none (up to date).
type UpdateVariant = "none" | "available" | "downloading" | "error" | "manual" | "restart";
const updateVariant = (["none", "available", "downloading", "error", "manual", "restart"].includes(q.get("update") ?? "") ? q.get("update") : "none") as UpdateVariant;
let updateCheckedAt: number | null = updateVariant === "none" ? null : Date.now() - 3_600_000;
let updateRunning = false;
const updateListeners = new Set<(e: UpdateProgress) => void>();
const emitUpdate = (p: Partial<UpdateProgress> & Pick<UpdateProgress, "stage">) =>
  updateListeners.forEach((l) => l({ file: "", done: 0, total: 0, result: null, code: "", error: "", ...p }));
const UPDATE_SIZE = 48_000_000;
const updateFile = "e5extend_0.3.0_linux_amd64.tar.gz";

function mockUpdateInfo(): UpdateInfo {
  const newer = updateVariant !== "none";
  const manual = updateVariant === "manual";
  return {
    current: "0.2.0", latest: newer ? "0.3.0" : updateCheckedAt ? "0.2.0" : "", available: newer, prerelease: false,
    url: newer ? "https://github.com/KKazuhaK/e5-developer-extend/releases/tag/v0.3.0" : "https://github.com/KKazuhaK/e5-developer-extend/releases/latest",
    channel: "stable", dev: false, checkEnabled: true, checkedAt: updateCheckedAt ? iso(updateCheckedAt) : null,
    kind: manual ? "docker" : "linux-standalone", mode: manual ? "manual" : "auto", reason: manual ? "docker" : "",
  };
}

function mockUpdateResult(): UpdateResult {
  return {
    upToDate: false, version: "0.3.0", done: true, exitNow: false, restartService: true, restartApp: updateVariant === "restart",
    steps: [
      { code: "downloaded", arg: "0.3.0", text: `downloaded ${updateFile}` },
      { code: "verified", text: "verified the signature and the checksums" },
      { code: "replaced", arg: "e5extend", text: "replaced e5extend" },
    ],
    manual: [], service: "restarted", serviceNote: "",
  };
}

async function runMockUpgrade() {
  updateRunning = true;
  try {
    const steps = 12;
    for (let i = 1; i <= steps; i++) {
      await wait(250);
      const done = Math.round((UPDATE_SIZE * i) / steps);
      if (updateVariant === "downloading" && done > UPDATE_SIZE * 0.42) {
        emitUpdate({ stage: "download", file: updateFile, done: Math.round(UPDATE_SIZE * 0.42), total: UPDATE_SIZE });
        return; // stuck, to look at the progress bar
      }
      emitUpdate({ stage: "download", file: updateFile, done, total: UPDATE_SIZE });
    }
    emitUpdate({ stage: "verify" });
    await wait(700);
    if (updateVariant === "error") {
      emitUpdate({ stage: "error", code: "checksum", error: `the downloaded file does not match its checksum: ${updateFile}` });
      return;
    }
    if (updateVariant === "manual") {
      emitUpdate({ stage: "error", code: "manual_docker", error: "this is a Docker image; pull the new image instead" });
      return;
    }
    emitUpdate({ stage: "install" });
    await wait(900);
    emitUpdate({ stage: "done", result: mockUpdateResult() });
  } finally {
    updateRunning = false;
  }
}

// ---- subscriptions ----
// ?subscription=ok|expiring|warning|nopermission|manual picks what the check found for the first tenant
// (nopermission and manual apply to every tenant; manual also has a hand-written date on the first).
type SubVariant = "ok" | "expiring" | "warning" | "nopermission" | "manual";
let subVariantNow = (["ok", "expiring", "warning", "nopermission", "manual"].includes(q.get("subscription") ?? "") ? q.get("subscription") : "ok") as SubVariant;
const subManual = new Map<string, string>(); // tenant -> hand-written date
const subCheckedAt = new Map<string, number>();
const daysFrom = (iso: string) => Math.round((Date.parse(`${iso}T00:00:00Z`) - Date.UTC(new Date().getUTCFullYear(), new Date().getUTCMonth(), new Date().getUTCDate())) / 86_400_000);
function mockSub(id: string, sku: string, status: string, expires: number, extra: Partial<SubscriptionView> = {}): SubscriptionView {
  return {
    id, sku, watched: sku.startsWith("DEVELOPERPACK"), status, isTrial: false, licenses: 25, expires: day(expires), daysLeft: expires, checkedAt: iso(now0 - 3 * 3_600_000),
    renewals: [], ...extra,
  };
}
function mockTenants(): TenantSubscriptions[] {
  const groups = new Map<string, string[]>();
  for (const a of accounts) {
    const key = a.input.tenant.toLowerCase();
    groups.set(key, [...(groups.get(key) ?? []), a.input.name]);
  }
  if (subVariantNow === "manual" && groups.size > 0 && !subManual.has([...groups.keys()][0])) subManual.set([...groups.keys()][0], day(40));
  // The demo shows a second tenant (no account of its own in the demo data) so the cards sit side by side.
  if (demo && subVariantNow !== "nopermission" && subVariantNow !== "manual" && !groups.has("fabrikam.onmicrosoft.com")) groups.set("fabrikam.onmicrosoft.com", ["fabrikam-sales"]);
  return [...groups.entries()].map(([tenant, names], i) => {
    const first = i === 0;
    const checked = subCheckedAt.get(tenant) ?? now0 - 3 * 3_600_000;
    const base = {
      tenant, label: tenant, tenantId: first ? MOCK_TENANT : "", accounts: names, detail: "", attemptedAt: iso(checked), nextCheckAt: iso(checked + 86_400_000),
      manualExpires: subManual.get(tenant) ?? "", manualDaysLeft: null as number | null, manualInUse: false, manualRedundant: false,
      fixAccount: names[0],
      fixKind: (accounts.find((a) => a.input.name === names[0])?.input.auth === "client_secret" ? "app" : "delegated") as "app" | "delegated",
      fixCommand: accounts.find((a) => a.input.name === names[0])?.input.auth === "client_secret"
        ? `e5extend app setup --repair --account ${names[0]}`
        : `e5extend app setup --repair --account ${names[0]}`,
      watchedSkus: ["DEVELOPERPACK"],
    };
    let t: TenantSubscriptions;
    if (subVariantNow === "nopermission" || subVariantNow === "manual") {
      t = { ...base, access: "no_permission", checkedAt: null, subscriptions: [] };
    } else {
      const days = first ? (subVariantNow === "expiring" ? 5 : subVariantNow === "warning" ? 20 : demo ? 80 : 89) : demo ? 25 : 60 + i * 37;
      const status = first && subVariantNow === "warning" ? "Warning" : "Enabled";
      const subs = [mockSub(`s-${i}`, "DEVELOPERPACK_E5", status, days, first ? {
        renewals: [
          { from: day(days - 180), to: day(days - 90), seenAt: iso(now0 - 91 * 86_400_000) },
          { from: day(days - 90), to: day(days), seenAt: iso(now0 - 2 * 86_400_000) },
        ],
      } : demo ? { renewals: [{ from: day(days - 90), to: day(days), seenAt: iso(now0 - 64 * 86_400_000) }] } : {})];
      if (first && !demo) subs.push(mockSub("s-x", "POWER_BI_STANDARD", "Enabled", 400, { licenses: 1_000_000, isTrial: true }));
      t = { ...base, access: "ok", checkedAt: iso(checked), subscriptions: subs.map((x) => ({ ...x, checkedAt: iso(checked) })) };
    }
    if (t.manualExpires) {
      t.manualDaysLeft = daysFrom(t.manualExpires);
      t.manualInUse = !t.subscriptions.some((x) => x.watched);
      t.manualRedundant = !t.manualInUse;
    }
    return t;
  });
}

// ---- catalog ----
const CATALOG: { id: string; category: string; name: string; scopes: string[] }[] = [
  { id: "user.me", category: "user", name: "我的资料", scopes: ["User.Read"] },
  { id: "user.list", category: "user", name: "用户列表", scopes: ["User.ReadBasic.All"] },
  { id: "user.memberof", category: "user", name: "我所在的组", scopes: ["User.Read"] },
  { id: "mail.messages", category: "mail", name: "最近邮件", scopes: ["Mail.ReadBasic", "Mail.Read"] },
  { id: "mail.folders", category: "mail", name: "邮件文件夹", scopes: ["Mail.ReadBasic", "Mail.Read"] },
  { id: "mail.rules", category: "mail", name: "收件箱规则", scopes: ["MailboxSettings.Read"] },
  { id: "calendar.events", category: "calendar", name: "日历事件", scopes: ["Calendars.Read"] },
  { id: "calendar.list", category: "calendar", name: "日历列表", scopes: ["Calendars.Read"] },
  { id: "contacts.list", category: "contacts", name: "联系人", scopes: ["Contacts.Read"] },
  { id: "files.root", category: "files", name: "OneDrive 根目录", scopes: ["Files.Read"] },
  { id: "files.recent", category: "files", name: "最近文件", scopes: ["Files.Read"] },
  { id: "org.info", category: "org", name: "组织信息", scopes: ["Organization.Read.All"] },
  { id: "org.domains", category: "org", name: "域名", scopes: ["Domain.Read.All"] },
  { id: "group.list", category: "group", name: "组列表", scopes: ["Group.ReadBasic.All"] },
  { id: "sites.root", category: "sites", name: "根站点", scopes: ["Sites.Read.All"] },
  { id: "sites.search", category: "sites", name: "站点搜索", scopes: ["Sites.Read.All"] },
  { id: "teams.joined", category: "teams", name: "已加入的团队", scopes: ["Team.ReadBasic.All"] },
  { id: "tasks.lists", category: "tasks", name: "待办列表", scopes: ["Tasks.Read"] },
  { id: "notes.books", category: "notes", name: "OneNote 笔记本", scopes: ["Notes.Read"] },
  { id: "reports.mail", category: "reports", name: "邮件活动报告", scopes: ["Reports.Read.All"] },
  { id: "apps.list", category: "apps", name: "应用注册", scopes: ["Application.Read.All"] },
  { id: "insights.trending", category: "insights", name: "热门文档", scopes: ["Sites.Read.All"] },
  { id: "people.list", category: "people", name: "相关人员", scopes: ["People.Read"] },
  { id: "presence.me", category: "presence", name: "在线状态", scopes: ["Presence.Read"] },
  { id: "bookings.businesses", category: "bookings", name: "Bookings 业务", scopes: ["Bookings.Read.All"] },
];
const categories = () => [...new Set(CATALOG.map((c) => c.category))];

function catalogFor(account: string): CatalogEntry[] {
  const a = accounts.find((x) => x.input.name === account);
  const chosen = a ? new Set(a.input.categories) : null;
  return CATALOG.filter((c) => !chosen || chosen.has(c.category)).map((c) => ({
    ...c,
    usable: a?.status === "not_signed_in" || a?.status === "needs_attention" ? false : hash(account + c.id) % 7 !== 0,
  }));
}

// ---- logs ----
const logs: LogEntry[] = [];
function logCall(a: MockAccount, t: number) {
  const c = CATALOG[rand(0, CATALOG.length - 1)];
  const r = Math.random();
  const failed = a.status === "needs_attention" ? true : !demo && r < 0.06;
  const status = failed ? (a.status === "needs_attention" ? 401 : [429, 403, 503][rand(0, 2)]) : 200;
  const cls = status === 200 ? "ok" : status === 401 ? "auth" : status === 429 ? "throttled" : status === 403 ? "forbidden" : "server";
  logs.push({ ts: iso(t), account: a.input.name, api: c.id, status, class: cls, latencyMs: rand(90, failed ? 2600 : 780) });
}
function seedLogs() {
  logs.length = 0;
  const active = accounts.filter((a) => a.status !== "not_signed_in");
  for (let i = 0; i < 160 && active.length; i++) {
    const a = active[rand(0, active.length - 1)];
    const t = now0 - rand(0, 48 * 60) * MIN + rand(0, 59_000);
    if (a.status === "paused" && t > now0 - 3 * 86_400_000 + 0) continue;
    logCall(a, t);
  }
  if (accounts.length && !demo) {
    logs.push({ ts: iso(now0 - 5 * 3_600_000), account: "lab", api: "", status: 0, class: "", latencyMs: 0, event: "needs_attention", detail: "invalid_grant: refresh token revoked" });
    logs.push({ ts: iso(now0 - 3 * 86_400_000), account: "archive", api: "", status: 0, class: "", latencyMs: 0, event: "paused", detail: "手动暂停" });
  }
  logs.sort((x, y) => Date.parse(x.ts) - Date.parse(y.ts));
}
seedLogs();

function runRound(a: MockAccount) {
  const t = Date.now();
  const n = rand(3, 6);
  for (let i = 0; i < n; i++) logCall(a, t - (n - i) * 900);
  const recent = logs.slice(-n);
  const bad = recent.filter((l) => l.status >= 400).length;
  a.todayOk += n - bad;
  a.todayFailed += bad;
  a.lastRunAt = t;
  if (bad < n) a.lastSuccessAt = t;
  a.nextRunAt = t + rand(15, 30) * MIN;
}

function tick() {
  if (controls.host === "none") return;
  const t = Date.now();
  for (const a of accounts) {
    if (a.status === "paused" || a.status === "not_signed_in" || a.status === "needs_attention") continue;
    if (a.nextRunAt && a.nextRunAt <= t) runRound(a);
  }
}

const navListeners = new Set<(page: unknown) => void>();
/** Simulates the backend "navigate" event (used by MockPanel). */
export const mockNavigate = (page: unknown) => navListeners.forEach((cb) => cb(page));

const runDoneListeners = new Set<(e: RunDone) => void>();

// ---- login flow ----
const loginListeners = new Map<string, Set<(e: LoginEvent) => void>>();
const loginTimers = new Map<string, number[]>();
function emit(name: string, e: LoginEvent) {
  loginListeners.get(name)?.forEach((cb) => cb(e));
}
function clearLogin(name: string) {
  (loginTimers.get(name) ?? []).forEach((id) => clearTimeout(id));
  loginTimers.delete(name);
}
function finishLogin(name: string) {
  const a = accounts.find((x) => x.input.name === name);
  if (a) {
    a.status = "ok";
    a.reason = "";
    a.fix = "";
    a.lastRunAt = a.lastSuccessAt = Date.now();
    a.nextRunAt = Date.now() + rand(5, 15) * MIN;
  }
  emit(name, { kind: "done", message: "登录成功" });
}

// ---- automatic app registration ----
// Variants, picked with ?appsetup=: candidates (hand-made apps are found: adopt or create),
// existing (an app of an earlier run lacks two permissions), ready (everything is in place),
// noconsent (the admin lacks the role to grant consent), forbidden (403 when creating the app),
// blocked (the tenant refuses Graph Command Line Tools). ?revokefail=1 makes undoing an unused
// client secret fail.
const setupVariant = q.get("appsetup") ?? "";
const setupListeners = new Map<string, Set<(e: AppSetupEvent) => void>>();
const setupTimers = new Map<string, number[]>();
// catalog.RecommendedDelegated and catalog.RecommendedApplication, verbatim.
const DELEGATED_PERMS = [
  "User.Read", "User.ReadBasic.All", "Organization.Read.All", "GroupMember.Read.All", "Mail.ReadBasic", "MailboxSettings.Read", "Calendars.ReadBasic", "Contacts.Read",
  "Files.Read", "Sites.Read.All", "Notes.Read", "Tasks.Read", "Team.ReadBasic.All", "offline_access",
];
const APP_PERMS = [
  "User.Read.All", "Organization.Read.All", "Group.Read.All", "Mail.ReadBasic.All", "MailboxSettings.Read", "Calendars.Read", "Contacts.Read",
  "Files.Read.All", "Sites.Read.All", "Tasks.Read.All", "Team.ReadBasic.All",
];
const MOCK_TENANT = "b6f0a3c1-2d94-4e57-8a1b-90c7e5d4f812";
const MOCK_CLIENT = "e41d7a92-6b30-4c58-9f0a-1c85d3b7a6e2";
const MOCK_CANDIDATES = [{ clientId: MOCK_CLIENT, displayName: "e5renew" }, { clientId: "a1b2c3d4-0000-4b6e-9d11-77aa55cc33ee", displayName: "e5extend" }];
function setupEmit(id: string, e: AppSetupEvent) {
  setupListeners.get(id)?.forEach((cb) => cb(e));
}
function setupAt(id: string, ms: number, fn: () => void) {
  const t = window.setTimeout(fn, controls.slow ? ms * 4 : ms);
  setupTimers.set(id, [...(setupTimers.get(id) ?? []), t]);
}
function setupClear(id: string) {
  (setupTimers.get(id) ?? []).forEach((t) => clearTimeout(t));
  setupTimers.delete(id);
}
interface MockSetup { start: AppSetupStart; plan: AppSetupPlan; result: AppSetupResult | null; repair?: boolean }
const setups = new Map<string, MockSetup>();

/** The actions and missing permissions for the app the plan is about; choice is the user's decision when candidates exist. */
function mockActions(base: Omit<AppSetupPlan, "actions" | "missing">, choice: AppSetupChoice | null): { actions: AppSetupAction[]; missing: string[] } {
  const app = base.kind === "app";
  const name = base.displayName;
  const A = (code: string, text: string): AppSetupAction => ({ code, text });
  const tail = (needSP: boolean, needConsent: boolean) => [
    ...(needSP ? [A("create_service_principal", "create the service principal")] : []),
    ...(needConsent ? [app ? A("assign_roles", "grant the application permissions") : A("grant_consent", "grant administrator consent")] : []),
    ...(app ? [A("create_secret", setupVariant === "existing" ? "create a client secret; then delete this computer's 1 earlier secret(s) from the app" : "create a client secret")] : []),
  ];
  const adoptSteps = (rename: boolean) => [
    A("adopt_app", "adopt the existing app registration"),
    ...(rename ? [A("rename_app", `rename the app registration to "${name}"`)] : []),
    A("set_notes", "add a note"),
    A("add_tags", "add the tags e5extend"),
    ...(app ? [] : [A("add_redirect_uri", "add the redirect URI http://localhost"), A("set_public_client", "allow public client flows")]),
    A("add_permissions", `add ${base.permissions.length} permission(s)`),
  ];
  if (setupVariant === "candidates") {
    if (!choice || (!choice.adopt && !choice.createNew)) {
      return { missing: base.permissions, actions: [A("adopt_or_create", `adopt one of the existing app registrations (2 found) or create "${name}"`), ...(app ? [A("create_secret", "create a client secret")] : [])] };
    }
    if (choice.adopt) return { missing: base.permissions, actions: [...adoptSteps(choice.rename), ...tail(true, true)] };
    return { missing: base.permissions, actions: [A("create_app", `create the app registration "${name}"`), ...tail(true, true)] };
  }
  if (setupVariant === "existing") {
    const missing = base.permissions.slice(-2);
    return { missing, actions: [A("add_permissions", "add 2 permission(s)"), ...tail(false, true)] };
  }
  if (setupVariant === "ready") return { missing: [], actions: app ? [A("create_secret", "create a client secret")] : [] };
  return { missing: base.permissions, actions: [A("create_app", `create the app registration "${name}"`), ...tail(true, true)] };
}
function mockPlan(start: AppSetupStart): AppSetupPlan {
  const app = start.appOnly;
  const displayName = app ? "E5 Developer Extend (app-only)" : "E5 Developer Extend";
  const base = {
    kind: (app ? "app" : "delegated") as AppSetupPlan["kind"], tenantId: MOCK_TENANT, displayName,
    tags: ["e5extend", app ? "e5extend:app" : "e5extend:delegated"], permissions: app ? APP_PERMS : DELEGATED_PERMS,
    existing: setupVariant === "existing" || setupVariant === "ready" ? { clientId: MOCK_CLIENT, displayName } : null,
    candidates: setupVariant === "candidates" ? MOCK_CANDIDATES : [], needsChoice: setupVariant === "candidates",
  };
  return { ...base, ...mockActions(base, null) };
}
function mockResult(plan: AppSetupPlan, choice: AppSetupChoice, name: string): AppSetupResult {
  const adopted = !!choice.adopt && plan.needsChoice;
  const created = plan.needsChoice ? !adopted : !plan.existing;
  const noConsent = setupVariant === "noconsent";
  const client = adopted ? choice.adopt : MOCK_CLIENT;
  const app = plan.kind === "app";
  void name;
  return {
    kind: plan.kind, tenantId: plan.tenantId, clientId: client, displayName: plan.displayName,
    createdApp: created, adoptedApp: adopted, renamed: adopted && choice.rename,
    consentGranted: !noConsent,
    adminConsentUrl: noConsent ? `https://login.microsoftonline.com/${plan.tenantId}/adminconsent?client_id=${client}` : "",
    consentError: noConsent
      ? `this account is not allowed to grant administrator consent: it needs the role ${app ? "Privileged Role Administrator or Global Administrator" : "Cloud Application Administrator or Application Administrator or Privileged Role Administrator or Global Administrator"}`
      : "",
    secretExpires: app ? day(365) : "", secretCreated: app,
    removedSecrets: app && !created ? 1 : 0, notRemovedSecrets: app && !created ? 1 : 0,
  };
}
/** Progress lines in the backend's own words, for the actions that will run. */
function mockProgress(plan: AppSetupPlan, actions: AppSetupAction[], account: string, adoptName?: string): string[] {
  const has = (c: string) => actions.some((a) => a.code === c);
  const name = adoptName ?? plan.existing?.displayName ?? plan.displayName;
  const out: string[] = [];
  if (has("create_app")) out.push(`creating the app registration "${plan.displayName}"...`);
  else if (actions.some((a) => ["adopt_app", "rename_app", "set_notes", "add_tags", "add_redirect_uri", "set_public_client", "add_permissions"].includes(a.code))) out.push(`updating the app registration "${name}"...`);
  if (has("create_service_principal")) out.push("creating the service principal...", "waiting for Microsoft to replicate the new app (service principal), retrying in 3s...");
  if (has("grant_consent")) out.push("granting administrator consent...");
  if (has("assign_roles")) out.push(`granting ${plan.permissions[0]}...`, `granting ${plan.permissions[1]}...`);
  if (has("create_secret")) out.push(`creating a client secret "e5extend dev-box ${account} 2026-11-16"...`);
  return out;
}

// ---- settings / health state ----
let settings: Settings = {
  schedule: {
    intervalMin: "20m", intervalMax: "45m", apisPerRoundMin: 3, apisPerRoundMax: 6, randomOrder: true,
    workDays: ["mon", "tue", "wed", "thu", "fri", "sat", "sun"], workStart: "00:00", workEnd: "00:00",
    periodStart: "", periodEnd: day(300),
  },
  network: { proxy: "" },
  notify: {
    staleAfter: "72h", events: ["needs_attention", "auto_paused", "stale"], webhookConfigured: false, channels: 0,
    cooldown: "6h", quietHours: "", dailySummaryAt: "", spikeRounds: 3, spikeRate: 0.5, spikeMinFailures: 5,
  },
  ui: { language: (["zh", "en"].includes(q.get("lang") ?? "") ? q.get("lang") : "auto") as "auto" | "zh" | "en", theme: (["light", "dark"].includes(q.get("theme") ?? "") ? q.get("theme") : "system") as "light" | "dark" | "system", startAtLogin: demo },
};
if (demo) Object.assign(settings.schedule, { intervalMin: "40m", intervalMax: "2h", apisPerRoundMin: 3, apisPerRoundMax: 8 });
// ?service=stopped starts with the service installed but not running
let service: { installed: boolean } = { installed: demo || q.get("service") === "stopped" };
/** What the last mock export "wrote"; the mock import reads it back. */
const emptyExport = { path: "", accounts: [], vaultEntries: 0, includesLogs: false, logFiles: 0, notes: [], noteCodes: [] };
const emptyImport = {
  mode: "default" as const, accounts: [], vaultEntries: 0, sourceOS: "", sourceVersion: "", created: "", newMasterKey: false,
  skipped: [], notes: [], skippedCodes: [], noteCodes: [], hint: "",
};
let lastBackup: { passphrase: string; accounts: string[] } | null = null;

function serviceInfo(): ServiceInfo {
  // ?service=unsupported previews a platform without background job support.
  if (q.get("service") === "unsupported") return { platform: "unsupported", installed: false, enabled: "unknown", running: "unknown", details: [] };
  return {
    platform: "linux · systemd (user)",
    installed: service.installed,
    enabled: service.installed ? "yes" : "no",
    running: service.installed ? (controls.host === "service" ? "yes" : "no") : "no",
    details: service.installed
      ? ["单元文件：~/.config/systemd/user/e5extend.service", "可执行文件：~/.local/bin/e5extend run", "日志：journalctl --user -u e5extend"]
      : [],
  };
}

function snapshot(): Snapshot {
  tick();
  const running = controls.host !== "none";
  return {
    engine: { running, heartbeatAgeSec: running ? rand(1, 24) : null, host: controls.host },
    generatedAt: new Date().toISOString(),
    accounts: accounts.map((a): AccountSummary => {
      const usable = catalogFor(a.input.name).filter((c) => c.usable).length;
      return {
        name: a.input.name, auth: a.input.auth, tenant: a.input.tenant, status: a.status, reason: a.reason, fix: a.fix,
        nextRun: a.nextRunAt && running && a.status === "ok" ? iso(a.nextRunAt) : null,
        lastRun: a.lastRunAt ? iso(a.lastRunAt) : null,
        lastSuccess: a.lastSuccessAt ? iso(a.lastSuccessAt) : null,
        todayOk: a.todayOk, todayFailed: a.todayFailed, todayThrottled: a.todayThrottled, pausedApis: a.pausedApis,
        autoResumeAt: a.probeInMin !== null ? iso(Date.now() + a.probeInMin * MIN) : null, autoResumeTries: a.probes, usableApis: usable, totalApis: catalogFor(a.input.name).length,
        secretExpires: a.input.secretExpires || null, paused: a.status === "paused",
      };
    }),
  };
}

function find(name: string): MockAccount {
  const a = accounts.find((x) => x.input.name === name);
  if (!a) throw new Error(`账号 ${name} 不存在`);
  return a;
}

// ---- notification channels (secrets never leave this closure) ----
const SECRETS: Record<ChannelType, [string, boolean][]> = {
  telegram: [["bot_token", true]], email: [["password", false]], wecom: [["url", true]], feishu: [["url", true], ["secret", false]],
  dingtalk: [["url", true], ["secret", false]], discord: [["url", true]], slack: [["url", true]], bark: [["device_key", true]],
  ntfy: [["token", false]], serverchan: [["sendkey", true]], webhook: [["url", true]],
};
interface MockChannel { view: ChannelView; secrets: Record<string, string> }
let mockChannels: MockChannel[] = q.get("channels") === "some" ? seedChannels() : demo ? seedChannels().slice(0, 1) : [];
function seedChannels(): MockChannel[] {
  const base = (c: Partial<ChannelView> & Pick<ChannelInput, "id" | "type">): ChannelView => ({
    minLevel: "", events: [], language: "", template: "", chatId: "", apiBase: "", smtpHost: "", smtpPort: 0, security: "", username: "",
    from: "", to: [], server: "", topic: "", format: "", allowHttp: false, allowPlainAuth: false, secrets: {}, clearSecrets: [],
    secretFields: SECRETS[c.type].map(([name, required]): SecretStatus => ({ name, required, set: true })),
    legacy: false, lastResult: null, warnings: [], ...c,
  });
  return [
    { view: base({ id: "tg", type: "telegram", chatId: "123456789", minLevel: "warning", lastResult: { at: iso(Date.now() - 20 * MIN), ok: true } }), secrets: { bot_token: "x" } },
    { view: base({ id: "mail", type: "email", smtpHost: "smtp.gmail.com", smtpPort: 587, security: "starttls", username: "me@gmail.com", from: "me@gmail.com", to: ["me@gmail.com"], minLevel: "critical", lastResult: { at: iso(Date.now() - 3 * 60 * MIN), ok: false, error: "email: 535 5.7.8 Username and Password not accepted" } }), secrets: { password: "x" } },
  ];
}
function syncChannelCount() {
  settings.notify.channels = mockChannels.length;
  settings.notify.webhookConfigured = mockChannels.some((c) => c.view.id === "webhook");
}
syncChannelCount();
const EVENT_LABEL: Record<string, [string, string]> = {
  needs_attention: ["需要处理", "Needs attention"], recovered: ["已恢复", "Recovered"], auto_paused: ["已自动暂停", "Paused automatically"],
  stale: ["长时间没有成功调用", "No successful call for a long time"], secret_expiring: ["密钥即将过期", "Client secret expiring"],
  period_ended: ["运行周期已结束", "Run period ended"], failure_spike: ["调用失败率过高", "High failure rate"],
  all_failing: ["所有账号同时异常", "All accounts failing"],
  subscription_expiring: ["E5 订阅即将到期", "E5 subscription expiring"], subscription_status: ["E5 订阅状态变化", "E5 subscription status changed"],
  subscription_renewed: ["E5 订阅已续期", "E5 subscription renewed"], daily_summary: ["每日摘要", "Daily summary"], update_available: ["有新版本可以升级", "A new version is available"], test: ["测试消息", "Test message"], digest: ["免打扰期间的告警汇总", "Alerts held during quiet hours"],
};

function validateSettings(s: Settings): ValidationError[] {
  const out: ValidationError[] = [];
  if (s.network.proxy && /bad/.test(s.network.proxy)) out.push({ field: "network.proxy", message: "not a valid URL: " + s.network.proxy });
  if (s.notify.quietHours && !/^([01]\d|2[0-3]):[0-5]\d-([01]\d|2[0-3]):[0-5]\d$/.test(s.notify.quietHours)) out.push({ field: "notify.quietHours", message: "use the form 23:00-08:00" });
  if (s.notify.dailySummaryAt && !/^([01]\d|2[0-3]):[0-5]\d$/.test(s.notify.dailySummaryAt)) out.push({ field: "notify.dailySummaryAt", message: "use the form 21:00" });
  if (s.schedule.apisPerRoundMax > 12) out.push({ field: "schedule.apisPerRoundMax", message: "must be at most 12 (this catalog has only 12 read-only categories)" });
  return out;
}

export const mockApi: Api = {
  status: {
    onRunDone(cb) {
      runDoneListeners.add(cb);
      return () => void runDoneListeners.delete(cb);
    },
    async snapshot() {
      await lag();
      return snapshot();
    },
    async runNow(names) {
      await lag();
      const go = () => {
        for (const a of accounts) {
          if ((names.length === 0 || names.includes(a.input.name)) && a.status === "ok") runRound(a);
        }
      };
      if (controls.host === "none") {
        // No engine: the round runs in-process; RunNow returns at once and "run:done" follows.
        setTimeout(() => {
          go();
          runDoneListeners.forEach((cb) => cb({ error: "" }));
        }, 1500);
        return;
      }
      go();
    },
  },
  accounts: {
    async add(input) {
      await wait(350);
      if (accounts.some((a) => a.input.name === input.name)) throw new Error(`账号 ${input.name} 已存在。请换一个名称。`);
      const { secret, useStoredSecret, ...rest } = input;
      void secret; // never stored in the mock either
      void useStoredSecret;
      accounts.push({
        input: rest, status: "not_signed_in", reason: "尚未登录", fix: "完成一次登录后才会开始调用",
        lastRunAt: null, lastSuccessAt: null, nextRunAt: null, todayOk: 0, todayFailed: 0, todayThrottled: 0, pausedApis: 0, probeInMin: null, probes: 0,
      });
    },
    async update(input) {
      await wait(250);
      const a = find(input.name);
      const { secret, ...rest } = input;
      void secret;
      a.input = rest;
    },
    async remove(name) {
      await lag();
      find(name);
      accounts = accounts.filter((a) => a.input.name !== name);
    },
    async setSecret(name, secret) {
      await wait(300);
      find(name);
      if (!secret) throw new Error("Secret 不能为空");
    },
    async login(name) {
      const a = find(name);
      clearLogin(name);
      const timers: number[] = [];
      const at = (ms: number, fn: () => void) => timers.push(window.setTimeout(fn, controls.slow ? ms * 4 : ms));
      loginTimers.set(name, timers);
      if (a.input.auth === "device_code") {
        at(500, () => emit(name, {
          kind: "device_code", message: "在浏览器中打开链接并输入代码", userCode: "H7K2-QX9M", verificationUri: "https://microsoft.com/devicelogin",
        }));
        at(9000, () => finishLogin(name));
      } else if (a.input.auth === "browser") {
        at(500, () => emit(name, { kind: "browser_opened", message: "已在浏览器中打开登录页，请在浏览器中完成登录" }));
        at(7000, () => finishLogin(name));
      } else {
        at(1000, () => finishLogin(name));
      }
    },
    async loginAfterSetup(name) {
      // Microsoft takes a moment to know the new app: two waits, then the normal sign-in.
      const a = find(name);
      clearLogin(name);
      const timers: number[] = [];
      const at = (ms: number, fn: () => void) => timers.push(window.setTimeout(fn, controls.slow ? ms * 4 : ms));
      loginTimers.set(name, timers);
      at(300, () => emit(name, { kind: "progress", message: "waiting for Microsoft to replicate the new app, retrying sign-in in 5s..." }));
      const t0 = 2800;
      if (a.input.auth === "device_code") {
        at(t0, () => emit(name, { kind: "device_code", message: "在浏览器中打开链接并输入代码", userCode: "H7K2-QX9M", verificationUri: "https://microsoft.com/devicelogin" }));
        at(t0 + 7000, () => finishLogin(name));
      } else if (a.input.auth === "browser") {
        at(t0, () => emit(name, { kind: "browser_opened", message: "已在浏览器中打开登录页，请在浏览器中完成登录" }));
        at(t0 + 5000, () => finishLogin(name));
      } else {
        at(t0, () => finishLogin(name));
      }
    },
    async cancelLogin(name) {
      clearLogin(name);
      emit(name, { kind: "error", message: "已取消登录" });
    },
    async test(name) {
      await wait(1200);
      const a = find(name);
      if (a.status === "needs_attention") throw new Error("令牌已失效（invalid_grant）。建议：重新登录该账号。");
      if (a.status === "not_signed_in") throw new Error("该账号还没有登录。建议：先完成登录。");
      const entries = catalogFor(name);
      const usable = entries.filter((c) => c.usable);
      return {
        scopes: [...new Set(usable.flatMap((c) => c.scopes))].sort(),
        usableApis: usable.length,
        totalApis: entries.length,
        expiresOn: iso(Date.now() + 52 * MIN),
      };
    },
    async recommend(name) {
      await wait(900);
      const a = find(name);
      if (a.status === "not_signed_in") return { needsLogin: true, categories: [], recommended: [] };
      const variant = recommendVariant;
      if (variant === "error") throw new Error("检查失败：网络连接超时。稍后再试，或先按默认选择。");
      const unavailable: Record<string, string> = {};
      if (variant === "nomailbox") for (const c of ["mail", "calendar", "contacts", "tasks"]) unavailable[c] = "no_mailbox";
      if (variant === "noonedrive") for (const c of ["files", "notes"]) unavailable[c] = "no_onedrive";
      const list: CategoryAdvice[] = categories().map((category) => {
        const dflt = category !== "reports";
        if (unavailable[category]) return { category, status: "unavailable", reason: unavailable[category], default: dflt };
        if (category === "reports") return { category, status: "maybe", reason: "needs_admin_role", default: dflt };
        if (category === "apps") return { category, status: "unavailable", reason: "missing_permission:Application.Read.All", default: dflt };
        if (variant === "noonedrive" && category === "sites") return { category, status: "maybe", reason: "probe_failed", default: dflt };
        return { category, status: "recommended", reason: "", default: dflt };
      });
      return { needsLogin: false, categories: list, recommended: list.filter((c) => c.status === "recommended").map((c) => c.category) };
    },
    async pause(name) {
      await lag();
      const a = find(name);
      a.status = "paused";
      a.reason = "已手动暂停";
      a.nextRunAt = null;
    },
    async resume(name) {
      await lag();
      const a = find(name);
      a.status = "ok";
      a.reason = "";
      a.nextRunAt = Date.now() + rand(2, 10) * MIN;
    },
    async get(name) {
      await lag();
      return { ...find(name).input, categories: [...find(name).input.categories] };
    },
    onLoginEvent(name, cb) {
      let set = loginListeners.get(name);
      if (!set) loginListeners.set(name, (set = new Set()));
      set.add(cb);
      return () => void set.delete(cb);
    },
  },
  appSetup: {
    async begin(id, start) {
      if (accounts.some((a) => a.input.name === start.name)) throw new Error(`account "${start.name}" already exists`);
      if (start.appOnly && !start.username.trim()) throw new Error("an application identity reads one user's data: enter that user's name (UPN)");
      if (setups.size > 0) throw new Error("an app setup is already in progress");
      setupClear(id);
      const plan = mockPlan(start);
      setups.set(id, { start, plan, result: null });
      await wait(150);
      if (!setups.has(id)) return; // cancelled meanwhile
      if (start.method === "device_code") {
        setupAt(id, 400, () => setupEmit(id, { kind: "device_code", message: "To sign in, open https://microsoft.com/devicelogin and enter the code to authenticate.", userCode: "K4PD-82MQ", verificationUri: "https://microsoft.com/devicelogin" }));
      } else {
        setupAt(id, 400, () => setupEmit(id, { kind: "browser_opened", message: "complete the sign-in in your browser" }));
      }
      if (setupVariant === "blocked") {
        setupAt(id, 3000, () => {
          setups.delete(id);
          setupEmit(id, {
            kind: "error", code: "blocked",
            message: "the tenant does not allow signing in with Microsoft Graph Command Line Tools (AADSTS65001)\nUse the manual setup instead: docs/user/app-registration.md",
          });
        });
        return;
      }
      setupAt(id, 6000, () => setupEmit(id, { kind: "signed_in", message: "signed in" }));
      setupAt(id, 7200, () => setupEmit(id, { kind: "plan", message: "", plan }));
    },
    async repair(id, account, method) {
      const a = accounts.find((x) => x.input.name === account);
      if (!a) throw new Error(`no account named "${account}"`);
      if (setups.size > 0) throw new Error("an app setup is already in progress");
      setupClear(id);
      const app = a.input.auth === "client_secret";
      const displayName = app ? "E5 Developer Extend (app-only)" : "E5 Developer Extend";
      const plan: AppSetupPlan = {
        kind: app ? "app" : "delegated", tenantId: MOCK_TENANT, displayName, tags: ["e5extend", app ? "e5extend:app" : "e5extend:delegated"],
        permissions: app ? APP_PERMS : DELEGATED_PERMS, missing: ["Organization.Read.All"], existing: { clientId: a.input.clientId, displayName },
        candidates: [], needsChoice: false,
        actions: [{ code: "add_permissions", text: "add 1 permission(s)" }, app ? { code: "assign_roles", text: "grant the application permissions" } : { code: "grant_consent", text: "grant administrator consent" }],
      };
      setups.set(id, { start: { name: account, appOnly: app, auth: "device_code", username: "", method }, plan, result: null, repair: true });
      await wait(150);
      if (!setups.has(id)) return;
      if (method === "device_code") {
        setupAt(id, 400, () => setupEmit(id, { kind: "device_code", message: "To sign in, open https://microsoft.com/devicelogin and enter the code to authenticate.", userCode: "K4PD-82MQ", verificationUri: "https://microsoft.com/devicelogin" }));
      } else {
        setupAt(id, 400, () => setupEmit(id, { kind: "browser_opened", message: "complete the sign-in in your browser" }));
      }
      setupAt(id, 2000, () => setupEmit(id, { kind: "signed_in", message: "signed in" }));
      setupAt(id, 2600, () => setupEmit(id, { kind: "plan", message: "", plan }));
    },
    async preview(id, choice) {
      await wait(250);
      const st = setups.get(id);
      if (!st) throw new Error("the app setup is not waiting for a decision");
      if (!st.plan.needsChoice || (!choice.adopt && !choice.createNew)) return st.plan;
      return { ...st.plan, ...mockActions(st.plan, choice) };
    },
    async apply(id, choice) {
      await wait(100);
      const st = setups.get(id);
      if (!st) throw new Error("this app setup is not running any more");
      if (st.repair) {
        setupAt(id, 500, () => setupEmit(id, { kind: "progress", message: `updating the app registration "${st.plan.displayName}"...` }));
        setupAt(id, 1400, () => setupEmit(id, { kind: "progress", message: st.plan.kind === "app" ? `granting ${st.plan.missing[0]}...` : "granting administrator consent..." }));
        setupAt(id, 2300, () => {
          st.result = {
            kind: st.plan.kind, tenantId: st.plan.tenantId, clientId: st.plan.existing?.clientId ?? MOCK_CLIENT, displayName: st.plan.displayName,
            createdApp: false, adoptedApp: false, renamed: false, consentGranted: true, adminConsentUrl: "", consentError: "",
            secretExpires: "", secretCreated: false, removedSecrets: 0, notRemovedSecrets: 0,
          };
          // The permission is there now: the next subscription check reads the subscription.
          subVariantNow = "ok";
          setupEmit(id, { kind: "result", message: "done", result: st.result });
        });
        return;
      }
      if (st.plan.needsChoice && !choice.adopt && !choice.createNew) throw new Error("choose whether to adopt an existing app registration or create a new one");
      const { actions } = mockActions(st.plan, st.plan.needsChoice ? choice : null);
      const steps = mockProgress(st.plan, actions, st.start.name, st.plan.candidates.find((c) => c.clientId === choice.adopt)?.displayName);
      if (setupVariant === "forbidden") {
        const roles = ["Application Administrator", "Cloud Application Administrator", "Global Administrator"];
        if (steps[0]) setupAt(id, 600, () => setupEmit(id, { kind: "progress", message: steps[0] }));
        setupAt(id, 1700, () => {
          setups.delete(id);
          setupEmit(id, {
            kind: "error", code: "forbidden", roles,
            message: `this account is not allowed to create or update the app registration: it needs the role ${roles.join(" or ")}`,
          });
        });
        return;
      }
      steps.forEach((m, i) => setupAt(id, 600 + i * 1100, () => setupEmit(id, { kind: "progress", message: m })));
      setupAt(id, 600 + steps.length * 1100 + 300, () => {
        st.result = mockResult(st.plan, choice, st.start.name);
        // The session stays open until the wizard saves the account (finish) or leaves (cancel).
        setupEmit(id, { kind: "result", message: "done", result: st.result });
      });
    },
    async finish(id) {
      if (!setups.delete(id)) throw new Error("this app setup has not finished");
    },
    async cancel(id) {
      setupClear(id);
      const st = setups.get(id);
      if (!st) return { secretRemoved: false, error: "" };
      setups.delete(id);
      if (st.result?.secretCreated) {
        await wait(400);
        return q.get("revokefail") === "1"
          ? { secretRemoved: false, error: `a client secret named "e5extend dev-box ${st.start.name} 2026-11-16" with key ID 4b1c0d2e-77aa-4e0c-9a31-5d2f6c8e9a10 is still live in Entra and must be deleted (Entra admin center, App registrations, Certificates & secrets): Graph is unreachable` }
          : { secretRemoved: true, error: "" };
      }
      if (!st.result) setupEmit(id, { kind: "error", code: "cancelled", message: "app setup cancelled" });
      return { secretRemoved: false, error: "" } satisfies AppSetupCancelResult;
    },
    onEvent(id, cb) {
      let set = setupListeners.get(id);
      if (!set) setupListeners.set(id, (set = new Set()));
      set.add(cb);
      return () => void set.delete(cb);
    },
  },
  logs: {
    async read(account, sinceHours, limit) {
      await lag();
      tick();
      const since = Date.now() - sinceHours * 3_600_000;
      return logs
        .filter((l) => (!account || l.account === account) && Date.parse(l.ts) >= since)
        .slice(-limit)
        .reverse();
    },
    async apiStats(account) {
      await lag();
      tick();
      const m = new Map<string, APIStat>();
      for (const l of logs) {
        if (!l.api || l.api === "users.pick" || (account && l.account !== account)) continue;
        const c = CATALOG.find((x) => x.id === l.api);
        const st = m.get(l.api) ?? { id: l.api, category: c?.category ?? "", name: c?.name ?? l.api, ok: 0, failed: 0, total: 0, lastClass: "", lastStatus: 0, lastAt: null };
        if (l.class === "ok") st.ok++;
        else st.failed++;
        st.total++;
        if (!st.lastAt || Date.parse(l.ts) >= Date.parse(st.lastAt)) Object.assign(st, { lastClass: l.class, lastStatus: l.status, lastAt: l.ts });
        m.set(l.api, st);
      }
      return [...m.values()];
    },
  },
  settings: {
    async get() {
      await lag();
      return structuredClone(settings);
    },
    async save(s) {
      await wait(300);
      const errs = validateSettings(s);
      if (errs.length) return errs;
      // start at login is applied by setStartAtLogin, never by save
      settings = { ...structuredClone(s), notify: { ...s.notify, webhookConfigured: settings.notify.webhookConfigured, channels: settings.notify.channels }, ui: { ...s.ui, startAtLogin: settings.ui.startAtLogin } };
      return [];
    },
    async setStartAtLogin(on) {
      await lag();
      if (q.get("autostart") === "fail") throw new Error("start at login: permission denied");
      settings.ui.startAtLogin = on;
    },
    async listChannels() {
      await lag();
      return structuredClone(mockChannels.map((c) => c.view));
    },
    async saveChannel(c, create) {
      await wait(300);
      const bad: ValidationError[] = [];
      const need = (field: string, v: string) => !v.trim() && bad.push({ field, message: `is required for type ${c.type}` });
      if (!/^[a-z0-9-]{1,32}$/.test(c.id)) bad.push({ field: "id", message: "must be 1-32 of a-z, 0-9 and -" });
      if (c.type === "telegram") need("chatId", c.chatId);
      if (c.type === "email") { need("smtpHost", c.smtpHost); need("from", c.from); if (c.to.length === 0) bad.push({ field: "to", message: "is required for type email" }); }
      if (c.type === "ntfy") need("topic", c.topic);
      if (c.template.includes("{{.Nope}}")) bad.push({ field: "template", message: "template: can't evaluate field Nope (checked with the example needs_attention event)" });
      const idx = mockChannels.findIndex((x) => x.view.id === c.id);
      if (create && idx >= 0) bad.push({ field: "id", message: "a channel with this id already exists" });
      const prev = idx >= 0 ? mockChannels[idx].secrets : {};
      const secrets: Record<string, string> = { ...prev };
      for (const [k, v] of Object.entries(c.secrets)) {
        if (!v.trim()) continue;
        if (k === "url" && !/^https:\/\//.test(v)) bad.push({ field: "secrets.url", message: "the address must be an https:// URL" });
        secrets[k] = v;
      }
      for (const k of c.clearSecrets) delete secrets[k];
      for (const [name, required] of SECRETS[c.type]) if (required && !secrets[name]) bad.push({ field: `secrets.${name}`, message: "is required" });
      if (bad.length) return bad;
      const view: ChannelView = {
        ...structuredClone(c), secrets: {}, clearSecrets: [],
        secretFields: SECRETS[c.type].map(([name, required]): SecretStatus => ({ name, required, set: !!secrets[name] })),
        legacy: false, lastResult: idx >= 0 ? mockChannels[idx].view.lastResult : null,
        warnings: c.server.startsWith("http://") ? ["server_http"] : c.type === "email" && c.security === "none" ? ["email_plain"] : [],
      };
      if (idx >= 0) mockChannels[idx] = { view, secrets };
      else mockChannels.push({ view, secrets });
      syncChannelCount();
      return [];
    },
    async removeChannel(id) {
      await lag();
      mockChannels = mockChannels.filter((c) => c.view.id !== id);
      syncChannelCount();
    },
    async testChannel(id) {
      await wait(700);
      const targets = mockChannels.filter((c) => !id || c.view.id === id);
      if (targets.length === 0) throw new Error(id ? `no channel named "${id}"` : "no notification channel is set; run: e5extend notify channel add <id> --type <type>");
      return targets.map((c): ChannelTest => {
        const fail = c.view.id.includes("bad") || (c.view.type === "email" && c.view.lastResult?.ok === false);
        c.view.lastResult = fail ? { at: iso(Date.now()), ok: false, error: `${c.view.type} answered HTTP 500` } : { at: iso(Date.now()), ok: true };
        return { id: c.view.id, ok: !fail, error: fail ? `${c.view.type} answered HTTP 500` : "" };
      });
    },
    async previewMessage(type, language, template, _format, event) {
      await wait(150);
      if (template.includes("{{.Nope}}")) throw new Error("template: can't evaluate field Nope (checked with the example needs_attention event)");
      const zh = language === "zh" || (language === "" && (settings.ui.language === "zh" || (settings.ui.language === "auto" && navigator.language.startsWith("zh"))));
      const label = (EVENT_LABEL[event] ?? [event, event])[zh ? 0 : 1];
      const SUB = "of tenant contoso.onmicrosoft.com";
      const msg = ({
        failure_spike: "9 of the last 12 calls failed (75%) over the last 3 rounds",
        subscription_expiring: `subscription DEVELOPERPACK_E5 ${SUB} expires in 7 days (2027-02-14); renew it in the Microsoft 365 admin center`,
        subscription_status: `subscription DEVELOPERPACK_E5 ${SUB} changed from Enabled to Warning; check it in the Microsoft 365 admin center (Billing > Your products)`,
        subscription_renewed: `subscription DEVELOPERPACK_E5 ${SUB} was renewed: the end date moved from 2027-02-14 to 2027-05-14`,
        daily_summary: "daily summary: 2 accounts, 41 calls ok, 1 failed, 2 throttled in the last 24 hours",
        update_available: "version 0.3.0 is available (this is 0.2.0): https://github.com/KKazuhaK/e5-developer-extend/releases/tag/v0.3.0",
      } as Record<string, string>)[event] ?? "needs attention: login required; run: e5extend account login demo";
      const bold = (x: string) => (type === "telegram" ? `<b>${x}</b>` : type === "slack" ? `*${x}*` : ["wecom", "dingtalk", "discord", "serverchan"].includes(type) ? `**${x}**` : x);
      const body = template.trim()
        ? template.replace(/\{\{\s*\.(\w+)\s*\}\}/g, (_m, k: string) => (k === "Event" ? event : k === "Account" ? "demo" : k === "Message" ? msg : k === "Level" ? "warning" : ""))
        : [bold(`[${zh ? "警告" : "Warning"}] ${label}`) + " · demo", msg, `2026-11-16 20:44 PST · dev-box`].join(type === "wecom" || type === "dingtalk" ? "\n\n" : "\n");
      return {
        title: `E5 Developer Extend · ${label} · demo`, body, html: type === "email" ? `<html><body><h2>${label}</h2><p>${msg}</p></body></html>` : "",
        format: type === "telegram" ? "html" : type === "webhook" ? "json" : ["wecom", "dingtalk", "discord", "slack", "serverchan"].includes(type) ? "markdown" : "text",
      } satisfies MessagePreview;
    },
    async emailPresets() {
      await lag();
      return [
        { name: "gmail", host: "smtp.gmail.com", port: 587, security: "starttls", note: "an app password" },
        { name: "outlook", host: "smtp-mail.outlook.com", port: 587, security: "starttls", note: "an app password" },
        { name: "qq", host: "smtp.qq.com", port: 465, security: "tls", note: "the SMTP authorization code" },
        { name: "163", host: "smtp.163.com", port: 465, security: "tls", note: "the SMTP authorization code" },
      ] satisfies EmailPreset[];
    },
  },
  backup: {
    async targetHasData() {
      await lag();
      return accounts.length > 0;
    },
    async export(passphrase, withLogs, _title) {
      await wait(600);
      if ([...passphrase].length < 8) return { cancelled: false, summary: emptyExport, problem: { code: "short_passphrase", names: [], detail: "" } };
      lastBackup = { passphrase, accounts: accounts.map((a) => a.input.name) };
      return {
        cancelled: false, problem: null,
        summary: {
          path: "~/e5extend-backup-20261116.e5backup", accounts: lastBackup.accounts, vaultEntries: accounts.length * 2 + 1,
          includesLogs: withLogs, logFiles: withLogs ? 3 : 0, notes: [], noteCodes: [],
        },
      };
    },
    async import(passphrase, mode, _title) {
      await wait(800);
      const fail = (code: string, names: string[] = []): BackupImportResult => ({ cancelled: false, summary: emptyImport, problem: { code, names, detail: "" } });
      if (!lastBackup) return fail("not_backup");
      if (passphrase !== lastBackup.passphrase) return fail("wrong_passphrase");
      const names = lastBackup.accounts;
      if (mode === "default" && accounts.length > 0) return fail("target_has_data");
      if (mode === "merge") {
        const clash = names.filter((n) => accounts.some((a) => a.input.name === n));
        if (clash.length) return fail("merge_conflict", clash);
      }
      return {
        cancelled: false, problem: null,
        summary: {
          mode, accounts: names, vaultEntries: names.length * 2, sourceOS: "linux", sourceVersion: "0.2.0",
          created: new Date().toISOString(), newMasterKey: false,
          skipped: [], notes: [], hint: "",
          skippedCodes: mode === "merge" ? [{ code: "merge_settings", args: [] }] : [], noteCodes: [],
        },
      };
    },
  },
  update: {
    async info() {
      await lag();
      return mockUpdateInfo();
    },
    async check() {
      await wait(rand(500, 900));
      updateCheckedAt = Date.now();
      return mockUpdateInfo();
    },
    async install(_version) {
      await lag();
      if (updateRunning) throw new Error("an upgrade is already running");
      void runMockUpgrade();
    },
    async restart() {
      await lag();
    },
    onProgress(cb) {
      updateListeners.add(cb);
      // Opening the page with ?update=downloading shows an upgrade that is already under way.
      let timer: ReturnType<typeof setTimeout> | undefined;
      if (updateVariant === "downloading") {
        timer = setTimeout(() => cb({ stage: "download", file: updateFile, done: Math.round(UPDATE_SIZE * 0.42), total: UPDATE_SIZE, result: null, code: "", error: "" }), 400);
      }
      return () => {
        updateListeners.delete(cb);
        if (timer) clearTimeout(timer);
      };
    },
  },
  subscription: {
    async list() {
      await lag();
      return mockTenants();
    },
    async refresh(account) {
      await wait(rand(500, 900));
      const key = account ? accounts.find((a) => a.input.name === account)?.input.tenant.toLowerCase() : null;
      for (const t of mockTenants()) if (!key || t.tenant === key) subCheckedAt.set(t.tenant, Date.now());
      return { tenants: mockTenants(), queued: false };
    },
    async setExpires(account, date) {
      await lag();
      const a = accounts.find((x) => x.input.name === account);
      if (!a) throw new Error(`no account named "${account}"`);
      if (date && !/^\d{4}-\d{2}-\d{2}$/.test(date)) throw new Error(`not a YYYY-MM-DD date: "${date}"`);
      const key = a.input.tenant.toLowerCase();
      if (date) subManual.set(key, date);
      else subManual.delete(key);
    },
  },
  health: {
    async doctor(offline) {
      await wait(offline ? 300 : 900);
      // English titles and text plus codes with values: this is what the real backend returns.
      const none = controls.host === "none";
      const on = settings.notify.channels > 0;
      const checks: DoctorCheck[] = [
        dx("config", "Configuration", "ok", [["config.ok", { count: String(accounts.length) }]]),
        dx("vault", "Vault", "ok", [["vault.ok", { count: String(accounts.length * 2) }]]),
        none
          ? dx("engine", "Engine", "fail", [["engine.stopped"]], [["fix.engineStart"]])
          : dx("engine", "Engine", "ok", [["engine.running", { seconds: "14" }]]),
        service.installed
          ? dx("service", "Background service", "ok", [["service.okRunning"]])
          : dx("service", "Background service", "warn", [["service.notInstalled"]], [["fix.serviceInstall"]]),
        service.installed
          ? dx("linger", "Linger", "ok", [["linger.on"]])
          : dx("linger", "Linger", "warn", [["linger.off"]], [["fix.linger"]]),
        ...(accounts.some((a) => a.input.name === "lab")
          ? [
              dx("account.lab.token", "Account lab sign-in", "fail", [["token.loginRequired"]], [["hint.loginRequired", { account: "lab" }]]),
              dx("account.lab.state", "Account lab state", "fail", [["state.needsAttention", { reason: "login required" }]], [["fix.resumeAfter", { account: "lab" }]]),
            ]
          : []),
        ...(accounts.some((a) => a.input.name === "main")
          ? [
              dx("account.main.token", "Account main sign-in", "ok", [["token.ok", { seconds: "3300" }]]),
              dx("account.main.scopes", "Account main permissions", "ok", [["scopes.ok", { count: "15" }]]),
              dx("account.main.activity", "Account main activity", "ok", [["activity.last", { seconds: "1140" }]]),
              dx("account.main.state", "Account main state", "ok", [["state.ok"]]),
              dx("account.main.apis", "Account main APIs", "ok", [["apis.ok", { count: "3" }]]),
            ]
          : []),
        ...(demo
          ? accounts.flatMap((a): DoctorCheck[] => [
              dx(`account.${a.input.name}.token`, `Account ${a.input.name} sign-in`, "ok", [["token.ok", { seconds: "3300" }]]),
              dx(`account.${a.input.name}.scopes`, `Account ${a.input.name} permissions`, "ok", [["scopes.ok", { count: String(catalogFor(a.input.name).filter((c) => c.usable).length) }]]),
              dx(`account.${a.input.name}.activity`, `Account ${a.input.name} activity`, "ok", [["activity.last", { seconds: "1140" }]]),
            ])
          : []),
        ...(accounts.some((a) => a.input.name === "dev-app")
          ? [dx("account.dev-app.secret", "Account dev-app client secret", "warn", [["secret.warn", { count: "12", date: day(12) }]], [["fix.newSecret", { account: "dev-app" }]])]
          : []),
        dx("subscription.contoso.onmicrosoft.com", "Subscription contoso.onmicrosoft.com", "ok", [["sub.skuEnds", { sku: "DEVELOPERPACK_E5", status: "Enabled", date: day(80), offset: "80" }]]),
        dx("clock", "Clock", "ok", [["clock.ok", { seconds: "120" }]]),
        dx("network", "Network", "ok", [["net.ok", { status: "401" }]]),
        on
          ? dx("notify", "Alerts", "ok", [[settings.notify.channels === 1 ? "notify.one" : "notify.many", { count: String(settings.notify.channels) }]])
          : dx("notify", "Alerts", "warn", [["notify.none"]], [["fix.channelAdd"]]),
        ...(updateVariant !== "none" && updateVariant !== "manual"
          ? [dx("update", "Update", "ok", [["update.available", { current: "0.2.0", latest: "0.3.0", url: "https://github.com/KKazuhaK/e5-developer-extend/releases/tag/v0.3.0" }]], [["fix.update"]])]
          : []),
        // A code this version has no text for: the English text is shown (hidden in the clean demo data set).
        ...(demo ? [] : [{ id: "future.check", title: "Some future check", level: "ok", detail: "unknown codes fall back to the backend text", fix: "", parts: [{ code: "future.thing", params: {} }], fixParts: [] } as DoctorCheck]),
      ];
      if (offline) return checks.filter((c) => c.id !== "clock" && c.id !== "network" && !c.id.endsWith(".token"));
      return checks;
    },
    async serviceInfo() {
      await lag();
      return serviceInfo();
    },
    async installService() {
      await wait(900);
      service.installed = true;
      mockControls.set({ host: "service" }); // the tray hands the engine over
      return {
        actions: ["wrote ~/.config/systemd/user/e5extend.service", "enabled and started e5extend.service"],
        warnings: ["the tray app is also running; the service takes over the engine"],
        manual: ["Run once so it survives sign-out: loginctl enable-linger $USER"],
      };
    },
    async startService() {
      await wait(700);
      if (!service.installed) throw new Error("the background service is not installed");
      mockControls.set({ host: "service" });
      return { actions: ["ran systemctl --user start e5extend.service"], warnings: [], manual: [] };
    },
    async uninstallService() {
      await wait(700);
      service.installed = false;
      if (controls.host === "service") mockControls.set({ host: "gui" });
      return {
        actions: ["stopped and removed e5extend.service"],
        warnings: ["the master key file (master.key in the config directory) was left in place: it may be the only copy of the vault key; delete it yourself if you do not need it"],
        manual: [],
      };
    },
  },
  catalog: {
    async list(account) {
      await lag();
      return catalogFor(account);
    },
    async categories() {
      await lag();
      return categories();
    },
    async defaultCategories() {
      await lag();
      return categories().filter((c) => c !== "reports");
    },
  },
  system: {
    onNavigate(cb) {
      navListeners.add(cb);
      return () => void navListeners.delete(cb);
    },
    async openExternal(url) {
      window.open(url, "_blank", "noopener,noreferrer");
    },
  },
};
