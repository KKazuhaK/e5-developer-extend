import { CalendarClock, CheckCircle2, Clock, KeyRound, LogIn, Pause, Pencil, Play, RefreshCw, XCircle } from "lucide-react";
import type { AccountSummary } from "@/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { StatusBadge, Spinner } from "@/components/common";
import { useI18n } from "@/i18n";
import { daysUntil, formatRelative, formatWhen } from "@/lib/time";
import { displayStatus, needsAction } from "@/lib/status";
import { cn } from "@/lib/utils";

export interface AccountCardActions {
  busy: ReadonlySet<string>;
  onRun: (name: string) => void;
  onPause: (name: string) => void;
  onResume: (name: string) => void;
  onEdit: (name: string) => void;
  onLogin: (name: string) => void;
}

export function SecretExpiry({ date }: { date: string | null }) {
  const { t } = useI18n();
  const d = daysUntil(date);
  if (d === null || d > 30) return null;
  return (
    <Badge tone={d <= 7 ? "danger" : "warning"}>
      <KeyRound aria-hidden />
      {d < 0 ? t("account.secretExpired") : t("account.secretExpires", { n: d })}
    </Badge>
  );
}

export function AccountCard({ a, actions }: { a: AccountSummary; actions: AccountCardActions }) {
  const { t, lang } = useI18n();
  const busy = actions.busy.has(a.name);
  const canRun = a.status === "ok";
  const attention = needsAction(a.status);
  return (
    <Card className={cn("flex flex-col gap-3 p-4", a.status === "needs_attention" && "border-danger/50")}>
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="truncate text-[15px] font-semibold">{a.name}</h3>
          <p className="truncate text-xs text-muted-foreground" title={a.tenant}>
            {t(`auth.${a.auth}`)} · {a.tenant}
          </p>
        </div>
        <StatusBadge account={a} />
      </div>

      {(a.reason || a.fix) && a.status !== "ok" && (
        <div className="space-y-0.5 rounded-md bg-muted/60 px-3 py-2 text-[13px]">
          {a.reason && <p>{a.reason}</p>}
          {a.fix && (
            <p className="text-muted-foreground">
              {t("account.fixPrefix")}
              {a.fix}
            </p>
          )}
        </div>
      )}

      <dl className="grid grid-cols-2 gap-x-3 gap-y-1.5 text-[13px]">
        <div className="flex items-center gap-1.5">
          <Clock className="size-3.5 text-muted-foreground" aria-hidden />
          <dt className="sr-only">{t("account.lastSuccess")}</dt>
          <dd className="truncate" title={a.lastSuccess ?? ""}>
            <span className="text-muted-foreground">{t("account.lastSuccess")} </span>
            {formatRelative(a.lastSuccess, lang)}
          </dd>
        </div>
        <div className="flex items-center gap-1.5">
          <CalendarClock className="size-3.5 text-muted-foreground" aria-hidden />
          <dt className="sr-only">{t("account.nextRun")}</dt>
          <dd className="truncate">
            <span className="text-muted-foreground">{t("account.nextRun")} </span>
            {formatWhen(a.nextRun)}
          </dd>
        </div>
        <div className="flex items-center gap-3 tabular-nums">
          <dt className="sr-only">{t("account.today")}</dt>
          <dd className="flex items-center gap-3">
            <span className="text-muted-foreground">{t("account.today")}</span>
            <span className="flex items-center gap-1" title={t("account.todayOk")}>
              <CheckCircle2 className="size-3.5 text-success" aria-hidden />
              <span className="sr-only">{t("account.todayOk")}</span>
              {a.todayOk}
            </span>
            <span className={cn("flex items-center gap-1", a.todayFailed > 0 && "font-medium text-danger")} title={t("account.todayFailed")}>
              <XCircle className="size-3.5" aria-hidden />
              <span className="sr-only">{t("account.todayFailed")}</span>
              {a.todayFailed}
            </span>
          </dd>
        </div>
        <div className="tabular-nums">
          <dt className="sr-only">{t("account.apis")}</dt>
          <dd>
            <span className="text-muted-foreground">{t("account.apis")} </span>
            {displayStatus(a) === "waiting" ? "—" : `${a.usableApis}/${a.totalApis}`}
          </dd>
        </div>
      </dl>

      <div className="mt-auto flex min-h-8 flex-wrap items-center gap-1.5 pt-1">
        {attention ? (
          <Button size="sm" onClick={() => actions.onLogin(a.name)}>
            <LogIn /> {a.status === "not_signed_in" ? t("account.signIn") : t("account.reLogin")}
          </Button>
        ) : (
          <Button size="sm" variant="outline" disabled={!canRun || busy} onClick={() => actions.onRun(a.name)}>
            {busy ? <Spinner /> : <RefreshCw />} {t("account.runNow")}
          </Button>
        )}
        {a.paused ? (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => actions.onResume(a.name)}>
            <Play /> {t("account.resume")}
          </Button>
        ) : (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => actions.onPause(a.name)}>
            <Pause /> {t("account.pause")}
          </Button>
        )}
        <Button size="sm" variant="ghost" onClick={() => actions.onEdit(a.name)}>
          <Pencil /> {t("common.edit")}
        </Button>
        <span className="ml-auto">
          <SecretExpiry date={a.secretExpires} />
        </span>
      </div>
    </Card>
  );
}
