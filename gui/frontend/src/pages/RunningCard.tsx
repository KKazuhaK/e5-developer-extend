import { useCallback, useEffect, useId, useState } from "react";
import { AlertTriangle, Power } from "lucide-react";
import { api } from "@/api";
import type { ServiceInfo, ServiceResult, Settings } from "@/api";
import { useApp } from "@/state/app";
import { useI18n } from "@/i18n";
import { useToast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Switch } from "@/components/ui/switch";
import { ErrorBlock, Spinner } from "@/components/common";
import { serviceState, type ServiceState } from "@/lib/service";
import { formatWhen } from "@/lib/time";
import { errMsg } from "@/lib/utils";

const TONE: Record<ServiceState, "success" | "warning" | "muted"> = { running: "success", stopped: "warning", notInstalled: "muted", unsupported: "muted" };

/**
 * "Start at login" edits the settings draft (saved with the page's Save
 * button). "Keep running in the background" acts at once: it installs or
 * removes the background service through HealthService, the same calls the
 * Health page uses.
 */
export function RunningCard({ draft, upd }: { draft: Settings; upd: (fn: (d: Settings) => void) => void }) {
  const { t } = useI18n();
  const toast = useToast();
  const { snapshot } = useApp();
  const [svc, setSvc] = useState<ServiceInfo | null>(null);
  const [svcError, setSvcError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmOff, setConfirmOff] = useState(false);
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

  const change = async (on: boolean) => {
    setBusy(true);
    try {
      const r = on ? await api.health.installService() : await api.health.uninstallService();
      setResult(on ? r : null);
      toast.success(t(on ? "health.installed" : "health.uninstalled"));
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setBusy(false);
      setConfirmOff(false);
    }
    await load();
    void snapshot.refresh();
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
          <Switch className="mt-0.5" checked={draft.ui.startAtLogin} onCheckedChange={(v) => upd((d) => void (d.ui.startAtLogin = v))} />
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
              onCheckedChange={(v) => (v ? void change(true) : setConfirmOff(true))}
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
            <p className="flex flex-wrap items-center gap-2 pl-[46px] text-xs text-muted-foreground" role="status">
              <Badge tone={TONE[state]}>{statusText[state]}</Badge>
              {nextRun && <span>{t("settings.svcNext", { when: formatWhen(nextRun) })}</span>}
            </p>
          )}
          {state === "unsupported" && <p className="pl-[46px] text-xs text-muted-foreground" role="status">{statusText.unsupported}</p>}
          {result && (result.manual.length > 0 || result.warnings.length > 0) && (
            <div className="ml-[46px] space-y-2 text-[13px]" role="status">
              {result.manual.length > 0 && (
                <div className="rounded-md border-2 border-warning/60 bg-warning-bg px-3 py-2.5">
                  <p className="mb-1 flex items-center gap-1.5 font-semibold text-warning"><AlertTriangle className="size-4" aria-hidden /> {t("health.manual")}</p>
                  <ul className="list-disc space-y-1 pl-5 break-words">{result.manual.map((m) => <li key={m}>{m}</li>)}</ul>
                </div>
              )}
              {result.warnings.length > 0 && (
                <div className="rounded-md bg-muted px-3 py-2">
                  <p className="mb-1 font-medium">{t("health.warnings")}</p>
                  <ul className="list-disc space-y-0.5 pl-5 break-words">{result.warnings.map((m) => <li key={m}>{m}</li>)}</ul>
                </div>
              )}
            </div>
          )}
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
            <Button variant="destructive" onClick={() => void change(false)} disabled={busy}>{busy && <Spinner />} {t("health.uninstall")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
