import { AlertTriangle, ExternalLink, Play, Plus, Users } from "lucide-react";
import { api } from "@/api";
import { DOCS_URL } from "@/config";
import { useApp } from "@/state/app";
import { useAccountActions } from "@/state/actions";
import { useI18n } from "@/i18n";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { AccountCard } from "@/components/AccountCard";
import { SubscriptionCard } from "@/pages/SubscriptionCard";
import { RenewalHelp } from "@/pages/RenewalHelp";
import { EmptyBlock, ErrorBlock, LoadingBlock, PageHeader, Spinner, ToneIcon } from "@/components/common";
import { engineState, engineTone, compareStatus, isHealthy, needsAction } from "@/lib/status";
import { formatRelative, formatWhen } from "@/lib/time";

export function Overview() {
  const { t, lang } = useI18n();
  const { snapshot, navigate } = useApp();
  const act = useAccountActions();
  const snap = snapshot.data;

  if (!snap) {
    return (
      <>
        <PageHeader title={t("nav.overview")} />
        {snapshot.error ? <ErrorBlock message={snapshot.error} onRetry={() => void snapshot.refresh()} /> : <LoadingBlock rows={4} />}
      </>
    );
  }

  const eng = engineState(snap.engine);
  const accounts = [...snap.accounts].sort((a, b) => compareStatus(a.status, b.status) || a.name.localeCompare(b.name));
  const attention = accounts.filter((a) => needsAction(a.status));
  const okCount = accounts.filter(isHealthy).length;
  const nextRun = accounts.map((a) => a.nextRun).filter((x): x is string => !!x).sort()[0] ?? null;
  const todayOk = accounts.reduce((n, a) => n + a.todayOk, 0);
  const todayFailed = accounts.reduce((n, a) => n + a.todayFailed, 0);
  const todayThrottled = accounts.reduce((n, a) => n + a.todayThrottled, 0);
  const pausedApis = accounts.reduce((n, a) => n + a.pausedApis, 0);
  const runningAll = act.busy.has("*");

  const addAccount = () => navigate("accounts", { wizard: { mode: "add" } });

  return (
    <>
      <PageHeader
        title={t("nav.overview")}
        actions={
          <>
            <RenewalHelp />
            {accounts.length > 0 && (
              <Button onClick={() => void act.runNow([])} disabled={runningAll || accounts.every((a) => a.status !== "ok")}>
                {runningAll ? <Spinner /> : <Play />} {t("overview.runAll")}
              </Button>
            )}
          </>
        }
      />

      {snapshot.error && <ErrorBlock className="mb-4" message={snapshot.error} onRetry={() => void snapshot.refresh()} />}

      {attention.length > 0 && (
        <div role="alert" className="mb-4 flex items-start gap-3 rounded-lg border border-danger/50 bg-danger-bg p-4">
          <AlertTriangle className="mt-0.5 size-5 shrink-0 text-danger" aria-hidden />
          <div className="min-w-0 flex-1">
            <p className="font-semibold text-danger">{t("overview.attention", { count: attention.length })}</p>
            <p className="mt-0.5 truncate text-[13px] text-foreground/80">
              {attention.map((a) => a.name).join("、")}
              {attention[0]?.reason ? ` — ${attention[0].reason}` : ""}
            </p>
          </div>
          <Button size="sm" onClick={() => navigate("accounts", { filter: "attention" })}>
            {t("overview.handle")}
          </Button>
        </div>
      )}

      {accounts.length > 0 && (
      <Card className="mb-5 grid grid-cols-2 gap-y-3 p-4 lg:grid-cols-4">
        <Stat label={t("overview.engine")}>
          <Badge tone={engineTone(eng)}>
            <ToneIcon name={eng === "stopped" ? "power" : "check"} />
            {t(`engine.${eng}`)}
          </Badge>
          <span className="mt-1 block text-xs text-muted-foreground tabular-nums">
            {snap.engine.heartbeatAgeSec !== null
              ? t("overview.heartbeat", { s: snap.engine.heartbeatAgeSec })
              : t("overview.noHeartbeat")}
          </span>
        </Stat>
        <Stat label={t("overview.nextRun")}>
          <span className="text-lg font-semibold tabular-nums">{eng === "stopped" ? "—" : formatWhen(nextRun)}</span>
          <span className="mt-0.5 block text-xs text-muted-foreground">{eng === "stopped" ? t("overview.engineStoppedShort") : formatRelative(nextRun, lang)}</span>
        </Stat>
        <Stat label={t("overview.todayCalls")}>
          <span className="text-lg font-semibold tabular-nums">
            {todayOk}
            <span className="ml-1 text-sm font-normal text-muted-foreground">{t("overview.ok")}</span>
          </span>
          <span className="mt-0.5 block text-xs tabular-nums text-muted-foreground">
            {t("overview.failedN", { n: todayFailed })}
            {todayThrottled > 0 && <> · <span title={t("account.throttledHint")}>{t("overview.throttledN", { n: todayThrottled })}</span></>}
            {pausedApis > 0 && <> · <span title={t("account.pausedApisHint")}>{t("account.pausedApis", { n: pausedApis })}</span></>}
          </span>
        </Stat>
        <Stat label={t("overview.healthy")}>
          <span className="text-lg font-semibold tabular-nums">
            {okCount}
            <span className="font-normal text-muted-foreground"> / {accounts.length}</span>
          </span>
          <span className="mt-0.5 block text-xs text-muted-foreground">{t("overview.accountsOk", { count: okCount })}</span>
        </Stat>
      </Card>
      )}

      {accounts.length > 0 && (
        <SubscriptionCard accounts={accounts} />
      )}

      {eng === "stopped" && (
        <div className="mb-5 flex items-start gap-3 rounded-lg border border-warning/40 bg-warning-bg p-3 text-[13px]">
          <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden />
          <p className="flex-1">{t("overview.engineStopped")}</p>
          <Button size="sm" variant="outline" onClick={() => navigate("health")}>
            {t("overview.toHealth")}
          </Button>
        </div>
      )}

      {accounts.length === 0 ? (
        <EmptyBlock icon={<Users />} title={t("overview.emptyTitle")} hint={t("overview.emptyHint")}>
          <div className="flex flex-wrap justify-center gap-2">
            <Button onClick={addAccount}>
              <Plus /> {t("accounts.add")}
            </Button>
            <Button variant="outline" onClick={() => void api.system.openExternal(DOCS_URL)}>
              <ExternalLink /> {t("overview.appDocs")}
            </Button>
          </div>
        </EmptyBlock>
      ) : (
        <section aria-labelledby="accounts-title">
        <h2 id="accounts-title" className="mb-3 text-[15px] font-semibold">{t("overview.accountsTitle", { count: accounts.length })}</h2>
        <div className="grid grid-cols-[repeat(auto-fill,minmax(320px,1fr))] gap-4">
          {accounts.map((a) => (
            <AccountCard
              key={a.name}
              a={a}
              actions={{
                busy: act.busy,
                onRun: (n) => void act.runNow([n]),
                onPause: (n) => void act.pause(n),
                onResume: (n) => void act.resume(n),
                onEdit: (n) => navigate("accounts", { wizard: { mode: "edit", name: n } }),
                onLogin: (n) => navigate("accounts", { signIn: n }),
                onSwitchToApp: (n) => navigate("accounts", { wizard: { mode: "add", from: n } }),
              }}
            />
          ))}
        </div>
        </section>
      )}
    </>
  );
}

function Stat({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="min-h-[4.25rem] px-2">
      <p className="mb-1 text-xs text-muted-foreground">{label}</p>
      {children}
    </div>
  );
}
