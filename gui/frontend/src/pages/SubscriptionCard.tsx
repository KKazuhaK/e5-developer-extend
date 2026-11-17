import { useCallback, useEffect, useState } from "react";
import { AlertTriangle, CalendarClock, ChevronDown, ChevronRight, Info, RefreshCw, ShieldCheck } from "lucide-react";
import { api } from "@/api";
import type { AccountSummary, SubscriptionView, TenantSubscriptions } from "@/api";
import { AppSetupStep, type AdminMethod } from "@/pages/AppSetupFlow";
import { DOCS_URL } from "@/config";
import { useI18n } from "@/i18n";
import { useToast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Field, Spinner, fieldAria } from "@/components/common";
import { dateOwner, daysTone, statusBadge, validDate, watched } from "@/lib/subscription";
import { formatRelative } from "@/lib/time";
import { errMsg } from "@/lib/utils";

/** How many days an end date is away, in words: "还剩 89 天", "今天到期", "已过期 3 天". */
function DaysBadge({ days }: { days: number | null }) {
  const { t } = useI18n();
  if (days === null) return null;
  const text = days > 0 ? t("sub.daysLeft", { count: days }) : days === 0 ? t("sub.dueToday") : t("sub.overdue", { count: -days });
  return <Badge tone={daysTone(days)}>{text}</Badge>;
}

function SubscriptionLine({ s, tv }: { s: SubscriptionView; tv: TenantSubscriptions }) {
  const { t, lang } = useI18n();
  const [open, setOpen] = useState(false);
  const st = statusBadge(s.status);
  const Chevron = open ? ChevronDown : ChevronRight;
  return (
    <li className="py-2">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="font-mono text-[13px] font-medium">{s.sku}</span>
        <span aria-hidden className="text-muted-foreground">·</span>
        <Badge tone={st.tone}>{st.label ? t(st.label) : s.status}</Badge>
        {s.expires && (
          <>
            <span aria-hidden className="text-muted-foreground">·</span>
            <span className="text-[13px] tabular-nums">{t("sub.endsOn", { date: s.expires })}</span>
          </>
        )}
        <DaysBadge days={s.daysLeft} />
        {s.isTrial && <Badge tone="info">{t("sub.trial")}</Badge>}
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-x-3 text-xs text-muted-foreground">
        <span>{t("sub.licenses", { count: s.licenses })}</span>
        <span>{t("sub.checked", { when: formatRelative(s.checkedAt, lang) })}</span>
        <button
          type="button"
          className="inline-flex cursor-pointer items-center gap-0.5 rounded text-foreground/80 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
          aria-expanded={open}
          aria-controls={`renewals-${tv.tenant}-${s.id}`}
          onClick={() => setOpen(!open)}
        >
          <Chevron className="size-3.5" aria-hidden />
          {t("sub.history", { count: s.renewals.length })}
        </button>
      </div>
      {open && (
        <ul id={`renewals-${tv.tenant}-${s.id}`} className="mt-1.5 space-y-0.5 border-l-2 pl-3 text-xs tabular-nums">
          {s.renewals.length === 0 && <li className="text-muted-foreground">{t("sub.historyEmpty")}</li>}
          {[...s.renewals].reverse().map((r) => (
            <li key={r.seenAt}>{t("sub.renewal", { from: r.from, to: r.to, when: formatRelative(r.seenAt, lang) })}</li>
          ))}
        </ul>
      )}
    </li>
  );
}

/** The hand-written date: a field when the subscription cannot be read, a note when it is no longer needed. */
function ManualDate({ tv, onSaved }: { tv: TenantSubscriptions; onSaved: () => void }) {
  const { t } = useI18n();
  const toast = useToast();
  const owner = dateOwner(tv);
  const [date, setDate] = useState(tv.manualExpires);
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  const id = `sub-manual-${tv.tenant}`;
  const save = async (value: string) => {
    if (value && !validDate(value)) return setError(t("sub.badDate"));
    setError(undefined);
    setBusy(true);
    try {
      await api.subscription.setExpires(owner, value);
      if (!value) setDate("");
      toast.success(t(value ? "sub.manualSaved" : "sub.manualCleared"));
      onSaved();
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  if (tv.manualRedundant) {
    return (
      <div className="mt-2 flex flex-wrap items-center gap-2 rounded-md bg-muted/60 px-3 py-2 text-[13px]">
        <Info className="size-4 shrink-0 text-info" aria-hidden />
        <span className="min-w-0 flex-1">{t("sub.manualRedundant", { date: tv.manualExpires })}</span>
        <Button size="sm" variant="outline" disabled={busy} onClick={() => void save("")}>{t("sub.manualClear")}</Button>
      </div>
    );
  }
  if (tv.access === "ok") return null; // real data is shown; nothing to enter
  return (
    <div className="mt-2 rounded-md border border-dashed p-3">
      {tv.manualInUse && (
        <p className="mb-2 flex flex-wrap items-center gap-2 text-[13px]">
          <CalendarClock className="size-4 shrink-0 text-muted-foreground" aria-hidden />
          <span className="tabular-nums">{t("sub.manualUsed", { date: tv.manualExpires })}</span>
          <DaysBadge days={tv.manualDaysLeft} />
        </p>
      )}
      <Field id={id} label={t("sub.manualLabel")} hint={t("sub.manualHint")} error={error}>
        <div className="flex flex-wrap gap-2">
          <Input {...fieldAria(id, error, "x")} type="date" className="w-44" value={date} onChange={(e) => setDate(e.target.value)} disabled={busy} />
          <Button size="sm" disabled={busy || date === tv.manualExpires} onClick={() => void save(date)}>
            {busy ? <Spinner /> : null} {t("common.save")}
          </Button>
          {tv.manualExpires && (
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => void save("")}>{t("sub.manualClear")}</Button>
          )}
        </div>
      </Field>
    </div>
  );
}

function Tenant({ tv, onFix, onChanged }: { tv: TenantSubscriptions; onFix: (account: string) => void; onChanged: () => void }) {
  const { t, lang } = useI18n();
  const subs = watched(tv);
  const others = tv.subscriptions.length - subs.length;
  return (
    <section className="py-3 first:pt-0 last:pb-0" aria-label={tv.label}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        <h3 className="min-w-0 truncate text-[14px] font-semibold" title={tv.tenantId || tv.label}>{tv.label}</h3>
        <span className="text-xs text-muted-foreground">
          {tv.checkedAt ? t("sub.checked", { when: formatRelative(tv.checkedAt, lang) }) : t("sub.neverChecked")}
          {tv.accounts.length > 0 && <> · {t("sub.accounts", { names: tv.accounts.join("、") })}</>}
        </span>
      </div>

      {tv.access === "no_permission" && (
        <div role="status" className="mt-2 flex flex-wrap items-start gap-3 rounded-md border border-warning/40 bg-warning-bg p-3 text-[13px]">
          <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden />
          <div className="min-w-0 flex-1">
            <p className="font-medium">{t("sub.noPermission")}</p>
            <p className="mt-0.5 text-muted-foreground">{t("sub.noPermissionHow")}</p>
            <p className="mt-0.5 text-xs text-muted-foreground">{t("sub.fixHint", { account: tv.fixAccount })}</p>
            {tv.fixCommand && <p className="mt-0.5 text-xs text-muted-foreground">{t("sub.fixCli")} <code className="rounded bg-muted px-1 py-0.5 font-mono select-all">{tv.fixCommand}</code></p>}
          </div>
          {tv.fixAccount && (
            <Button size="sm" onClick={() => onFix(tv.fixAccount)}>
              <ShieldCheck /> {t("sub.fix")}
            </Button>
          )}
        </div>
      )}
      {tv.access === "error" && (
        <p role="status" className="mt-2 text-[13px] text-warning">{t("sub.error", { detail: tv.detail })}</p>
      )}
      {tv.access === "unchecked" && <p className="mt-2 text-[13px] text-muted-foreground">{t("sub.unchecked")}</p>}

      {subs.length > 0 && (
        <ul className="mt-1 divide-y">
          {subs.map((s) => <SubscriptionLine key={s.id} s={s} tv={tv} />)}
        </ul>
      )}
      {(tv.access === "ok" || (tv.access === "error" && tv.subscriptions.length > 0)) && subs.length === 0 && (
        <p className="mt-2 text-[13px] text-muted-foreground">{t("sub.noWatched")}</p>
      )}
      {others > 0 && <p className="mt-1 text-xs text-muted-foreground">{t("sub.others", { count: others })}</p>}

      <ManualDate key={`${tv.tenant}:${tv.manualExpires}:${tv.access}`} tv={tv} onSaved={onChanged} />
    </section>
  );
}

/**
 * Repairs the app registration of an account (only the missing permissions are added; no account,
 * no client secret, no other app), then asks the subscription to be read again.
 */
function RepairPanel({ account, onClose, onRepaired }: { account: string; onClose: () => void; onRepaired: () => void }) {
  const { t } = useI18n();
  const [method, setMethod] = useState<AdminMethod | null>(null);
  const [restart, setRestart] = useState(0);
  const [finished, setFinished] = useState(false);
  return (
    <div className="space-y-3">
      <div>
        <h3 className="text-[14px] font-semibold">{t("sub.repair.title", { account })}</h3>
        <p className="mt-0.5 text-xs text-muted-foreground">{t("sub.repair.desc", { account })}</p>
      </div>
      {method === null ? (
        <div className="space-y-2">
          <p className="text-[13px] font-medium">{t("auto.method")}</p>
          <div className="flex gap-2" role="radiogroup" aria-label={t("auto.method")}>
            {(["device_code", "browser"] as const).map((m) => (
              <Button key={m} type="button" role="radio" aria-checked={false} size="sm" variant="outline" onClick={() => setMethod(m)}>
                {t(`auto.method.${m}`)}
              </Button>
            ))}
          </div>
          <p className="text-xs text-muted-foreground">{t("auto.methodHint")}</p>
          <Button variant="ghost" size="sm" onClick={onClose}>{t("common.cancel")}</Button>
        </div>
      ) : (
        <AppSetupStep
          key={restart}
          method={method}
          done={null}
          repair={account}
          onResult={(_r, sid) => { void api.appSetup.finish(sid).catch(() => undefined); setFinished(true); onRepaired(); }}
          onManual={() => void api.system.openExternal(DOCS_URL)}
          onRestart={() => { setMethod(null); setRestart((n) => n + 1); }}
          onAbort={onClose}
        />
      )}
      {finished && <Button size="sm" onClick={onClose}>{t("sub.repair.close")}</Button>}
    </div>
  );
}

/** The "E5 subscription" card of the overview: one block per tenant. */
export function SubscriptionCard({ accounts }: { accounts: AccountSummary[] }) {
  const { t } = useI18n();
  const toast = useToast();
  const [tenants, setTenants] = useState<TenantSubscriptions[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [repair, setRepair] = useState<string | null>(null);
  // The tenants follow the accounts: a new or removed account changes what is listed.
  const accountKey = accounts.map((a) => `${a.name}@${a.tenant}`).join("|");

  const load = useCallback(async () => {
    try {
      setTenants(await api.subscription.list());
      setError(null);
    } catch (e) {
      setError(errMsg(e));
    }
  }, []);
  useEffect(() => { void load(); }, [load, accountKey]);

  const refresh = async () => {
    setRefreshing(true);
    try {
      const r = await api.subscription.refresh("");
      setTenants(r.tenants);
      setError(null);
      if (r.queued) toast.error(t("sub.queued"));
      else toast.success(t("sub.refreshed"));
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setRefreshing(false);
    }
  };

  return (
    <Card className="mb-5 p-4" aria-labelledby="sub-title">
      <div className="mb-3 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 id="sub-title" className="text-[15px] font-semibold">{t("sub.title")}</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">{t("sub.desc", { skus: (tenants?.[0]?.watchedSkus ?? ["DEVELOPERPACK"]).join("、") })}</p>
        </div>
        <Button size="sm" variant="outline" onClick={() => void refresh()} disabled={refreshing || !tenants?.length}>
          {refreshing ? <Spinner /> : <RefreshCw />} {t("sub.refresh")}
        </Button>
      </div>
      {error && <p role="alert" className="text-[13px] text-danger">{error}</p>}
      {!tenants && !error && <p className="text-[13px] text-muted-foreground">{t("common.loading")}</p>}
      {repair ? (
        <RepairPanel
          account={repair}
          onClose={() => setRepair(null)}
          onRepaired={() => { void api.subscription.refresh(repair).then((r) => setTenants(r.tenants)).catch((e) => setError(errMsg(e))); }}
        />
      ) : tenants && (
        <div className="divide-y">
          {tenants.map((tv) => <Tenant key={tv.tenant} tv={tv} onFix={setRepair} onChanged={() => void load()} />)}
        </div>
      )}
    </Card>
  );
}
