// In-browser fake backend implementing the contract in ./interface.ts.
// Used when VITE_MOCK=1. Text that comes from the real backend (reasons, fixes,
// doctor details) is Chinese here, as the Go side will produce.
import type { Api } from "./interface";
import type {
  AccountInput, BackupImportResult, AccountStatus, AccountSummary, CatalogEntry, DoctorCheck, LogEntry, LoginEvent, RunDone, ServiceInfo,
  Settings, Snapshot, ValidationError,
} from "./types";

type Host = "gui" | "service" | "none";

// ---- controls (used by MockPanel and by ?host=none&empty=1 in the URL) ----
interface Controls { host: Host; empty: boolean; slow: boolean }
const q = new URLSearchParams(typeof location === "undefined" ? "" : location.search);
let controls: Controls = {
  host: (["gui", "service", "none"].includes(q.get("host") ?? "") ? q.get("host") : "gui") as Host,
  empty: q.get("empty") === "1",
  slow: q.get("slow") === "1",
};
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
  };
}

let accounts: MockAccount[] = [];
function seedAccounts() {
  accounts = [
    mk("main", "device_code", "contoso.onmicrosoft.com", "ok", { categories: CATS_ALL.slice(0, 10), todayFailed: 1 }),
    mk("dev-app", "client_secret", "7d3f2a10-55c1-4c88-b1d4-0a3e9c61f2b8", "ok", { secretExpires: day(12), categories: CATS_ALL }),
    mk("sales-demo", "browser", "fabrikam.onmicrosoft.com", "ok", { categories: CATS_ALL.slice(0, 6) }),
    mk("lab", "device_code", "labtenant.onmicrosoft.com", "needs_attention", {
      reason: "刷新令牌已失效（invalid_grant）：密码被重置或会话被撤销",
      fix: "重新登录该账号以获取新的令牌",
      lastRunAt: now0 - 5 * 3_600_000, lastSuccessAt: now0 - 31 * 3_600_000, todayFailed: 6,
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
  const failed = a.status === "needs_attention" ? true : r < 0.06;
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
  if (accounts.length) {
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

// ---- settings / health state ----
let settings: Settings = {
  schedule: {
    intervalMin: "20m", intervalMax: "45m", apisPerRoundMin: 3, apisPerRoundMax: 6, randomOrder: true,
    workDays: ["mon", "tue", "wed", "thu", "fri", "sat", "sun"], workStart: "00:00", workEnd: "23:59",
    periodStart: "", periodEnd: day(300),
  },
  network: { proxy: "" },
  notify: { staleAfter: "72h", events: ["needs_attention", "auto_paused", "stale"], webhookConfigured: false },
  ui: { language: "auto", theme: "system", startAtLogin: false },
};
let service: { installed: boolean } = { installed: false };
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
        todayOk: a.todayOk, todayFailed: a.todayFailed, usableApis: usable, totalApis: catalogFor(a.input.name).length,
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

function validateSettings(s: Settings): ValidationError[] {
  const out: ValidationError[] = [];
  if (s.network.proxy && /bad/.test(s.network.proxy)) out.push({ field: "network.proxy", message: "not a valid URL: " + s.network.proxy });
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
      const { secret, ...rest } = input;
      void secret; // never stored in the mock either
      accounts.push({
        input: rest, status: "not_signed_in", reason: "尚未登录", fix: "完成一次登录后才会开始调用",
        lastRunAt: null, lastSuccessAt: null, nextRunAt: null, todayOk: 0, todayFailed: 0,
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
      settings = { ...structuredClone(s), notify: { ...s.notify, webhookConfigured: settings.notify.webhookConfigured }, ui: { ...s.ui, startAtLogin: settings.ui.startAtLogin } };
      return [];
    },
    async setWebhook(url) {
      await wait(300);
      if (!/^https:\/\//.test(url)) throw new Error("Webhook 地址必须以 https:// 开头");
      settings.notify.webhookConfigured = true;
    },
    async setStartAtLogin(on) {
      await lag();
      if (q.get("autostart") === "fail") throw new Error("start at login: permission denied");
      settings.ui.startAtLogin = on;
    },
    async clearWebhook() {
      await lag();
      settings.notify.webhookConfigured = false;
    },
    async testWebhook() {
      await wait(700);
      if (!settings.notify.webhookConfigured) throw new Error("还没有设置 Webhook");
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
  health: {
    async doctor(offline) {
      await wait(offline ? 300 : 900);
      // English titles, details and fixes: this is what the real backend returns.
      const checks: DoctorCheck[] = [
        { id: "config", title: "Configuration", level: "ok", detail: "~/.config/e5extend/config.toml is readable and valid", fix: "" },
        { id: "vault", title: "Vault", level: "ok", detail: "master key stored in the system keychain", fix: "" },
        { id: "engine", title: "Engine", level: controls.host === "none" ? "warn" : "ok", detail: controls.host === "none" ? "no engine is running" : `running (${controls.host})`, fix: controls.host === "none" ? "start the tray app or run: e5extend service install" : "" },
        service.installed
          ? { id: "service", title: "Background service", level: "ok", detail: "installed and enabled", fix: "" }
          : { id: "service", title: "Background service", level: "warn", detail: "not installed: nothing runs after you sign out", fix: "run: e5extend service install" },
        { id: "linger", title: "Linger", level: service.installed ? "ok" : "warn", detail: service.installed ? "lingering is on" : "lingering is off", fix: service.installed ? "" : "run: loginctl enable-linger $USER" },
        { id: "clock", title: "Clock", level: "ok", detail: offline ? "skipped (offline)" : "0.4 s from Microsoft's servers", fix: "" },
        offline
          ? { id: "network", title: "Network", level: "warn", detail: "skipped (offline)", fix: "" }
          : { id: "network", title: "Network", level: "ok", detail: "login.microsoftonline.com and graph.microsoft.com reachable (87 ms)", fix: "" },
        ...(accounts.some((a) => a.input.name === "lab")
          ? [
              { id: "account.lab.token", title: "lab sign-in", level: "fail", detail: "refresh token rejected (invalid_grant)", fix: "run: e5extend account login lab" } as DoctorCheck,
              { id: "account.lab.state", title: "lab state", level: "fail", detail: "needs attention", fix: "sign in again, then resume the account" } as DoctorCheck,
            ]
          : []),
        ...(accounts.some((a) => a.input.name === "main")
          ? [
              { id: "account.main.token", title: "main sign-in", level: "ok", detail: "token valid", fix: "" } as DoctorCheck,
              { id: "account.main.scopes", title: "main permissions", level: "ok", detail: "15 of 18 APIs usable", fix: "" } as DoctorCheck,
              { id: "account.main.activity", title: "main activity", level: "ok", detail: "last success 19 minutes ago", fix: "" } as DoctorCheck,
            ]
          : []),
        ...(accounts.some((a) => a.input.name === "dev-app")
          ? [{ id: "account.dev-app.secret", title: "dev-app client secret", level: "warn", detail: `expires in 12 days (${day(12)})`, fix: "create a new secret in Entra, then run: e5extend account set-secret dev-app" } as DoctorCheck]
          : []),
        { id: "notify", title: "Alerts", level: settings.notify.webhookConfigured ? "ok" : "warn", detail: settings.notify.webhookConfigured ? "webhook configured" : "no webhook configured", fix: settings.notify.webhookConfigured ? "" : "set one in Settings" },
        { id: "future.check", title: "Some future check", level: "ok", detail: "unknown ids fall back to the backend title", fix: "" },
      ];
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
    async uninstallService() {
      await wait(700);
      service.installed = false;
      if (controls.host === "service") mockControls.set({ host: "gui" });
      return { actions: ["stopped and removed e5extend.service"], warnings: [], manual: [] };
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
