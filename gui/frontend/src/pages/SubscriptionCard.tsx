import { useCallback, useEffect, useState } from "react";
import { AlertTriangle, CalendarClock, Info, RefreshCw, ShieldCheck } from "lucide-react";
import { api } from "@/api";
import type { AccountSummary, SubscriptionView, TenantSubscriptions } from "@/api";
import { AppSetupStep, type AdminMethod } from "@/pages/AppSetupFlow";
import { DOCS_URL } from "@/config";
import { useI18n } from "@/i18n";
import { useToast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Field, Spinner, fieldAria } from "@/components/common";
import { dateOwner, daysTone, statusBadge, validDate, watched } from "@/lib/subscription";
import { formatRelative } from "@/lib/time";
import { cn, errMsg } from "@/lib/utils";

/** How many days an end date is away, in words: "还剩 89 天", "今天到期", "已过期 3 天". */
function DaysBadge({ days }: { days: number | null }) {
  const { t } = useI18n();
  if (days === null) return null;
  const text = days > 0 ? t("sub.daysLeft", { count: days }) : days === 0 ? t("sub.dueToday") : t("sub.overdue", { count: -days });
  return <Badge tone={daysTone(days)}>{text}</Badge>;
}

/** The days-left bar fills over this many days (a developer subscription renews every 90). */
const BAR_DAYS = 90;
const BAR_TONE: Record<string, string> = { danger: "bg-danger", warning: "bg-warning" };

/** Slim countdown bar: neutral above 30 days, warning at 30 or fewer, danger at 7 or fewer. */
function CountdownBar({ days }: { days: number }) {
  const { t } = useI18n();
  const pct = Math.max(2, Math.min(100, Math.round((Math.max(days, 0) / BAR_DAYS) * 100)));
  const label = days > 0 ? t("sub.daysLeft", { count: days }) : days === 0 ? t("sub.dueToday") : t("sub.overdue", { count: -days });
  return (
    <div
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={BAR_DAYS}
      aria-valuenow={Math.max(0, Math.min(days, BAR_DAYS))}
      aria-valuetext={label}
      className="h-1.5 overflow-hidden rounded-full bg-muted"
    >
      <div className={cn("h-full rounded-full", BAR_TONE[daysTone(days)] ?? "bg-muted-foreground/60")} style={{ width: `${pct}%` }} />
    </div>
  );
}

/** A tenant given by its ID alone is shown short; a domain is shown whole. */
const GUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const tenantName = (tv: TenantSubscriptions) => (GUID_RE.test(tv.label) ? `${tv.label.slice(0, 8)}…` : tv.label);

/** One block of the grid: a watched subscription of a tenant, or the state of a tenant that has none to show. */
interface CardItem {
  key: string;
  tv: TenantSubscriptions;
  s: SubscriptionView | null;
  /** The first card of its tenant carries the tenant-wide notes (hand-written date). */
  lead: boolean;
}

function cardItems(tenants: TenantSubscriptions[]): CardItem[] {
  return tenants.flatMap((tv): CardItem[] => {
    const subs = tv.access === "no_permission" ? [] : watched(tv);
    if (subs.length === 0) return [{ key: tv.tenant, tv, s: null, lead: true }];
    return subs.map((s, i) => ({ key: `${tv.tenant}:${s.id}`, tv, s, lead: i === 0 }));
  });
}

function RenewalHistory({ s, name, onClose }: { s: SubscriptionView; name: string; onClose: () => void }) {
  const { t, lang } = useI18n();
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose(); }}>
      <DialogContent closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle>{t("sub.historyTitle")}</DialogTitle>
          <DialogDescription>{name} · <span className="font-mono">{s.sku}</span></DialogDescription>
        </DialogHeader>
        <ul className="space-y-1.5 text-[13px] tabular-nums">
          {s.renewals.length === 0 && <li className="text-muted-foreground">{t("sub.historyEmpty")}</li>}
          {[...s.renewals].reverse().map((r) => (
            <li key={r.seenAt}>{t("sub.renewal", { from: r.from, to: r.to, when: formatRelative(r.seenAt, lang) })}</li>
          ))}
        </ul>
      </DialogContent>
    </Dialog>
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

function SubscriptionTile({ item, refreshing, onRefresh, onFix, onChanged }: {
  item: CardItem;
  refreshing: boolean;
  onRefresh: (tv: TenantSubscriptions) => void;
  onFix: (account: string) => void;
  onChanged: () => void;
}) {
  const { tv, s, lead } = item;
  const { t, lang } = useI18n();
  const [history, setHistory] = useState(false);
  const name = tenantName(tv);
  const st = s ? statusBadge(s.status) : null;
  const checkedAt = s ? s.checkedAt : tv.checkedAt;
  const days = s ? s.daysLeft : null;
  return (
    <Card className="flex min-w-0 flex-col gap-3 p-4" role="group" aria-label={s ? `${name} ${s.sku}` : name}>
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="truncate text-[15px] font-semibold" title={tv.tenantId ? `${tv.label} (${tv.tenantId})` : tv.label}>{name}</h3>
          <p className="truncate font-mono text-xs text-muted-foreground" title={tv.accounts.length ? t("sub.accounts", { names: tv.accounts.join(lang === "zh" ? "、" : ", ") }) : undefined}>
            {s ? s.sku : tv.watchedSkus.join(", ")}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          {st && s && <Badge tone={st.tone}>{st.label ? t(st.label) : s.status}</Badge>}
          <Button
            size="icon"
            variant="ghost"
            className="size-7"
            disabled={refreshing}
            aria-label={t("sub.refreshOne", { name })}
            title={t("sub.refreshOne", { name })}
            onClick={() => onRefresh(tv)}
          >
            {refreshing ? <Spinner /> : <RefreshCw />}
          </Button>
        </div>
      </div>

      {s && (
        <div className="space-y-1.5">
          <div className="flex flex-wrap items-baseline justify-between gap-x-2 text-[13px]">
            <span className="tabular-nums">{s.expires ? t("sub.endsOn", { date: s.expires }) : ""}</span>
            <DaysBadge days={s.daysLeft} />
          </div>
          {days !== null && <CountdownBar days={days} />}
          {s.isTrial && <Badge tone="info">{t("sub.trial")}</Badge>}
        </div>
      )}

      {tv.access === "no_permission" && (
        <div role="status" className="flex flex-wrap items-start gap-3 rounded-md border border-warning/40 bg-warning-bg p-3 text-[13px]">
          <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden />
          <div className="min-w-0 flex-1">
            <p className="font-medium">{t("sub.noPermission")}</p>
            <p className="mt-0.5 text-muted-foreground">{t("sub.noPermissionHow")}</p>
            <p className="mt-0.5 text-xs text-muted-foreground">{t("sub.fixHint", { account: tv.fixAccount })}</p>
            {tv.fixCommand && <p className="mt-0.5 break-all text-xs text-muted-foreground">{t("sub.fixCli")} <code className="rounded bg-muted px-1 py-0.5 font-mono select-all">{tv.fixCommand}</code></p>}
          </div>
          {tv.fixAccount && (
            <Button size="sm" onClick={() => onFix(tv.fixAccount)}>
              <ShieldCheck /> {t("sub.fix")}
            </Button>
          )}
        </div>
      )}
      {tv.access === "error" && (
        <p role="status" className="text-[13px] text-warning">{t("sub.error", { detail: tv.detail })}</p>
      )}
      {tv.access === "unchecked" && <p className="text-[13px] text-muted-foreground">{t("sub.unchecked")}</p>}
      {!s && (tv.access === "ok" || (tv.access === "error" && tv.subscriptions.length > 0)) && (
        <p className="text-[13px] text-muted-foreground">{t("sub.noWatched")}</p>
      )}

      {lead && <ManualDate key={`${tv.tenant}:${tv.manualExpires}:${tv.access}`} tv={tv} onSaved={onChanged} />}

      <div className="mt-auto flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span>{checkedAt ? t("sub.checked", { when: formatRelative(checkedAt, lang) }) : t("sub.neverChecked")}</span>
        {s && (
          <button
            type="button"
            className="cursor-pointer rounded text-foreground/80 underline-offset-2 hover:text-foreground hover:underline focus-visible:ring-2 focus-visible:ring-ring"
            aria-haspopup="dialog"
            onClick={() => setHistory(true)}
          >
            {t("sub.history", { count: s.renewals.length })}
          </button>
        )}
      </div>
      {s && history && <RenewalHistory s={s} name={name} onClose={() => setHistory(false)} />}
    </Card>
  );
}

/** The subscriptions the settings do not watch: no card of their own, listed on request. */
function OtherSubscriptions({ tenants }: { tenants: TenantSubscriptions[] }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const rows = tenants.flatMap((tv) => tv.subscriptions.filter((s) => !s.watched).map((s) => ({ tv, s })));
  if (rows.length === 0) return null;
  return (
    <>
      <button
        type="button"
        className="mt-3 cursor-pointer rounded text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline focus-visible:ring-2 focus-visible:ring-ring"
        aria-haspopup="dialog"
        onClick={() => setOpen(true)}
      >
        {t("sub.othersLink", { count: rows.length })}
      </button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle>{t("sub.othersTitle")}</DialogTitle>
            <DialogDescription>{t("sub.others", { count: rows.length })}</DialogDescription>
          </DialogHeader>
          <ul className="divide-y text-[13px]">
            {rows.map(({ tv, s }) => {
              const st = statusBadge(s.status);
              return (
                <li key={`${tv.tenant}:${s.id}`} className="flex flex-wrap items-center gap-x-2 gap-y-1 py-2">
                  <span className="font-mono">{s.sku}</span>
                  <span className="text-xs text-muted-foreground">{tenantName(tv)}</span>
                  <Badge tone={st.tone}>{st.label ? t(st.label) : s.status}</Badge>
                  {s.isTrial && <Badge tone="info">{t("sub.trial")}</Badge>}
                  {s.expires && <span className="text-xs tabular-nums text-muted-foreground">{t("sub.endsOn", { date: s.expires })}</span>}
                </li>
              );
            })}
          </ul>
        </DialogContent>
      </Dialog>
    </>
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

/** The "E5 subscriptions" section of the overview: one compact card per watched subscription, side by side. */
export function SubscriptionCard({ accounts }: { accounts: AccountSummary[] }) {
  const { t, lang } = useI18n();
  const toast = useToast();
  const [tenants, setTenants] = useState<TenantSubscriptions[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState<string | null>(null); // "" = all, else a tenant
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

  // account "" refreshes every tenant; otherwise the tenant that account belongs to.
  const refresh = async (account: string, scope: string) => {
    setRefreshing(scope);
    try {
      const r = await api.subscription.refresh(account);
      setTenants(r.tenants);
      setError(null);
      if (r.queued) toast.error(t("sub.queued"));
      else toast.success(t("sub.refreshed"));
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setRefreshing(null);
    }
  };

  return (
    <section className="mb-5" aria-labelledby="sub-title">
      <div className="mb-3 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 id="sub-title" className="text-[15px] font-semibold">{t("sub.title")}</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">{t("sub.desc", { skus: (tenants?.[0]?.watchedSkus ?? ["DEVELOPERPACK"]).join(lang === "zh" ? "、" : ", ") })}</p>
        </div>
        <Button size="sm" variant="outline" onClick={() => void refresh("", "")} disabled={refreshing !== null || !tenants?.length}>
          {refreshing === "" ? <Spinner /> : <RefreshCw />} {t("sub.refresh")}
        </Button>
      </div>
      {error && <p role="alert" className="text-[13px] text-danger">{error}</p>}
      {!tenants && !error && <p className="text-[13px] text-muted-foreground">{t("common.loading")}</p>}
      {repair ? (
        <Card className="p-4">
          <RepairPanel
            account={repair}
            onClose={() => setRepair(null)}
            onRepaired={() => { void api.subscription.refresh(repair).then((r) => setTenants(r.tenants)).catch((e) => setError(errMsg(e))); }}
          />
        </Card>
      ) : tenants && (
        <>
          <div className="grid grid-cols-[repeat(auto-fill,minmax(320px,1fr))] gap-4">
            {cardItems(tenants).map((item) => (
              <SubscriptionTile
                key={item.key}
                item={item}
                refreshing={refreshing === "" || refreshing === item.tv.tenant}
                onRefresh={(tv) => void refresh(dateOwner(tv), tv.tenant)}
                onFix={setRepair}
                onChanged={() => void load()}
              />
            ))}
          </div>
          <OtherSubscriptions tenants={tenants} />
        </>
      )}
    </section>
  );
}
