import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowDown, ArrowUp, BarChart3, CheckCircle2, RefreshCw, XCircle } from "lucide-react";
import { api } from "@/api";
import type { APIStat } from "@/api";
import { LOG_POLL_MS } from "@/config";
import { useI18n } from "@/i18n";
import { Button } from "@/components/ui/button";
import { EmptyBlock, ErrorBlock, LoadingBlock, Spinner } from "@/components/common";
import { nextSort, sortStats, lastWorked, type StatKey, type StatSort } from "@/lib/apistats";
import { formatLogTime } from "@/lib/time";
import { cn, errMsg } from "@/lib/utils";

/** Per-API call counters, like the table of the tool this app replaces: sortable, with the last result. */
export function ApiStats({ account, auto }: { account: string; auto: boolean }) {
  const { t, tl } = useI18n();
  const [rows, setRows] = useState<APIStat[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [fetching, setFetching] = useState(false);
  const [sort, setSort] = useState<StatSort>({ key: "total", dir: "desc" });
  const seq = useRef(0);

  const load = useCallback(async () => {
    const my = ++seq.current;
    setFetching(true);
    try {
      const r = await api.logs.apiStats(account);
      if (my === seq.current) {
        setRows(r);
        setError(null);
      }
    } catch (e) {
      if (my === seq.current) setError(errMsg(e));
    } finally {
      if (my === seq.current) setFetching(false);
    }
  }, [account]);

  useEffect(() => {
    void load();
  }, [load]);
  useEffect(() => {
    if (!auto) return;
    const id = window.setInterval(() => document.visibilityState === "visible" && void load(), LOG_POLL_MS);
    return () => window.clearInterval(id);
  }, [auto, load]);

  const label = useCallback((s: APIStat) => `${tl(`cat.${s.category}`, s.category)} · ${tl(`api.${s.id}`, s.name)}`, [tl]);
  const sorted = useMemo(() => (rows ? sortStats(rows, sort, label) : []), [rows, sort, label]);

  const cols: [StatKey, string, string][] = [
    ["api", t("logs.statsApi"), ""],
    ["last", t("logs.statsLast"), "w-40"],
    ["ok", t("logs.statsOk"), "w-20 text-right"],
    ["failed", t("logs.statsFail"), "w-20 text-right"],
    ["total", t("logs.statsTotal"), "w-20 text-right"],
  ];

  return (
    <>
      <div className="mb-2 flex items-center gap-3">
        <p className="text-xs text-muted-foreground">{t("logs.statsDesc")}</p>
        <Button className="ml-auto shrink-0" variant="outline" size="sm" onClick={() => void load()} disabled={fetching}>
          {fetching ? <Spinner /> : <RefreshCw />} {t("common.refresh")}
        </Button>
      </div>
      {error && <ErrorBlock className="mb-3" message={error} onRetry={() => void load()} />}
      {!rows ? (
        error ? null : <LoadingBlock rows={4} />
      ) : rows.length === 0 ? (
        <EmptyBlock icon={<BarChart3 />} title={t("logs.statsEmpty")} hint={t("logs.statsEmptyHint")} />
      ) : (
        <div className="scroll-stable max-h-[calc(100vh-15rem)] overflow-auto rounded-lg border bg-card">
          <table className="w-full table-fixed border-collapse text-[13px]">
            <thead className="sticky top-0 z-10 bg-muted text-left text-xs text-muted-foreground">
              <tr>
                {cols.map(([key, text, cls]) => (
                  <th key={key} scope="col" className={cn("px-3 py-2 font-medium", cls)} aria-sort={sort.key === key ? (sort.dir === "asc" ? "ascending" : "descending") : "none"}>
                    <button
                      type="button"
                      className={cn("inline-flex cursor-pointer items-center gap-1 rounded hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring", cls.includes("text-right") && "flex-row-reverse")}
                      aria-label={t("logs.sortBy", { col: text })}
                      onClick={() => setSort((s) => nextSort(s, key))}
                    >
                      {text}
                      {sort.key === key && (sort.dir === "asc" ? <ArrowUp className="size-3" aria-hidden /> : <ArrowDown className="size-3" aria-hidden />)}
                    </button>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="tabular-nums">
              {sorted.map((r) => {
                const worked = lastWorked(r);
                return (
                  <tr key={r.id} className={cn("border-t", !worked && "bg-danger-bg/70")}>
                    <td className="truncate px-3 py-1.5" title={r.id}>{label(r)}</td>
                    <td className="px-3 py-1.5 whitespace-nowrap" title={r.lastAt ? formatLogTime(r.lastAt) : undefined}>
                      <span className={cn("inline-flex items-center gap-1.5", !worked && "font-medium text-danger")}>
                        {worked ? <CheckCircle2 className="size-3.5 text-success" aria-hidden /> : <XCircle className="size-3.5" aria-hidden />}
                        {r.lastStatus || "—"}
                        <span className="text-xs font-normal opacity-80">{r.lastClass}</span>
                        <span className="sr-only">{worked ? t("logs.success") : t("logs.failed")}</span>
                      </span>
                    </td>
                    <td className="px-3 py-1.5 text-right">{r.ok}</td>
                    <td className={cn("px-3 py-1.5 text-right", r.failed > 0 && "font-medium text-danger")}>{r.failed}</td>
                    <td className="px-3 py-1.5 text-right">{r.total}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
