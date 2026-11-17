import { AlertTriangle, CheckCircle2, Copy, ExternalLink, RefreshCw, XCircle } from "lucide-react";
import { api } from "@/api";
import type { UpdateInfo, UpdateResult } from "@/api";
import { useI18n } from "@/i18n";
import { useUpdate } from "@/state/update";
import { useToast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/common";
import { cn, copyText } from "@/lib/utils";
import { bannerVisible, errorKey, formatBytes, percent, reasonKey, stageKey, stepKey } from "@/lib/update";

/**
 * The upgrade, as one block: a new version with "Upgrade now" and "Later", the
 * progress while it runs, and what it did (or what the person has to do) at the
 * end. On the Overview it appears for a new version that was not put off; on the
 * About page (`always`) it appears whenever there is something to show.
 */
export function UpdateBanner({ always = false, className }: { always?: boolean; className?: string }) {
  const { t } = useI18n();
  const u = useUpdate();
  const { info, install } = u;

  if (install.phase === "running") {
    const p = install.progress;
    const pct = p?.stage === "download" ? percent(p.done, p.total) : null;
    return (
      <Shell tone="info" className={className} role="status">
        <Spinner className="mt-0.5 size-5 shrink-0 text-info" />
        <div className="min-w-0 flex-1">
          <p className="font-semibold">{t("update.running", { version: info?.latest ?? "" })}</p>
          <p className="mt-0.5 text-[13px] text-foreground/80">
            {t(stageKey(p?.stage))}
            {p?.stage === "download" && p.file ? ` ${p.file}` : ""}
            {p?.stage === "download" && p.total > 0 ? ` (${formatBytes(p.done)} / ${formatBytes(p.total)})` : ""}
          </p>
          <div
            className="mt-2 h-1.5 overflow-hidden rounded-full bg-muted"
            role="progressbar"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={pct ?? undefined}
            aria-label={t(stageKey(p?.stage))}
          >
            <div className={cn("h-full rounded-full bg-primary transition-[width]", pct === null && "w-1/3 animate-pulse")} style={pct === null ? undefined : { width: `${pct}%` }} />
          </div>
        </div>
      </Shell>
    );
  }

  if (install.phase === "done") return <Done result={install.result} onClose={u.reset} className={className} onRestart={() => void u.restart()} />;

  if (install.phase === "error") {
    const key = errorKey(install.code);
    return (
      <Shell tone="danger" className={className} role="alert">
        <XCircle className="mt-0.5 size-5 shrink-0 text-danger" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="font-semibold text-danger">{t("update.failed")}</p>
          <p className="mt-0.5 break-words text-[13px] text-foreground/80">{key ? t(key) : install.message}</p>
          {key && install.message && install.code !== "other" && <p className="mt-1 break-words text-xs text-muted-foreground">{install.message}</p>}
          <div className="mt-3 flex flex-wrap gap-2">
            {info?.mode === "auto" && (
              <Button size="sm" onClick={() => void u.start()}>
                <RefreshCw /> {t("update.retry")}
              </Button>
            )}
            <ReleaseButton info={info} />
            <Button size="sm" variant="ghost" onClick={u.reset}>
              {t("common.close")}
            </Button>
          </div>
        </div>
      </Shell>
    );
  }

  if (!info || info.dev || !info.available) return null;
  if (!always && !bannerVisible(info, u.dismissed(info.latest))) return null;
  const auto = info.mode === "auto";
  return (
    <Shell tone="info" className={className} role="region" label={t("update.available", { version: info.latest })}>
      <RefreshCw className="mt-0.5 size-5 shrink-0 text-info" aria-hidden />
      <div className="min-w-0 flex-1">
        <p className="font-semibold">{t("update.available", { version: info.latest })}</p>
        <p className="mt-0.5 text-[13px] text-foreground/80">{t("update.availableSub", { current: info.current })}</p>
        {!auto && <p className="mt-1 text-[13px] text-warning">{t(reasonKey(info.reason))}</p>}
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {auto ? (
            <Button size="sm" onClick={() => void u.start()}>
              {t("update.upgradeNow")}
            </Button>
          ) : (
            <ReleaseButton info={info} primary />
          )}
          <Button size="sm" variant="outline" onClick={() => void api.system.openExternal(info.url)}>
            <ExternalLink /> {t("update.notes")}
          </Button>
          {!always && (
            <Button size="sm" variant="ghost" onClick={() => u.dismiss(info.latest)}>
              {t("update.later")}
            </Button>
          )}
        </div>
      </div>
    </Shell>
  );
}

function ReleaseButton({ info, primary = false }: { info: UpdateInfo | null; primary?: boolean }) {
  const { t } = useI18n();
  if (!info) return null;
  return (
    <Button size="sm" variant={primary ? "default" : "outline"} onClick={() => void api.system.openExternal(info.url)}>
      <ExternalLink /> {t("update.openRelease")}
    </Button>
  );
}

function Done({ result, onClose, onRestart, className }: { result: UpdateResult; onClose: () => void; onRestart: () => void; className?: string }) {
  const { t } = useI18n();
  const toast = useToast();
  const needsYou = result.manual.length > 0;
  return (
    <Shell tone={needsYou ? "warning" : "success"} className={className} role="status">
      {needsYou ? <AlertTriangle className="mt-0.5 size-5 shrink-0 text-warning" aria-hidden /> : <CheckCircle2 className="mt-0.5 size-5 shrink-0 text-success" aria-hidden />}
      <div className="min-w-0 flex-1">
        <p className="font-semibold">{result.upToDate ? t("update.doneUpToDate") : needsYou ? t("update.manualTitle") : t("update.done", { version: result.version })}</p>
        {needsYou && (
          <div className="mt-2">
            <p className="text-[13px]">{t("update.manualHint")}</p>
            {result.manual.map((cmd) => (
              <div key={cmd} className="mt-1.5 flex items-start gap-2">
                <code className="min-w-0 flex-1 rounded bg-muted px-2 py-1.5 text-xs break-all">{cmd}</code>
                <Button size="sm" variant="outline" onClick={() => void copyText(cmd).then((ok) => ok && toast.success(t("update.copied")))}>
                  <Copy /> {t("update.copy")}
                </Button>
              </div>
            ))}
          </div>
        )}
        {!result.upToDate && result.steps.length > 0 && !needsYou && (
          <ul className="mt-1.5 list-disc space-y-0.5 pl-5 text-[13px] text-foreground/80">
            {result.steps.map((s, i) => {
              const k = stepKey(s.code);
              return <li key={`${s.code}-${i}`}>{k ? t(k, { arg: s.arg ?? "" }) : s.text}</li>;
            })}
            {result.service === "restarted" && <li>{t("update.service.restarted")}</li>}
            {result.service === "failed" && <li className="text-warning">{t("update.service.failed", { note: result.serviceNote })}</li>}
          </ul>
        )}
        {result.exitNow && <p className="mt-1.5 text-[13px]">{t("update.installerRunning")}</p>}
        {result.restartApp && !result.exitNow && !needsYou && <p className="mt-1.5 text-[13px]">{t("update.restartHint")}</p>}
        <div className="mt-3 flex flex-wrap gap-2">
          {result.restartApp && !result.exitNow && !needsYou && (
            <Button size="sm" onClick={onRestart}>
              <RefreshCw /> {t("update.restartNow")}
            </Button>
          )}
          {!result.exitNow && (
            <Button size="sm" variant="ghost" onClick={onClose}>
              {t("common.close")}
            </Button>
          )}
        </div>
      </div>
    </Shell>
  );
}

const TONES = {
  info: "border-info/40 bg-info-bg",
  success: "border-success/40 bg-success-bg",
  warning: "border-warning/50 bg-warning-bg",
  danger: "border-danger/50 bg-danger-bg",
} as const;

function Shell({
  tone, className, role, label, children,
}: { tone: keyof typeof TONES; className?: string; role: "status" | "alert" | "region"; label?: string; children: React.ReactNode }) {
  return (
    <div role={role} aria-label={label} className={cn("mb-4 flex items-start gap-3 rounded-lg border p-4", TONES[tone], className)}>
      {children}
    </div>
  );
}
