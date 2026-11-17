import { useCallback, useEffect, useMemo, useState } from "react";
import { BellRing, ChevronDown, Globe, Palette, Save, SlidersHorizontal, Timer } from "lucide-react";
import { api } from "@/api";
import type { Settings, ValidationError } from "@/api";
import { useApp } from "@/state/app";
import { useI18n, type DictKey } from "@/i18n";
import { useToast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { ErrorBlock, Field, LoadingBlock, PageHeader, Spinner, fieldAria } from "@/components/common";
import { WEEKDAYS, errorsByField, validateSettings, type LocalCode } from "@/lib/settings";
import { cn, errMsg } from "@/lib/utils";
import { AlertsCard } from "./AlertsCard";
import { BackupCard } from "./BackupCard";
import { PaceCard } from "./PaceCard";
import { RunningCard } from "./RunningCard";

/** Form field (as the validators name it) to the id of its control, in page order. Every one lives in Advanced settings. */
const ADVANCED_FIELDS: [string, string][] = [
  ["schedule.intervalMin", "s-imin"], ["schedule.intervalMax", "s-imax"], ["schedule.apisPerRoundMin", "s-amin"],
  ["schedule.apisPerRoundMax", "s-amax"], ["schedule.workDays", "s-days-l"], ["schedule.workStart", "s-ws"],
  ["schedule.workEnd", "s-we"], ["schedule.periodStart", "s-ps"], ["schedule.periodEnd", "s-pe"],
  ["network.proxy", "n-proxy"], ["notify.staleAfter", "n-stale"], ["notify.cooldown", "n-cooldown"], ["notify.quietHours", "n-quiet"], ["notify.dailySummaryAt", "n-daily"],
  ["notify.spikeRounds", "n-spike-rounds"], ["notify.spikeRate", "n-spike-rate"], ["notify.spikeMinFailures", "n-spike-min"],
  ["notify.events", "n-ev-l"],
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
  cooldown: "settings.err.cooldown", quiet: "settings.err.quiet", spikeRounds: "settings.err.spikeRounds", spikeRate: "settings.err.spikeRate",
  spikeMin: "settings.err.spikeMin",
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
  // Save asks for the first bad field to be shown; the effect below opens Advanced
  // settings, then scrolls to it and focuses it once it is visible.
  const [reveal, setReveal] = useState<{ id: string; n: number } | null>(null);
  const revealFirstError = (keys: Set<string>) => {
    const hit = ADVANCED_FIELDS.find(([k]) => keys.has(k));
    if (!hit) return;
    setAdvOpen(true); // not remembered: it is only there to show the error
    setReveal((r) => ({ id: hit[1], n: (r?.n ?? 0) + 1 }));
  };
  useEffect(() => {
    if (!reveal || !advOpen) return;
    const el = document.getElementById(reveal.id);
    el?.scrollIntoView({ block: "center", behavior: "smooth" });
    if (el && el.tagName !== "P") el.focus({ preventScroll: true });
    setReveal(null);
  }, [reveal, advOpen]);

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

  // Channels are saved on their own; only their count (read only) is mirrored into the settings.
  const onChannelsChanged = useCallback((count: number, webhook: boolean) => {
    const set = (x: Settings | null) => x && x.notify.channels === count && x.notify.webhookConfigured === webhook ? x : x && { ...x, notify: { ...x.notify, channels: count, webhookConfigured: webhook } };
    setSaved(set);
    setDraft(set);
  }, []);
  const goToAlerts = () => {
    toggleAdvanced(true);
    window.setTimeout(() => document.getElementById("alerts-card")?.scrollIntoView({ block: "start", behavior: "smooth" }), 60);
  };

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
        {saved.notify.channels === 0 && (
          <div role="status" className="flex flex-wrap items-center gap-3 rounded-lg border border-warning/30 bg-warning-bg px-4 py-3 text-[13px]">
            <BellRing className="size-4 shrink-0 text-warning" aria-hidden />
            <p className="min-w-0 flex-1">{t("settings.alertsNudge")}</p>
            <Button size="sm" variant="outline" onClick={goToAlerts}>{t("settings.alertsNudgeGo")}</Button>
          </div>
        )}
        <RunningCard
          startAtLogin={draft.ui.startAtLogin}
          // Applied by the backend already: record it as saved too, so it never makes the page dirty.
          onStartAtLogin={(on) => { setSaved((x) => x && { ...x, ui: { ...x.ui, startAtLogin: on } }); setDraft((d) => d && { ...d, ui: { ...d.ui, startAtLogin: on } }); }}
        />

        <PaceCard draft={draft} upd={upd} clearRemote={clearRemote} />

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
              </span>
              <ChevronDown className={cn("size-4 shrink-0 transition-transform", advOpen && "rotate-180")} aria-hidden />
            </button>
          </h2>
          <p aria-live="polite" className="px-4 pb-3 text-xs text-danger empty:hidden empty:p-0">
            {!advOpen && ADVANCED_FIELDS.some(([k]) => fieldErr(k)) ? t("settings.advancedHasError") : null}
          </p>
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

          <AlertsCard draft={draft} upd={upd} fieldErr={fieldErr} clearRemote={clearRemote} onChannelsChanged={onChannelsChanged} />
          </div>
        </section>

      </div>
    </>
  );
}
