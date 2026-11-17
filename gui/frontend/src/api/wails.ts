import { Browser, Events } from "@wailsio/runtime";
import {
  AccountService, BackupService, CatalogService, HealthService, LogService, SettingsService, StatusService,
} from "../../bindings/github.com/KKazuhaK/e5-renew/gui/services";
import type { Api } from "./interface";
import type { LoginEvent, RunDone, Snapshot } from "./types";

// The bindings are generated (gitignored); see README. Generated models are
// classes whose structure matches ./types.ts, so results are cast. Go nil
// slices arrive as null: normalise them so the UI can map over them.
const arr = <T>(a: T[] | null | undefined): T[] => a ?? [];
const cast = <T>(x: unknown) => x as T;
const note = (n: { code: string; args: string[] | null }) => ({ ...n, args: arr(n.args) });

// Wails delivers events as { name, data }; data is the first emitted argument,
// or an array of arguments when the backend emitted several.
function payload<T>(ev: { data?: unknown }): T {
  const d = ev?.data;
  return (Array.isArray(d) ? d[0] : d) as T;
}

export const wailsApi: Api = {
  status: {
    snapshot: async () => {
      const s = cast<Snapshot>(await StatusService.Snapshot());
      return { ...s, accounts: arr(s.accounts) };
    },
    runNow: (names) => StatusService.RunNow(names),
    onRunDone: (cb) => Events.On("run:done", (ev) => cb(payload<RunDone>(ev))),
  },
  accounts: {
    add: (i) => AccountService.Add(cast(i)),
    update: (i) => AccountService.Update(cast(i)),
    remove: (n) => AccountService.Remove(n),
    setSecret: (n, s) => AccountService.SetSecret(n, s),
    login: (n) => AccountService.Login(n),
    cancelLogin: (n) => AccountService.CancelLogin(n),
    test: async (n) => {
      const r = await AccountService.Test(n);
      return { ...r, scopes: arr(r.scopes) };
    },
    pause: (n) => AccountService.Pause(n),
    resume: (n) => AccountService.Resume(n),
    get: async (n) => {
      const a = await AccountService.Get(n);
      return { ...cast<import("./types").AccountInput>(a), categories: arr(a.categories) };
    },
    onLoginEvent: (name, cb) => Events.On(`login:${name}`, (ev) => cb(payload<LoginEvent>(ev))),
  },
  logs: { read: async (a, h, l) => arr(cast<import("./types").LogEntry[]>(await LogService.Read(a, h, l))) },
  settings: {
    get: async () => {
      const s = cast<import("./types").Settings>(await SettingsService.Get());
      return {
        ...s,
        schedule: { ...s.schedule, workDays: arr(s.schedule.workDays) },
        notify: { ...s.notify, events: arr(s.notify.events) },
      };
    },
    save: async (s) => arr(await SettingsService.Save(cast(s))),
    setWebhook: (u) => SettingsService.SetWebhook(u),
    clearWebhook: () => SettingsService.ClearWebhook(),
    testWebhook: () => SettingsService.TestWebhook(),
  },
  backup: {
    targetHasData: () => BackupService.TargetHasData(),
    export: async (p, l, t) => {
      const r = cast<import("./types").BackupExportResult>(await BackupService.Export(p, l, t));
      const s = r.summary;
      return {
        ...r, problem: r.problem ? { ...r.problem, names: arr(r.problem.names) } : null,
        summary: { ...s, accounts: arr(s.accounts), notes: arr(s.notes), noteCodes: arr(s.noteCodes).map(note) },
      };
    },
    import: async (p, m, t) => {
      const r = cast<import("./types").BackupImportResult>(await BackupService.Import(p, m, t));
      const s = r.summary;
      return {
        ...r, problem: r.problem ? { ...r.problem, names: arr(r.problem.names) } : null,
        summary: {
          ...s, accounts: arr(s.accounts), skipped: arr(s.skipped), notes: arr(s.notes),
          skippedCodes: arr(s.skippedCodes).map(note), noteCodes: arr(s.noteCodes).map(note),
        },
      };
    },
  },
  health: {
    doctor: async (o) => arr(cast<import("./types").DoctorCheck[]>(await HealthService.Doctor(o))),
    serviceInfo: async () => {
      const i = await HealthService.ServiceInfo();
      return { ...i, details: arr(i.details) };
    },
    installService: async () => norm(await HealthService.InstallService()),
    uninstallService: async () => norm(await HealthService.UninstallService()),
  },
  catalog: {
    list: async (a) => arr(await CatalogService.List(a)).map((e) => ({ ...e, scopes: arr(e.scopes) })),
    categories: async () => arr(await CatalogService.Categories()),
  },
  system: {
    openExternal: async (url) => {
      await Browser.OpenURL(url);
    },
    onNavigate: (cb) => Events.On("navigate", (ev) => cb(payload<{ page?: unknown }>(ev)?.page)),
  },
};

function norm(r: { actions: string[] | null; warnings: string[] | null; manual: string[] | null }) {
  return { actions: arr(r.actions), warnings: arr(r.warnings), manual: arr(r.manual) };
}
