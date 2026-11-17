import { useCallback, useEffect, useState } from "react";
import { HeartPulse, Info, RefreshCw, Server, Trash2, Download } from "lucide-react";
import { api } from "@/api";
import type { DoctorCheck, ServiceInfo, ServiceResult } from "@/api";
import { doctorMessage, doctorTitle } from "@/lib/doctor";
import { useI18n } from "@/i18n";
import { useToast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ServiceResultPanel } from "@/components/ServiceResultPanel";
import { ErrorBlock, LevelBadge, LoadingBlock, PageHeader, Spinner } from "@/components/common";
import { cn, errMsg } from "@/lib/utils";

export function Health() {
  const { t, lang } = useI18n();
  const toast = useToast();
  const [offline, setOffline] = useState(false);
  const [checks, setChecks] = useState<DoctorCheck[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const [svc, setSvc] = useState<ServiceInfo | null>(null);
  const [svcError, setSvcError] = useState<string | null>(null);
  const [svcBusy, setSvcBusy] = useState<"install" | "uninstall" | null>(null);
  const [confirmUninstall, setConfirmUninstall] = useState(false);
  const [result, setResult] = useState<ServiceResult | null>(null);

  const run = useCallback(async () => {
    setRunning(true);
    try {
      setChecks(await api.health.doctor(offline));
      setError(null);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setRunning(false);
    }
  }, [offline]);

  const loadSvc = useCallback(async () => {
    try {
      setSvc(await api.health.serviceInfo());
      setSvcError(null);
    } catch (e) {
      setSvcError(errMsg(e));
    }
  }, []);

  useEffect(() => {
    void run();
  }, [run]);
  useEffect(() => {
    void loadSvc();
  }, [loadSvc]);

  const doService = async (kind: "install" | "uninstall") => {
    setSvcBusy(kind);
    try {
      const r = kind === "install" ? await api.health.installService() : await api.health.uninstallService();
      setResult(r);
      toast.success(t(kind === "install" ? "health.installed" : "health.uninstalled"));
      await loadSvc();
      void run();
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setSvcBusy(null);
      setConfirmUninstall(false);
    }
  };

  const checkTitle = (c: DoctorCheck) => {
    const spec = doctorTitle(c.id);
    return spec ? t(spec.key, spec.params) : c.title; // unknown ids keep the backend title
  };
  const count = (l: DoctorCheck["level"]) => checks?.filter((c) => c.level === l).length ?? 0;

  return (
    <>
      <PageHeader
        title={t("nav.health")}
        description={t("health.desc")}
        actions={
          <Button onClick={() => void run()} disabled={running}>
            {running ? <Spinner /> : <RefreshCw />} {t("health.recheck")}
          </Button>
        }
      />

      <div className="mb-3 flex flex-wrap items-center gap-4">
        <label className="flex cursor-pointer items-center gap-2 text-[13px]">
          <Checkbox checked={offline} onCheckedChange={(v) => setOffline(v === true)} />
          {t("health.offline")}
        </label>
        {checks && (
          <p className="ml-auto flex gap-3 text-xs tabular-nums text-muted-foreground" aria-live="polite">
            <span>{t("level.ok")} {count("ok")}</span>
            <span>{t("level.warn")} {count("warn")}</span>
            <span>{t("level.fail")} {count("fail")}</span>
          </p>
        )}
      </div>

      {error && <ErrorBlock className="mb-3" message={error} onRetry={() => void run()} />}
      {!checks ? (
        error ? null : <LoadingBlock rows={5} />
      ) : (
        <Card className={cn("mb-6 transition-opacity", running && "opacity-60")} aria-busy={running}>
          <ul className="divide-y">
            {checks.map((c) => (
              <li key={c.id} className="flex items-start gap-3 px-4 py-3">
                <div className="w-20 shrink-0 pt-0.5"><LevelBadge level={c.level} /></div>
                <div className="min-w-0 flex-1">
                  <p className="font-medium">{checkTitle(c)}</p>
                  {c.detail && <p className="text-[13px] text-muted-foreground">{doctorMessage(lang, c.parts, c.detail)}</p>}
                  {c.fix && c.level !== "ok" && (
                    <p className="mt-0.5 text-[13px]">
                      <span className="font-medium">{t("health.fix")}</span>
                      {doctorMessage(lang, c.fixParts, c.fix)}
                    </p>
                  )}
                </div>
              </li>
            ))}
          </ul>
        </Card>
      )}
      {checks && checks.length === 0 && <p className="text-[13px] text-muted-foreground"><HeartPulse className="mr-1 inline size-4" />{t("health.noChecks")}</p>}

      <Card>
        <CardHeader>
          <div className="flex items-center justify-between gap-2">
            <CardTitle className="flex items-center gap-2"><Server className="size-4" aria-hidden /> {t("health.service")}</CardTitle>
            {svc && (
              <Badge tone={svc.installed ? "success" : "muted"}>
                {svc.installed ? t("health.svcInstalled") : t("health.svcNotInstalled")}
              </Badge>
            )}
          </div>
          <CardDescription>{t("health.serviceDesc")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {svcError && <ErrorBlock message={svcError} onRetry={() => void loadSvc()} />}
          {!svc && !svcError && <LoadingBlock rows={1} />}
          {svc && (
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-[13px]">
              <dt className="text-muted-foreground">{t("health.platform")}</dt>
              <dd>{svc.platform}</dd>
              {svc.installed && (
                <>
                  <dt className="text-muted-foreground">{t("health.enabled")}</dt>
                  <dd>{svc.enabled}</dd>
                  <dt className="text-muted-foreground">{t("health.running")}</dt>
                  <dd>{svc.running}</dd>
                </>
              )}
              {svc.details.length > 0 && (
                <>
                  <dt className="text-muted-foreground">{t("health.details")}</dt>
                  <dd className="min-w-0 space-y-0.5 break-all">{svc.details.map((d) => <p key={d}>{d}</p>)}</dd>
                </>
              )}
            </dl>
          )}
          <div className="flex items-start gap-2 rounded-md bg-info-bg px-3 py-2 text-[13px]">
            <Info className="mt-0.5 size-4 shrink-0 text-info" aria-hidden />
            <p>{t("health.serviceVsTray")}</p>
          </div>
          {result && <ServiceResultPanel result={result} />}
          <div className="flex gap-2">
            <Button onClick={() => void doService("install")} disabled={!svc || svc.installed || svcBusy !== null}>
              {svcBusy === "install" ? <Spinner /> : <Download />} {t("health.install")}
            </Button>
            <Button variant="outline" onClick={() => setConfirmUninstall(true)} disabled={!svc || !svc.installed || svcBusy !== null}>
              {svcBusy === "uninstall" ? <Spinner /> : <Trash2 />} {t("health.uninstall")}
            </Button>
          </div>
        </CardContent>
      </Card>

      <Dialog open={confirmUninstall} onOpenChange={setConfirmUninstall}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle>{t("health.uninstallTitle")}</DialogTitle>
            <DialogDescription>{t("health.uninstallBody")}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmUninstall(false)}>{t("common.cancel")}</Button>
            <Button variant="destructive" onClick={() => void doService("uninstall")} disabled={svcBusy !== null}>{t("health.uninstall")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
