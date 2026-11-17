import { useCallback, useEffect, useRef, useState } from "react";
import { CheckCircle2, LogIn, RefreshCw, ShieldCheck, XCircle } from "lucide-react";
import { api } from "@/api";
import type { AccountInput, AuthKind, CatalogEntry, LoginEvent, RecommendResult, TestResult } from "@/api";
import { formatWhen } from "@/lib/time";
import { useI18n, type DictKey } from "@/i18n";
import { useApp } from "@/state/app";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { ErrorBlock, Spinner } from "@/components/common";
import { isInteractive, summarizeByCategory } from "@/lib/account";
import { cn, errMsg } from "@/lib/utils";
import { BrowserPrompt, DevicePrompt } from "@/components/signin";
import { progressMsg } from "@/lib/appsetup";
import { adviceByCategory, diffFromRecommended, hasFailedProbes, preselect, reasonMsg } from "@/lib/recommend";
import { ConsentNotice } from "./AppSetupFlow";

// The steps of adding an account that the edit form and the sign-in and test dialogs share.

export function CategoryName({ id }: { id: string }) {
  const { tl } = useI18n();
  return <span className="truncate">{tl(`cat.${id}`, id)}</span>;
}

/** What the API step knows about the account. */
export type RecState = { phase: "run" | "ok" | "login" | "fail"; result: RecommendResult | null; message: string };

const BADGE: Record<string, { key: DictKey; cls: string }> = {
  recommended: { key: "rec.badge.recommended", cls: "bg-success-bg text-success" },
  unavailable: { key: "rec.badge.unavailable", cls: "bg-muted text-muted-foreground" },
  maybe: { key: "rec.badge.maybe", cls: "bg-warning-bg text-warning" },
};

export function CategoriesStep({
  categories, error, onReload, selected, onChange, rec, onRetry, invalid,
}: {
  categories: string[] | null; error: string | null; onReload: () => void; selected: string[]; onChange: (c: string[]) => void;
  rec: RecState | null; onRetry: () => void; invalid: boolean;
}) {
  const { t, tl, lang } = useI18n();
  if (error) return <ErrorBlock message={error} onRetry={onReload} />;
  if (!categories || !rec || rec.phase === "run") {
    return (
      <div className="flex min-h-[18rem] flex-col items-center justify-center gap-3" role="status">
        <Spinner className="size-8 text-primary" />
        <p className="text-[13px] text-muted-foreground">{t("rec.checking")}</p>
      </div>
    );
  }
  const advice = adviceByCategory(rec.result);
  const nRecommended = rec.result?.categories.filter((c) => c.status === "recommended").length ?? 0;
  const diff = rec.phase === "ok" ? diffFromRecommended(categories, selected, rec.result) : { drop: [], add: [] };
  const names = (ids: string[]) => ids.map((c) => tl(`cat.${c}`, c)).join(lang === "zh" ? "、" : ", ");
  return (
    <div>
      <p className="mb-3 text-[13px] text-muted-foreground">{t("wizard.categoriesHint")}</p>
      {rec.phase === "ok" && <p className="mb-3 text-[13px] text-muted-foreground">{t("rec.summary", { n: nRecommended })}</p>}
      {(diff.drop.length > 0 || diff.add.length > 0) && (
        <p role="status" className="mb-3 rounded-md border border-warning/40 bg-warning-bg px-3 py-2 text-[13px] text-warning">
          {diff.drop.length > 0 && <span className="block">{t("rec.diff.drop", { names: names(diff.drop) })}</span>}
          {diff.add.length > 0 && <span className="block">{t("rec.diff.add", { names: names(diff.add) })}</span>}
        </p>
      )}
      {rec.phase === "ok" && hasFailedProbes(rec.result) && <p role="status" className="mb-3 text-[13px] text-warning">{t("rec.partial")}</p>}
      {rec.phase === "login" && <p role="status" className="mb-3 rounded-md border border-warning/40 bg-warning-bg px-3 py-2 text-[13px] text-warning">{t("rec.needsLogin")}</p>}
      {rec.phase === "fail" && (
        <div role="alert" className="mb-3 flex items-center gap-3 rounded-md border border-warning/40 bg-warning-bg px-3 py-2 text-[13px] text-warning">
          <span className="min-w-0 flex-1 break-words">{t("rec.failed", { detail: rec.message })}</span>
          <Button size="sm" variant="outline" onClick={onRetry}>
            <RefreshCw /> {t("rec.retry")}
          </Button>
        </div>
      )}
      <div className="mb-3 flex gap-2">
        {rec.phase === "ok" ? (
          <Button size="sm" variant="outline" onClick={() => onChange(preselect(categories, rec.result, selected))}>{t("wizard.selectRecommended")}</Button>
        ) : (
          <Button size="sm" variant="outline" onClick={() => onChange([...categories])}>{t("wizard.selectAll")}</Button>
        )}
        <Button size="sm" variant="outline" onClick={() => onChange([])}>{t("wizard.selectNone")}</Button>
        {rec.phase === "ok" && (
          <Button size="sm" variant="ghost" onClick={onRetry}>
            <RefreshCw /> {t("rec.retry")}
          </Button>
        )}
        <span className="ml-auto self-center text-xs tabular-nums text-muted-foreground">{t("wizard.selectedN", { n: selected.length, total: categories.length })}</span>
      </div>
      <div className="grid grid-cols-2 gap-2 md:grid-cols-3" role="group" aria-label={t("wizard.step4")}>
        {categories.map((c) => {
          const on = selected.includes(c);
          const adv = advice.get(c);
          const badge = adv ? BADGE[adv.status] : undefined;
          const why = adv ? reasonMsg(adv.reason) : null;
          return (
            <label key={c} className={cn("flex cursor-pointer items-start gap-2.5 rounded-md border px-3 py-2 text-[13px] hover:bg-accent/50", on && "border-primary/60 bg-accent/40", adv?.status === "unavailable" && !on && "opacity-70")}>
              <Checkbox className="mt-0.5" checked={on} onCheckedChange={(v) => onChange(v ? [...selected, c] : selected.filter((x) => x !== c))} />
              <span className="min-w-0 flex-1">
                <span className="flex items-center gap-2">
                  <CategoryName id={c} />
                  {badge && <span className={cn("shrink-0 rounded px-1.5 py-px text-[11px] font-medium", badge.cls)}>{t(badge.key)}</span>}
                </span>
                {why && <span className="mt-0.5 block break-words text-xs text-muted-foreground">{t(why.key, why.params)}</span>}
              </span>
            </label>
          );
        })}
      </div>
      {invalid && <p role="alert" className="mt-2 text-xs text-danger">{t("wizard.err.categories")}</p>}
    </div>
  );
}

type Phase = "idle" | "starting" | "waiting" | "done" | "error";

export function LoginStep({
  input, auto, afterSetup, consentUrl, alreadyDone, onDone, onIdle, onRestart, onStart,
}: {
  input: AccountInput; auto: boolean; afterSetup: boolean; consentUrl: string; alreadyDone: boolean; onDone: (d: boolean) => void; onIdle: (i: boolean) => void;
  onRestart: () => void; onStart: () => void;
}) {
  const { t } = useI18n();
  const { snapshot } = useApp();
  const [phase, setPhase] = useState<Phase>(alreadyDone ? "done" : auto ? "starting" : "idle");
  const [ev, setEv] = useState<LoginEvent | null>(null);
  const [message, setMessage] = useState("");
  const name = input.name;
  const phaseRef = useRef<Phase>(phase);
  phaseRef.current = phase;
  const auth: AuthKind = input.auth;
  const progressText = (m: string) => {
    const p = progressMsg(m);
    return p ? t(p.key, p.params) : m;
  };

  useEffect(() => {
    let alive = true;
    if (alreadyDone) return;
    if (!auto) {
      onIdle(true);
      return () => onIdle(false);
    }
    onDone(false);
    const off = api.accounts.onLoginEvent(name, (e) => {
      if (!alive) return;
      if (e.kind === "done") {
        setPhase("done");
        onDone(true);
        void snapshot.refresh();
      } else if (e.kind === "error") {
        setPhase("error");
        setMessage(e.message);
      } else {
        setEv(e);
        setPhase("waiting");
      }
    });
    (afterSetup ? api.accounts.loginAfterSetup(name) : api.accounts.login(name)).catch((e) => {
      if (!alive) return;
      setPhase("error");
      setMessage(errMsg(e));
    });
    return () => {
      alive = false;
      off();
      if (phaseRef.current === "starting" || phaseRef.current === "waiting") void api.accounts.cancelLogin(name).catch(() => undefined);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name]);

  return (
    <div className="flex min-h-[18rem] flex-col items-center justify-center gap-4 text-center" aria-live="polite">
      {phase === "idle" ? (
        <>
          <LogIn className="size-10 text-muted-foreground" aria-hidden />
          <p className="text-base font-medium">{t(consentUrl ? "wizard.savedConsent" : "wizard.savedIdle")}</p>
          <p className="max-w-md text-[13px] text-muted-foreground">{t(consentUrl ? "wizard.savedConsentHint" : "wizard.editLoginHint")}</p>
          {consentUrl && <ConsentNotice url={consentUrl} className="w-full max-w-lg" />}
          <Button variant="outline" onClick={onStart}>
            <LogIn /> {t("wizard.reLogin")}
          </Button>
        </>
      ) : phase === "done" ? (
        <>
          <CheckCircle2 className="size-12 text-success" aria-hidden />
          <p className="text-base font-medium">{t("wizard.loginDone")}</p>
          <p className="text-[13px] text-muted-foreground">{t("wizard.loginDoneHint")}</p>
        </>
      ) : phase === "error" ? (
        <>
          <XCircle className="size-12 text-danger" aria-hidden />
          <p className="text-base font-medium">{t("wizard.loginFailed")}</p>
          <p className="max-w-md break-words text-[13px] text-muted-foreground">{message}</p>
          <Button variant="outline" onClick={onRestart}>
            <RefreshCw /> {t("wizard.retry")}
          </Button>
        </>
      ) : ev?.kind === "device_code" ? (
        <DevicePrompt message={ev.message} userCode={ev.userCode} verificationUri={ev.verificationUri} />
      ) : ev?.kind === "browser_opened" ? (
        <BrowserPrompt />
      ) : ev?.kind === "progress" ? (
        <>
          <Spinner className="size-8 text-primary" />
          <p className="max-w-md text-[13px] text-muted-foreground">{progressText(ev.message)}</p>
        </>
      ) : (
        <>
          <Spinner className="size-8 text-primary" />
          <p className="text-[13px] text-muted-foreground">{isInteractive(auth) ? t("wizard.starting") : t("wizard.verifying")}</p>
        </>
      )}
    </div>
  );
}

export function TestStep({ name }: { name: string }) {
  const { t, tl } = useI18n();
  const [state, setState] = useState<{ phase: "run" | "ok" | "fail"; result: TestResult | null; entries: CatalogEntry[]; message: string }>({ phase: "run", result: null, entries: [], message: "" });
  const [n, setN] = useState(0);

  const run = useCallback(async () => {
    setState({ phase: "run", result: null, entries: [], message: "" });
    try {
      const result = await api.accounts.test(name);
      // The per-category breakdown is a bonus; the test result stands without it.
      const entries = await api.catalog.list(name).catch(() => [] as CatalogEntry[]);
      setState({ phase: "ok", result, entries, message: "" });
    } catch (e) {
      setState({ phase: "fail", result: null, entries: [], message: errMsg(e) });
    }
  }, [name]);

  useEffect(() => {
    void run();
  }, [run, n]);

  if (state.phase === "run") {
    return (
      <div className="flex min-h-[18rem] flex-col items-center justify-center gap-3" role="status">
        <Spinner className="size-8 text-primary" />
        <p className="text-[13px] text-muted-foreground">{t("wizard.testing")}</p>
      </div>
    );
  }
  if (state.phase === "fail" || !state.result) {
    return (
      <div className="flex min-h-[18rem] flex-col items-center justify-center gap-3 text-center">
        <XCircle className="size-12 text-danger" aria-hidden />
        <p className="text-base font-medium">{t("wizard.testFailed")}</p>
        <p className="max-w-md break-words text-[13px] text-muted-foreground">{state.message}</p>
        <Button variant="outline" onClick={() => setN((x) => x + 1)}>
          <RefreshCw /> {t("wizard.retest")}
        </Button>
      </div>
    );
  }
  const { result } = state;
  const scopes = [...result.scopes].sort();
  const cats = summarizeByCategory(state.entries);
  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <ShieldCheck className="size-8 text-success" aria-hidden />
        <div>
          <p className="text-base font-medium tabular-nums">{t("wizard.usableApis", { n: result.usableApis, total: result.totalApis, count: result.totalApis })}</p>
          <p className="text-[13px] text-muted-foreground">
            {t("wizard.testOk")}
            {result.expiresOn && <> {t("wizard.expiresOn", { when: formatWhen(result.expiresOn) })}</>}
          </p>
        </div>
      </div>
      <div>
        <p className="mb-1.5 text-[13px] font-medium">{t("wizard.grantedScopes")}</p>
        {scopes.length ? (
          <ul className="flex flex-wrap gap-1.5">
            {scopes.map((s) => (
              <li key={s} className="rounded border bg-muted/50 px-1.5 py-0.5 font-mono text-xs">{s}</li>
            ))}
          </ul>
        ) : (
          <p className="text-[13px] text-muted-foreground">{t("wizard.noScopes")}</p>
        )}
      </div>
      {cats.length > 0 && (
        <div>
          <p className="mb-1.5 text-[13px] font-medium">{t("wizard.byCategory")}</p>
          <ul className="grid grid-cols-2 gap-x-6 gap-y-1 text-[13px] md:grid-cols-3">
            {cats.map((c) => (
              <li key={c.category} className="flex items-center justify-between gap-2 tabular-nums">
                <span className="flex items-center gap-1.5 truncate">
                  {c.usable === c.total ? <CheckCircle2 className="size-3.5 shrink-0 text-success" aria-hidden /> : c.usable === 0 ? <XCircle className="size-3.5 shrink-0 text-danger" aria-hidden /> : <span className="inline-block size-3.5 shrink-0 text-center text-warning" aria-hidden>!</span>}
                  <span className="truncate">{tl(`cat.${c.category}`, c.category)}</span>
                </span>
                <span className="text-muted-foreground">{c.usable}/{c.total}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
