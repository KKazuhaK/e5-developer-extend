import { Browser, Events } from "@wailsio/runtime";
import {
  AccountService, AppSetupService, BackupService, CatalogService, HealthService, LogService, SettingsService, StatusService,
} from "../../bindings/github.com/KKazuhaK/e5-developer-extend/gui/services";
import type { Api } from "./interface";
import type { AppSetupEvent, AppSetupPlan, LoginEvent, RunDone, Snapshot } from "./types";

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
    loginAfterSetup: (n) => AccountService.LoginAfterSetup(n),
    cancelLogin: (n) => AccountService.CancelLogin(n),
    test: async (n) => {
      const r = await AccountService.Test(n);
      return { ...r, scopes: arr(r.scopes) };
    },
    recommend: async (n) => {
      const r = cast<import("./types").RecommendResult>(await AccountService.Recommend(n));
      return { ...r, categories: arr(r.categories), recommended: arr(r.recommended) };
    },
    pause: (n) => AccountService.Pause(n),
    resume: (n) => AccountService.Resume(n),
    get: async (n) => {
      const a = await AccountService.Get(n);
      return { ...cast<import("./types").AccountInput>(a), categories: arr(a.categories) };
    },
    onLoginEvent: (name, cb) => Events.On(`login:${name}`, (ev) => cb(payload<LoginEvent>(ev))),
  },
  appSetup: {
    begin: (id, s) => AppSetupService.Begin(id, cast(s)),
    preview: async (id, c) => planOf(cast<AppSetupPlan>(await AppSetupService.Preview(id, cast(c)))),
    apply: (id, c) => AppSetupService.Apply(id, cast(c)),
    finish: (id) => AppSetupService.Finish(id),
    cancel: async (id) => cast<import("./types").AppSetupCancelResult>(await AppSetupService.Cancel(id)),
    onEvent: (id, cb) =>
      Events.On(`appsetup:${id}`, (ev) => {
        const e = payload<AppSetupEvent>(ev);
        cb(e.plan ? { ...e, plan: planOf(e.plan) } : e);
      }),
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
    setStartAtLogin: (on) => SettingsService.SetStartAtLogin(on),
    listChannels: async () =>
      arr(cast<import("./types").ChannelView[]>(await SettingsService.ListChannels())).map((c) => ({
        ...c, events: arr(c.events), to: arr(c.to), clearSecrets: arr(c.clearSecrets), secrets: c.secrets ?? {},
        secretFields: arr(c.secretFields), warnings: arr(c.warnings), lastResult: c.lastResult ?? null,
      })),
    saveChannel: async (c, create) => arr(await SettingsService.SaveChannel(cast(c), create)),
    removeChannel: (id) => SettingsService.RemoveChannel(id),
    testChannel: async (id) => arr(cast<import("./types").ChannelTest[]>(await SettingsService.TestChannel(id))),
    previewMessage: async (type, language, template, format, event) =>
      cast<import("./types").MessagePreview>(await SettingsService.PreviewMessage(type, language, template, format, event)),
    emailPresets: async () => arr(cast<import("./types").EmailPreset[]>(await SettingsService.EmailPresets())),
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
    startService: async () => norm(await HealthService.StartService()),
    uninstallService: async () => norm(await HealthService.UninstallService()),
  },
  catalog: {
    list: async (a) => arr(await CatalogService.List(a)).map((e) => ({ ...e, scopes: arr(e.scopes) })),
    categories: async () => arr(await CatalogService.Categories()),
    defaultCategories: async () => arr(await CatalogService.DefaultCategories()),
  },
  system: {
    openExternal: async (url) => {
      await Browser.OpenURL(url);
    },
    onNavigate: (cb) => Events.On("navigate", (ev) => cb(payload<{ page?: unknown }>(ev)?.page)),
  },
};

const planOf = (p: AppSetupPlan): AppSetupPlan => ({
  ...p, tags: arr(p.tags), permissions: arr(p.permissions), missing: arr(p.missing), candidates: arr(p.candidates), actions: arr(p.actions),
});

function norm(r: { actions: string[] | null; warnings: string[] | null; manual: string[] | null }) {
  return { actions: arr(r.actions), warnings: arr(r.warnings), manual: arr(r.manual) };
}
