import { useCallback, useEffect, useRef, useState } from "react";
import { Info, RefreshCw, ScrollText, XCircle, CheckCircle2 } from "lucide-react";
import { api } from "@/api";
import type { LogEntry } from "@/api";
import { LOG_POLL_MS } from "@/config";
import { useApp } from "@/state/app";
import { useI18n, type DictKey } from "@/i18n";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { ApiStats } from "./ApiStats";
import { EmptyBlock, ErrorBlock, LoadingBlock, PageHeader, Spinner } from "@/components/common";
import { formatLatency, formatLogTime } from "@/lib/time";
import { isFailedLog } from "@/lib/status";
import { cn, errMsg } from "@/lib/utils";

const ALL = "__all";
const RANGES: [number, DictKey][] = [[1, "logs.range.1"], [6, "logs.range.6"], [24, "logs.range.24"], [72, "logs.range.72"], [168, "logs.range.168"]];
const LIMIT = 500;

export function Logs() {
  const { t } = useI18n();
  const { snapshot } = useApp();
  const [account, setAccount] = useState(ALL);
  const [hours, setHours] = useState(24);
  const [auto, setAuto] = useState(true);
  const [tab, setTab] = useState<"calls" | "stats">("calls");
  const [rows, setRows] = useState<LogEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [fetching, setFetching] = useState(false);
  const seq = useRef(0);

  const load = useCallback(async () => {
    const my = ++seq.current;
    setFetching(true);
    try {
      const r = await api.logs.read(account === ALL ? "" : account, hours, LIMIT);
      if (my === seq.current) {
        setRows(r);
        setError(null);
      }
    } catch (e) {
      if (my === seq.current) setError(errMsg(e));
    } finally {
      if (my === seq.current) setFetching(false);
    }
  }, [account, hours]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!auto) return;
    let timer: number | undefined;
    const start = () => {
      if (timer === undefined) timer = window.setInterval(() => void load(), LOG_POLL_MS);
    };
    const stop = () => {
      if (timer !== undefined) window.clearInterval(timer);
      timer = undefined;
    };
    const onVis = () => (document.visibilityState === "visible" ? (void load(), start()) : stop());
    if (document.visibilityState === "visible") start();
    document.addEventListener("visibilitychange", onVis);
    return () => {
      document.removeEventListener("visibilitychange", onVis);
      stop();
    };
  }, [auto, load]);

  const names = snapshot.data?.accounts.map((a) => a.name) ?? [];
  const failed = rows?.filter(isFailedLog).length ?? 0;

  return (
    <>
      <PageHeader title={t("nav.logs")} description={t("logs.desc")} />
      <div role="tablist" aria-label={t("nav.logs")} className="mb-3 flex gap-1 border-b">
        {(["calls", "stats"] as const).map((k) => (
          <button
            key={k}
            type="button"
            role="tab"
            aria-selected={tab === k}
            onClick={() => setTab(k)}
            className={cn(
              "-mb-px cursor-pointer border-b-2 px-3 py-1.5 text-[13px] font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring",
              tab === k ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {t(k === "calls" ? "logs.tabCalls" : "logs.tabStats")}
          </button>
        ))}
      </div>
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <div className="w-44">
          <Select value={account} onValueChange={setAccount}>
            <SelectTrigger aria-label={t("logs.account")}><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t("logs.allAccounts")}</SelectItem>
              {names.map((n) => <SelectItem key={n} value={n}>{n}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
        {tab === "calls" && <div className="w-40">
          <Select value={String(hours)} onValueChange={(v) => setHours(Number(v))}>
            <SelectTrigger aria-label={t("logs.range")}><SelectValue /></SelectTrigger>
            <SelectContent>
              {RANGES.map(([h, k]) => <SelectItem key={h} value={String(h)}>{t(k)}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>}
        <label className="ml-auto flex cursor-pointer items-center gap-2 text-[13px]">
          <Switch checked={auto} onCheckedChange={setAuto} aria-label={t("logs.auto")} />
          {t("logs.auto")}
        </label>
        {tab === "calls" && (
          <Button variant="outline" size="sm" onClick={() => void load()} disabled={fetching}>
            {fetching ? <Spinner /> : <RefreshCw />} {t("common.refresh")}
          </Button>
        )}
      </div>

      {tab === "stats" && <ApiStats account={account === ALL ? "" : account} auto={auto} />}

      {tab === "calls" && error && <ErrorBlock className="mb-3" message={error} onRetry={() => void load()} />}

      {tab !== "calls" ? null : !rows ? (
        error ? null : <LoadingBlock rows={4} />
      ) : rows.length === 0 ? (
        <EmptyBlock icon={<ScrollText />} title={t("logs.empty")} hint={t("logs.emptyHint")} />
      ) : (
        <>
          <p className="mb-2 text-xs tabular-nums text-muted-foreground">{t("logs.summary", { count: rows.length, failed })}</p>
          <div className="scroll-stable max-h-[calc(100vh-15rem)] overflow-auto rounded-lg border bg-card">
            <table className="w-full table-fixed border-collapse text-[13px]">
              <thead className="sticky top-0 z-10 bg-muted text-left text-xs text-muted-foreground">
                <tr>
                  <th className="w-[8.5rem] px-3 py-2 font-medium">{t("logs.time")}</th>
                  <th className="w-24 px-3 py-2 font-medium">{t("logs.accountCol")}</th>
                  <th className="px-3 py-2 font-medium">API</th>
                  <th className="w-28 px-3 py-2 font-medium">{t("logs.status")}</th>
                  <th className="w-[4.5rem] px-3 py-2 text-right font-medium">{t("logs.latency")}</th>
                </tr>
              </thead>
              <tbody className="tabular-nums">
                {rows.map((r, i) => {
                  const bad = isFailedLog(r);
                  return (
                    <tr key={`${r.ts}-${r.account}-${r.api}-${i}`} className={cn("border-t", bad && "bg-danger-bg/70")}>
                      <td className="px-3 py-1.5 whitespace-nowrap text-muted-foreground">{formatLogTime(r.ts)}</td>
                      <td className="truncate px-3 py-1.5">{r.account}</td>
                      {r.event ? (
                        <td colSpan={3} className="px-3 py-1.5">
                          <span className="inline-flex items-center gap-1.5 font-medium text-info">
                            <Info className="size-3.5" aria-hidden /> {r.event}
                          </span>
                          {r.detail && <span className="ml-2 text-muted-foreground">{r.detail}</span>}
                        </td>
                      ) : (
                        <>
                          <td className="truncate px-3 py-1.5 font-mono text-xs">
                            {r.api}
                            {r.detail && <span className="ml-2 font-sans text-muted-foreground">{r.detail}</span>}
                          </td>
                          <td className="px-3 py-1.5 whitespace-nowrap">
                            <span className={cn("inline-flex items-center gap-1.5", bad && "font-medium text-danger")}>
                              {bad ? <XCircle className="size-3.5" aria-hidden /> : <CheckCircle2 className="size-3.5 text-success" aria-hidden />}
                              {r.status || "—"}
                              <span className="text-xs font-normal opacity-80">{r.class}</span>
                              <span className="sr-only">{bad ? t("logs.failed") : t("logs.success")}</span>
                            </span>
                          </td>
                          <td className="px-3 py-1.5 text-right whitespace-nowrap">{formatLatency(r.latencyMs)}</td>
                        </>
                      )}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </>
      )}
    </>
  );
}
