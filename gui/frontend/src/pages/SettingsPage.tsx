import { useCallback, useEffect, useMemo, useState } from "react";
import { BellRing, CheckCircle2, ChevronDown, Globe, Palette, Save, Send, SlidersHorizontal, Timer, Trash2 } from "lucide-react";
import { api } from "@/api";
import type { Settings, ValidationError } from "@/api";
import { useApp } from "@/state/app";
import { useI18n, type DictKey } from "@/i18n";
import { useToast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { ErrorBlock, Field, LoadingBlock, PageHeader, Spinner, fieldAria } from "@/components/common";
import { NOTIFY_EVENTS, WEEKDAYS, errorsByField, isValidWebhook, validateSettings, type LocalCode } from "@/lib/settings";
import { cn, errMsg } from "@/lib/utils";
import { BackupCard } from "./BackupCard";
import { PaceCard } from "./PaceCard";
import { RunningCard } from "./RunningCard";

/** Form field (as the validators name it) to the id of its control, in page order. Every one lives in Advanced settings. */
const ADVANCED_FIELDS: [string, string][] = [
  ["schedule.intervalMin", "s-imin"], ["schedule.intervalMax", "s-imax"], ["schedule.apisPerRoundMin", "s-amin"],
  ["schedule.apisPerRoundMax", "s-amax"], ["schedule.workDays", "s-days-l"], ["schedule.workStart", "s-ws"],
  ["schedule.workEnd", "s-we"], ["schedule.periodStart", "s-ps"], ["schedule.periodEnd", "s-pe"],
  ["network.proxy", "n-proxy"], ["notify.staleAfter", "n-stale"], ["notify.events", "n-ev-l"],
];
const ADVANCED_KEY = "e5extend.settings.advancedOpen";

function readAdvancedOpen(): boolean {
  try {
    return localStorage.getItem(ADVANCED_KEY) === "1";
  } catch {
    return false;
  }
}
function writeAdvancedOpen(open: boolean) {
  try {
    localStorage.setItem(ADVANCED_KEY, open ? "1" : "0");
  } catch {
    /* storage can be unavailable; the section just does not remember */
  }
}

const LOCAL_MSG: Record<LocalCode, DictKey> = {
  duration: "settings.err.duration", minInterval: "settings.err.minInterval", intervalOrder: "settings.err.intervalOrder",
  posInt: "settings.err.posInt", apisOrder: "settings.err.apisOrder", time: "settings.err.time", timeOrder: "settings.err.timeOrder",
  date: "settings.err.date", periodOrder: "settings.err.periodOrder", proxy: "settings.err.proxy", days: "settings.err.days",
};

export function SettingsPage() {
  const { t } = useI18n();
  const toast = useToast();
  const { setPrefs, snapshot } = useApp();
  const [saved, setSaved] = useState<Settings | null>(null);
  const [draft, setDraft] = useState<Settings | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [remote, setRemote] = useState<Record<string, string>>({});
  const [showLocal, setShowLocal] = useState(false);
  const [advOpen, setAdvOpen] = useState(readAdvancedOpen);
  const toggleAdvanced = (open: boolean) => {
    setAdvOpen(open);
    writeAdvancedOpen(open);
  };
  // Opens Advanced settings and brings the first field with an error into view.
  const revealFirstError = (keys: Set<string>) => {
    const hit = ADVANCED_FIELDS.find(([k]) => keys.has(k));
    if (!hit) return;
    setAdvOpen(true); // not remembered: it is only there to show the error
    window.setTimeout(() => {
      const el = document.getElementById(hit[1]);
      el?.scrollIntoView({ block: "center", behavior: "smooth" });
      if (el && el.tagName !== "P") el.focus({ preventScroll: true });
    }, 0);
  };

  const load = useCallback(async () => {
    try {
      const s = await api.settings.get();
      setSaved(s);
      setDraft(structuredClone(s));
      setLoadError(null);
    } catch (e) {
      setLoadError(errMsg(e));
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  const local = useMemo(() => (draft ? validateSettings(draft) : {}), [draft]);
  const dirty = useMemo(() => !!draft && !!saved && JSON.stringify(draft) !== JSON.stringify(saved), [draft, saved]);

  if (loadError) return (<><PageHeader title={t("nav.settings")} /><ErrorBlock message={loadError} onRetry={() => void load()} /></>);
  if (!draft || !saved) return (<><PageHeader title={t("nav.settings")} /><LoadingBlock rows={3} /></>);

  const upd = (fn: (d: Settings) => void) => {
    setDraft((d) => {
      const n = structuredClone(d!);
      fn(n);
      return n;
    });
  };
  const fieldErr = (f: string): string | undefined => {
    if (remote[f]) return remote[f];
    const c = showLocal ? (local as Record<string, LocalCode>)[f] : undefined;
    return c ? t(LOCAL_MSG[c]) : undefined;
  };
  // An import can change every setting, including language and theme.
  const afterImport = async () => {
    try {
      const s = await api.settings.get();
      setSaved(s);
      setDraft(structuredClone(s));
      setPrefs({ language: s.ui.language, theme: s.ui.theme });
    } catch (e) {
      toast.error(errMsg(e));
    }
    void snapshot.refresh();
  };
  const clearRemote = (f: string) => remote[f] && setRemote((r) => ({ ...r, [f]: "" }));

  const save = async () => {
    setShowLocal(true);
    if (Object.keys(local).length) {
      toast.error(t("settings.fixErrors"));
      revealFirstError(new Set(Object.keys(local)));
      return;
    }
    setSaving(true);
    try {
      const errs: ValidationError[] = await api.settings.save(draft);
      if (errs.length) {
        const byField = errorsByField(errs);
        setRemote(byField);
        toast.error(t("settings.fixErrors"));
        revealFirstError(new Set(Object.keys(byField)));
        return;
      }
      setRemote({});
      const s = await api.settings.get();
      setSaved(s);
      setDraft(structuredClone(s));
      setPrefs({ language: s.ui.language, theme: s.ui.theme });
      setShowLocal(false);
      toast.success(t("settings.saved"));
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setSaving(false);
    }
  };

  const sc = draft.schedule;
  const days = WEEKDAYS;

  return (
    <>
      <PageHeader
        sticky
        title={t("nav.settings")}
        actions={
          <>
            {dirty && <span className="text-xs text-muted-foreground" role="status">{t("settings.unsaved")}</span>}
            <Button onClick={() => void save()} disabled={saving || !dirty}>
              {saving ? <Spinner /> : <Save />} {t("common.save")}
            </Button>
          </>
        }
      />

      <div className="grid gap-4">
        <RunningCard
          startAtLogin={draft.ui.startAtLogin}
          // Applied by the backend already: record it as saved too, so it never makes the page dirty.
          onStartAtLogin={(on) => { setSaved((x) => x && { ...x, ui: { ...x.ui, startAtLogin: on } }); setDraft((d) => d && { ...d, ui: { ...d.ui, startAtLogin: on } }); }}
        />

        <PaceCard draft={draft} upd={upd} />

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2"><Palette className="size-4" aria-hidden /> {t("settings.ui")}</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-4 sm:grid-cols-2">
            <Field id="u-lang" label={t("settings.language")}>
              <Select value={draft.ui.language} onValueChange={(v) => upd((d) => void (d.ui.language = v as Settings["ui"]["language"]))}>
                <SelectTrigger id="u-lang"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">{t("settings.langAuto")}</SelectItem>
                  <SelectItem value="zh">简体中文</SelectItem>
                  <SelectItem value="en">English</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field id="u-theme" label={t("settings.theme")}>
              <Select value={draft.ui.theme} onValueChange={(v) => upd((d) => void (d.ui.theme = v as Settings["ui"]["theme"]))}>
                <SelectTrigger id="u-theme"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="system">{t("settings.themeSystem")}</SelectItem>
                  <SelectItem value="light">{t("settings.themeLight")}</SelectItem>
                  <SelectItem value="dark">{t("settings.themeDark")}</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </CardContent>
        </Card>

        <BackupCard onImported={() => { void afterImport(); }} />

        <section className="rounded-lg border bg-card">
          <h2 className="text-base font-semibold">
            <button
              type="button"
              id="adv-btn"
              aria-expanded={advOpen}
              aria-controls="adv-panel"
              onClick={() => toggleAdvanced(!advOpen)}
              className="flex w-full cursor-pointer items-center gap-2 rounded-lg px-4 py-3 text-left"
            >
              <SlidersHorizontal className="size-4" aria-hidden />
              <span className="min-w-0 flex-1">
                {t("settings.advanced")}
                <span className="block text-xs font-normal text-muted-foreground">{t("settings.advancedDesc")}</span>
                {!advOpen && ADVANCED_FIELDS.some(([k]) => fieldErr(k)) && (
                  <span role="alert" className="block text-xs font-normal text-danger">{t("settings.advancedHasError")}</span>
                )}
              </span>
              <ChevronDown className={cn("size-4 shrink-0 transition-transform", advOpen && "rotate-180")} aria-hidden />
            </button>
          </h2>
          <div id="adv-panel" role="region" aria-labelledby="adv-btn" hidden={!advOpen} className="grid gap-4 border-t p-4">
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2"><Timer className="size-4" aria-hidden /> {t("settings.schedule")}</CardTitle>
              <CardDescription>{t("settings.scheduleDesc")}</CardDescription>
            </CardHeader>
            <CardContent className="grid gap-4 sm:grid-cols-2">
              <Field id="s-imin" label={t("settings.intervalMin")} hint={t("settings.durationHint")} error={fieldErr("schedule.intervalMin")}>
                <Input {...fieldAria("s-imin", fieldErr("schedule.intervalMin"), "x")} value={sc.intervalMin} onChange={(e) => { clearRemote("schedule.intervalMin"); upd((d) => void (d.schedule.intervalMin = e.target.value)); }} />
              </Field>
              <Field id="s-imax" label={t("settings.intervalMax")} error={fieldErr("schedule.intervalMax")}>
                <Input {...fieldAria("s-imax", fieldErr("schedule.intervalMax"))} value={sc.intervalMax} onChange={(e) => { clearRemote("schedule.intervalMax"); upd((d) => void (d.schedule.intervalMax = e.target.value)); }} />
              </Field>
              <Field id="s-amin" label={t("settings.apisMin")} error={fieldErr("schedule.apisPerRoundMin")}>
                <Input {...fieldAria("s-amin", fieldErr("schedule.apisPerRoundMin"))} type="number" min={1} value={Number.isNaN(sc.apisPerRoundMin) ? "" : sc.apisPerRoundMin} onChange={(e) => { clearRemote("schedule.apisPerRoundMin"); upd((d) => void (d.schedule.apisPerRoundMin = e.target.valueAsNumber)); }} />
              </Field>
              <Field id="s-amax" label={t("settings.apisMax")} error={fieldErr("schedule.apisPerRoundMax")}>
                <Input {...fieldAria("s-amax", fieldErr("schedule.apisPerRoundMax"))} type="number" min={1} value={Number.isNaN(sc.apisPerRoundMax) ? "" : sc.apisPerRoundMax} onChange={(e) => { clearRemote("schedule.apisPerRoundMax"); upd((d) => void (d.schedule.apisPerRoundMax = e.target.valueAsNumber)); }} />
              </Field>
              <div className="sm:col-span-2">
                <p className="mb-1.5 text-[13px] font-medium" id="s-days-l">{t("settings.workDays")}</p>
                <div role="group" aria-labelledby="s-days-l" className="flex flex-wrap gap-1.5">
                  {days.map((d) => {
                    const on = sc.workDays.includes(d);
                    return (
                      <button
                        key={d}
                        type="button"
                        aria-pressed={on}
                        onClick={() => { clearRemote("schedule.workDays"); upd((x) => void (x.schedule.workDays = on ? x.schedule.workDays.filter((y) => y !== d) : days.filter((y) => y === d || x.schedule.workDays.includes(y)))); }}
                        className={cn("flex h-8 min-w-12 cursor-pointer items-center justify-center gap-1 rounded-md border px-2 text-[13px] transition-colors", on ? "border-primary bg-primary text-primary-foreground" : "bg-card text-muted-foreground hover:bg-accent")}
                      >
                        {t(`day.${d}` as DictKey)}
                      </button>
                    );
                  })}
                </div>
                {fieldErr("schedule.workDays") && <p role="alert" className="mt-1.5 text-xs text-danger">{fieldErr("schedule.workDays")}</p>}
              </div>
              <Field id="s-ws" label={t("settings.workStart")} error={fieldErr("schedule.workStart")}>
                <Input {...fieldAria("s-ws", fieldErr("schedule.workStart"))} type="time" value={sc.workStart} onChange={(e) => { clearRemote("schedule.workStart"); upd((d) => void (d.schedule.workStart = e.target.value)); }} />
              </Field>
              <Field id="s-we" label={t("settings.workEnd")} hint={t("settings.workEndHint")} error={fieldErr("schedule.workEnd")}>
                <Input {...fieldAria("s-we", fieldErr("schedule.workEnd"), "x")} type="time" value={sc.workEnd} onChange={(e) => { clearRemote("schedule.workEnd"); upd((d) => void (d.schedule.workEnd = e.target.value)); }} />
              </Field>
              <Field id="s-ps" label={t("settings.periodStart")} hint={t("settings.periodHint")} error={fieldErr("schedule.periodStart")}>
                <Input {...fieldAria("s-ps", fieldErr("schedule.periodStart"), "x")} type="date" value={sc.periodStart} onChange={(e) => { clearRemote("schedule.periodStart"); upd((d) => void (d.schedule.periodStart = e.target.value)); }} />
              </Field>
              <Field id="s-pe" label={t("settings.periodEnd")} error={fieldErr("schedule.periodEnd")}>
                <Input {...fieldAria("s-pe", fieldErr("schedule.periodEnd"))} type="date" value={sc.periodEnd} onChange={(e) => { clearRemote("schedule.periodEnd"); upd((d) => void (d.schedule.periodEnd = e.target.value)); }} />
              </Field>
              <label className="flex cursor-pointer items-center gap-2.5 text-[13px] sm:col-span-2">
                <Switch checked={sc.randomOrder} onCheckedChange={(v) => upd((d) => void (d.schedule.randomOrder = v))} />
                <span>
                  {t("settings.randomOrder")}
                  <span className="block text-xs text-muted-foreground">{t("settings.randomOrderHint")}</span>
                </span>
              </label>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2"><Globe className="size-4" aria-hidden /> {t("settings.network")}</CardTitle>
            </CardHeader>
            <CardContent>
              <Field id="n-proxy" label={t("settings.proxy")} hint={t("settings.proxyHint")} error={fieldErr("network.proxy")}>
                <Input {...fieldAria("n-proxy", fieldErr("network.proxy"), "x")} value={draft.network.proxy} placeholder="http://127.0.0.1:7890" spellCheck={false} onChange={(e) => { clearRemote("network.proxy"); upd((d) => void (d.network.proxy = e.target.value)); }} />
              </Field>
            </CardContent>
          </Card>

          <NotifyCard draft={draft} upd={upd} fieldErr={fieldErr} clearRemote={clearRemote} onWebhookChanged={(v) => { setSaved((s) => s && { ...s, notify: { ...s.notify, webhookConfigured: v } }); setDraft((d) => d && { ...d, notify: { ...d.notify, webhookConfigured: v } }); }} />
          </div>
        </section>

      </div>
    </>
  );
}

function NotifyCard({
  draft, upd, fieldErr, clearRemote, onWebhookChanged,
}: {
  draft: Settings; upd: (fn: (d: Settings) => void) => void; fieldErr: (f: string) => string | undefined;
  clearRemote: (f: string) => void; onWebhookChanged: (configured: boolean) => void;
}) {
  const { t } = useI18n();
  const toast = useToast();
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState<"set" | "test" | "clear" | null>(null);
  const [urlError, setUrlError] = useState<string | null>(null);
  const configured = draft.notify.webhookConfigured;

  const setWebhook = async () => {
    if (!isValidWebhook(url)) {
      setUrlError(t("settings.err.webhook"));
      return;
    }
    setUrlError(null);
    setBusy("set");
    try {
      await api.settings.setWebhook(url.trim());
      toast.success(t("settings.webhookSaved"));
      onWebhookChanged(true);
    } catch (e) {
      setUrlError(errMsg(e));
    } finally {
      setUrl(""); // never keep the address in the UI after submitting
      setBusy(null);
    }
  };
  const run = async (kind: "test" | "clear") => {
    setBusy(kind);
    try {
      if (kind === "test") {
        await api.settings.testWebhook();
        toast.success(t("settings.webhookTestOk"));
      } else {
        await api.settings.clearWebhook();
        toast.success(t("settings.webhookCleared"));
        onWebhookChanged(false);
      }
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><BellRing className="size-4" aria-hidden /> {t("settings.notify")}</CardTitle>
        <CardDescription>{t("settings.notifyDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <div className="grid gap-2">
          <div className="flex items-center gap-2">
            <label htmlFor="n-hook" className="text-[13px] font-medium">Webhook</label>
            {configured ? (
              <Badge tone="success"><CheckCircle2 aria-hidden /> {t("settings.webhookSet")}</Badge>
            ) : (
              <Badge tone="muted">{t("settings.webhookUnset")}</Badge>
            )}
          </div>
          <form className="flex gap-2" autoComplete="off" onSubmit={(e) => { e.preventDefault(); void setWebhook(); }}>
            <Input
              {...fieldAria("n-hook", urlError ?? undefined, "x")}
              type="password"
              autoComplete="off"
              value={url}
              placeholder={configured ? t("settings.webhookReplace") : "https://"}
              onChange={(e) => { setUrl(e.target.value); setUrlError(null); }}
              spellCheck={false}
            />
            <Button type="submit" disabled={!url || busy !== null}>{busy === "set" && <Spinner />} {t("settings.webhookSave")}</Button>
          </form>
          {urlError ? <p id="n-hook-err" role="alert" className="text-xs text-danger">{urlError}</p> : <p id="n-hook-hint" className="text-xs text-muted-foreground">{t("settings.webhookHint")}</p>}
          <div className="flex gap-2">
            <Button size="sm" variant="outline" onClick={() => void run("test")} disabled={!configured || busy !== null}>
              {busy === "test" ? <Spinner /> : <Send />} {t("settings.webhookTest")}
            </Button>
            <Button size="sm" variant="outline" onClick={() => void run("clear")} disabled={!configured || busy !== null}>
              {busy === "clear" ? <Spinner /> : <Trash2 />} {t("settings.webhookClear")}
            </Button>
          </div>
        </div>

        <Field id="n-stale" label={t("settings.staleAfter")} hint={t("settings.staleHint")} error={fieldErr("notify.staleAfter")} className="sm:max-w-60">
          <Input {...fieldAria("n-stale", fieldErr("notify.staleAfter"), "x")} value={draft.notify.staleAfter} onChange={(e) => { clearRemote("notify.staleAfter"); upd((d) => void (d.notify.staleAfter = e.target.value)); }} />
        </Field>

        <div>
          <p className="mb-1.5 text-[13px] font-medium" id="n-ev-l">{t("settings.events")}</p>
          <div role="group" aria-labelledby="n-ev-l" className="grid gap-1.5 sm:grid-cols-2">
            {NOTIFY_EVENTS.map((ev) => (
              <label key={ev} className="flex cursor-pointer items-center gap-2 text-[13px]">
                <Checkbox
                  checked={draft.notify.events.includes(ev)}
                  onCheckedChange={(v) => { clearRemote("notify.events"); upd((d) => void (d.notify.events = v ? NOTIFY_EVENTS.filter((x) => x === ev || d.notify.events.includes(x)) : d.notify.events.filter((x) => x !== ev))); }}
                />
                {t(`event.${ev}` as DictKey)}
              </label>
            ))}
          </div>
          <p className="mt-1.5 text-xs text-muted-foreground">{fieldErr("notify.events") ?? t("settings.eventsHint")}</p>
        </div>
      </CardContent>
    </Card>
  );
}
