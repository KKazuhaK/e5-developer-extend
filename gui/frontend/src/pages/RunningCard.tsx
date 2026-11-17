import { useCallback, useEffect, useId, useState } from "react";
import { AlertTriangle, Play, Power } from "lucide-react";
import { api } from "@/api";
import type { ServiceInfo, ServiceResult } from "@/api";
import { useApp } from "@/state/app";
import { useI18n } from "@/i18n";
import { useToast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Switch } from "@/components/ui/switch";
import { ServiceResultPanel } from "@/components/ServiceResultPanel";
import { ErrorBlock, Spinner } from "@/components/common";
import { serviceState, type ServiceState } from "@/lib/service";
import { formatWhen } from "@/lib/time";
import { errMsg } from "@/lib/utils";

const TONE: Record<ServiceState, "success" | "warning" | "muted"> = { running: "success", stopped: "warning", notInstalled: "muted", unsupported: "muted" };

/**
 * Both switches act at once and neither goes through the page's Save button.
 * "Start at login" calls SettingsService.SetStartAtLogin; if that fails the
 * switch stays where it was and a toast says why. "Keep running in the
 * background" installs or removes the background service through
 * HealthService, the same calls the Health page uses.
 */
export function RunningCard({ startAtLogin, onStartAtLogin }: { startAtLogin: boolean; onStartAtLogin: (on: boolean) => void }) {
  const { t } = useI18n();
  const toast = useToast();
  const { snapshot } = useApp();
  const [svc, setSvc] = useState<ServiceInfo | null>(null);
  const [svcError, setSvcError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmOff, setConfirmOff] = useState(false);
  const [autoBusy, setAutoBusy] = useState(false);
  const [result, setResult] = useState<ServiceResult | null>(null);
  const hintId = useId();

  const load = useCallback(async () => {
    try {
      setSvc(await api.health.serviceInfo());
      setSvcError(null);
    } catch (e) {
      setSvcError(errMsg(e));
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  const changeAuto = async (on: boolean) => {
    setAutoBusy(true);
    try {
      await api.settings.setStartAtLogin(on);
      onStartAtLogin(on);
    } catch (e) {
      toast.error(errMsg(e)); // the switch is controlled: it keeps showing the old value
    } finally {
      setAutoBusy(false);
    }
  };

  // Install, start and uninstall share one guard: while one runs, nothing else starts.
  const change = async (kind: "install" | "start" | "uninstall") => {
    if (busy) return;
    setBusy(true);
    try {
      const r = kind === "install" ? await api.health.installService() : kind === "start" ? await api.health.startService() : await api.health.uninstallService();
      setResult(r);
      toast.success(t(kind === "install" ? "health.installed" : kind === "start" ? "settings.svcStarted" : "health.uninstalled"));
      await load();
      void snapshot.refresh();
    } catch (e) {
      toast.error(errMsg(e));
      await load();
    } finally {
      setBusy(false);
      setConfirmOff(false);
    }
  };

  const state = svc ? serviceState(svc) : null;
  const snap = snapshot.data;
  const nextRun = snap?.engine.running ? snap.accounts.map((a) => a.nextRun).filter((x): x is string => !!x).sort()[0] ?? null : null;
  const statusText: Record<ServiceState, string> = {
    running: t("settings.svcRunning"), stopped: t("settings.svcStopped"), notInstalled: t("settings.svcNotInstalled"), unsupported: t("settings.svcUnsupported"),
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><Power className="size-4" aria-hidden /> {t("settings.running")}</CardTitle>
        <CardDescription>{t("settings.runningDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <label className="flex cursor-pointer items-start gap-2.5 text-[13px]">
          <Switch className="mt-0.5" checked={startAtLogin} disabled={autoBusy} onCheckedChange={(v) => void changeAuto(v)} />
          <span>
            {t("settings.startAtLogin")}
            <span className="block text-xs text-muted-foreground">{t("settings.startAtLoginHint")}</span>
          </span>
        </label>

        <div className="grid gap-2 border-t pt-4">
          <label className="flex cursor-pointer items-start gap-2.5 text-[13px]">
            <Switch
              className="mt-0.5"
              aria-describedby={hintId}
              checked={state === "running" || state === "stopped"}
              disabled={!svc || state === "unsupported" || busy}
              onCheckedChange={(v) => { if (!busy) { if (v) void change("install"); else setConfirmOff(true); } }}
            />
            <span>
              <span className="flex items-center gap-2">
                {t("settings.svc")}
                {busy && <Spinner />}
              </span>
              <span id={hintId} className="block text-xs text-muted-foreground">{t("settings.svcHint")}</span>
            </span>
          </label>
          {svcError && <ErrorBlock message={`${t("settings.svcLoadFailed")}: ${svcError}`} onRetry={() => void load()} />}
          {state && state !== "unsupported" && (
            <div className="flex flex-wrap items-center gap-2 pl-[46px] text-xs text-muted-foreground" role="status">
              <Badge tone={TONE[state]}>{state === "stopped" && <AlertTriangle aria-hidden />}{statusText[state]}</Badge>
              {nextRun && state === "running" && <span>{t("settings.svcNext", { when: formatWhen(nextRun) })}</span>}
            </div>
          )}
          {state === "stopped" && (
            <div className="ml-[46px] flex flex-wrap items-center gap-3 rounded-md border border-warning/40 bg-warning-bg px-3 py-2 text-[13px]">
              <p className="min-w-0 flex-1">{t("settings.svcStoppedWarn")}</p>
              <Button size="sm" onClick={() => void change("start")} disabled={busy}>{busy ? <Spinner /> : <Play />} {t("settings.svcStart")}</Button>
            </div>
          )}
          {state === "unsupported" && <p className="pl-[46px] text-xs text-muted-foreground" role="status">{statusText.unsupported}</p>}
          {result && <ServiceResultPanel result={result} showActions={false} className="ml-[46px]" />}
        </div>
      </CardContent>

      <Dialog open={confirmOff} onOpenChange={setConfirmOff}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle>{t("health.uninstallTitle")}</DialogTitle>
            <DialogDescription>{t("health.uninstallBody")}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmOff(false)}>{t("common.cancel")}</Button>
            <Button variant="destructive" onClick={() => void change("uninstall")} disabled={busy}>{busy && <Spinner />} {t("health.uninstall")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
