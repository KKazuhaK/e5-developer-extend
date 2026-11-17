import { useCallback, useEffect, useRef, useState } from "react";
import { ArrowLeft, ArrowRight, Check, CheckCircle2, ExternalLink, LogIn, RefreshCw, ShieldCheck, XCircle } from "lucide-react";
import { api } from "@/api";
import type { AccountInput, AppSetupCancelResult, AppSetupResult, AuthKind, CatalogEntry, LoginEvent, RecommendResult, TestResult } from "@/api";
import { formatWhen } from "@/lib/time";
import { DOCS_URL } from "@/config";
import { useI18n, type DictKey } from "@/i18n";
import { useApp } from "@/state/app";
import { useToast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { ErrorBlock, Field, LoadingBlock, PageHeader, Spinner, fieldAria } from "@/components/common";
import { AUTH_KINDS, isInteractive, needsSecret, needsUsername, secretRequired, summarizeByCategory, validateAccountInput } from "@/lib/account";
import type { AccountErrorCode } from "@/lib/account";
import { cn, errMsg } from "@/lib/utils";
import { BrowserPrompt, DevicePrompt } from "@/components/signin";
import { progressMsg, validateAutoStart } from "@/lib/appsetup";
import { adviceByCategory, diffFromRecommended, preselect, reasonMsg } from "@/lib/recommend";
import { AppSetupStep, AutoStartForm, ChoiceCard, ConsentNotice, type AdminMethod } from "./AppSetupFlow";

type Mode = "add" | "edit";
type Route = "auto" | "manual";
const STEPS: DictKey[] = ["wizard.step1", "wizard.step2", "wizard.step3", "wizard.step4", "wizard.step5", "wizard.step6"];

const EMPTY: AccountInput = { name: "", auth: "device_code", tenant: "", clientId: "", username: "", categories: [], secretExpires: "" };

export function AccountWizard({ mode, name, startStep, onClose }: { mode: Mode; name?: string; startStep?: number; onClose: () => void }) {
  const { t } = useI18n();
  const { snapshot } = useApp();
  const [step, setStep] = useState(startStep ?? 1);
  const [input, setInput] = useState<AccountInput>(EMPTY);
  const [loaded, setLoaded] = useState(mode === "add");
  const [loadError, setLoadError] = useState<string | null>(null);
  const [errors, setErrors] = useState<Partial<Record<string, AccountErrorCode>>>({});
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [persisted, setPersisted] = useState(mode === "edit");
  // Auth kind the stored account has; null until it is saved. Decides whether a secret is required.
  const [savedAuth, setSavedAuth] = useState<AuthKind | null>(null);
  const [allCategories, setAllCategories] = useState<string[] | null>(null);
  const [catError, setCatError] = useState<string | null>(null);
  const [loginDone, setLoginDone] = useState(false);
  const [idle, setIdle] = useState(false);
  const canContinue = loginDone || idle;
  const [restart, setRestart] = useState(0);
  // Add: log in straight after saving. Edit: only when asked (or when opened from "re-login").
  const [autoLogin, setAutoLogin] = useState(mode === "add" || startStep === 3);
  // Add: the app registration is created for the user (default) or the user brings one. Edit is always manual.
  const [route, setRoute] = useState<Route>(mode === "add" ? "auto" : "manual");
  const [adminMethod, setAdminMethod] = useState<AdminMethod>("device_code");
  // What automatic setup made; null until it has run. Changing its inputs throws it away.
  const [setup, setSetup] = useState<AppSetupResult | null>(null);
  const [setupRestart, setSetupRestart] = useState(0);
  const auto = route === "auto";
  const toast = useToast();
  // The session of a finished automatic setup, until the account is saved (then it is finished) or the setup is thrown away (then it is cancelled).
  const setupSid = useRef<string | null>(null);
  // True while a thrown-away setup is being cleaned up; a new one must not start before that is over.
  const [cleaning, setCleaning] = useState(false);
  const discardSetup = (): Promise<void> => {
    const sid = setupSid.current;
    setupSid.current = null;
    if (!sid) return Promise.resolve();
    setCleaning(true);
    // An unused client secret is deleted again; say what happened.
    return api.appSetup
      .cancel(sid)
      .then((r: AppSetupCancelResult) => {
        if (r.secretRemoved) toast.success(t("auto.secretRevoked"));
        else if (r.error) toast.error(t("auto.secretStillLive", { detail: r.error }));
      })
      .catch(() => undefined)
      .finally(() => setCleaning(false));
  };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => () => void discardSetup(), []); // leaving the wizard

  const changeAuto = (patch: Partial<AccountInput>) => {
    setErrors({});
    setInput((i) => {
      const next = { ...i, ...patch };
      if (patch.auth && patch.auth !== "client_secret") next.username = "";
      return next;
    });
    if (setup) {
      void discardSetup();
      setSetup(null);
      setInput((i) => ({ ...i, tenant: "", clientId: "", secretExpires: "" }));
    }
  };
  const onSetupResult = (r: AppSetupResult, sessionId: string) => {
    setupSid.current = sessionId;
    setSetup(r);
    setInput((i) => ({ ...i, tenant: r.tenantId, clientId: r.clientId, secretExpires: r.secretExpires }));
    // Without consent the account is saved but cannot sign in yet.
    setAutoLogin(r.consentGranted);
  };

  const set = <K extends keyof AccountInput>(k: K, v: AccountInput[K]) => setInput((i) => ({ ...i, [k]: v }));

  useEffect(() => {
    if (mode !== "edit" || !name) return;
    api.accounts
      .get(name)
      .then((a) => {
        setInput({ ...a, secret: undefined });
        setSavedAuth(a.auth);
        setLoaded(true);
      })
      .catch((e) => setLoadError(errMsg(e)));
  }, [mode, name]);

  useEffect(() => {
    api.catalog
      .categories()
      .then(setAllCategories)
      .catch((e) => setCatError(errMsg(e)));
  }, []);

  // What the API step knows about the account. It is kept across steps so going back does not undo the user's choice.
  const [rec, setRec] = useState<RecState | null>(null);
  const probeSeq = useRef(0);
  const probe = useCallback(async () => {
    if (!allCategories) return;
    const seq = ++probeSeq.current;
    setRec({ phase: "run", result: null, message: "" });
    try {
      const result = await api.accounts.recommend(input.name.trim());
      if (seq !== probeSeq.current) return;
      if (result.needsLogin) {
        setRec({ phase: "login", result: null, message: "" });
        // Without advice a new account starts with everything ticked, an existing one keeps its choice.
        setInput((i) => (i.categories.length ? i : { ...i, categories: [...allCategories] }));
      } else {
        setRec({ phase: "ok", result, message: "" });
        // A fresh account (no categories yet) starts from the recommendation; an existing choice is kept and compared.
        setInput((i) => (i.categories.length ? i : { ...i, categories: preselect(allCategories, result) }));
      }
    } catch (e) {
      if (seq !== probeSeq.current) return;
      setRec({ phase: "fail", result: null, message: errMsg(e) });
      setInput((i) => (i.categories.length ? i : { ...i, categories: [...allCategories] }));
    }
  }, [allCategories, input.name]);
  useEffect(() => {
    if (step === 4 && loaded && allCategories && !rec) void probe();
  }, [step, loaded, allCategories, rec, probe]);

  const err = (f: string): string | undefined => {
    const c = errors[f];
    if (!c) return undefined;
    return c === "guid" ? t("wizard.err.guid") : c === "name" ? t("wizard.err.name") : c === "date" ? t("wizard.err.date") : t("wizard.err.required");
  };

  // Saves the account: the first time it is added (before sign-in needs it to exist), afterwards updated.
  const persist = async (): Promise<boolean> => {
    setSaving(true);
    try {
      const payload: AccountInput = { ...input, name: input.name.trim(), tenant: input.tenant.trim(), clientId: input.clientId.trim(), username: input.username.trim() };
      // Automatic setup already put the client secret in the vault.
      if (auto && setup && payload.auth === "client_secret" && !persisted) payload.useStoredSecret = true;
      if (persisted) await api.accounts.update({ ...payload, secret: payload.secret || undefined });
      else await api.accounts.add(payload);
      setPersisted(true);
      if (setupSid.current) {
        // The account owns the setup's result now (and its secret).
        void api.appSetup.finish(setupSid.current).catch(() => undefined);
        setupSid.current = null;
      }
      setSavedAuth(payload.auth);
      setInput((i) => ({ ...i, ...payload, secret: undefined })); // the secret is gone from memory after this
      void snapshot.refresh();
      return true;
    } catch (ex) {
      setSaveError(errMsg(ex));
      return false;
    } finally {
      setSaving(false);
    }
  };

  const next = async () => {
    setSaveError(null);
    if (step === 1 && auto && mode === "add") {
      const e = validateAutoStart(input);
      setErrors(e);
      if (Object.keys(e).length) return;
    }
    if (step === 2 && !auto) {
      const e = validateAccountInput(input, mode, "identity", savedAuth);
      setErrors(e);
      if (Object.keys(e).length) return;
    }
    if (step === 2) {
      if (!(await persist())) return;
      setRec(null); // the sign-in may be new; ask again on the API step
    }
    if (step === 4) {
      const e = validateAccountInput(input, mode, "categories");
      setErrors(e);
      if (Object.keys(e).length) return;
      if (!(await persist())) return;
    }
    setStep((s) => Math.min(6, s + 1));
  };
  const back = () => {
    setSaveError(null);
    setStep((s) => Math.max(1, s - 1));
  };

  const title = mode === "add" ? t("wizard.titleAdd") : t("wizard.titleEdit", { name: name ?? "" });

  if (loadError) {
    return (
      <>
        <PageHeader title={title} />
        <ErrorBlock message={loadError} onRetry={onClose} />
      </>
    );
  }
  if (!loaded) {
    return (
      <>
        <PageHeader title={title} />
        <LoadingBlock rows={2} />
      </>
    );
  }

  const needSecretNow = secretRequired(input.auth, savedAuth);

  return (
    <>
      <PageHeader title={title} description={t("wizard.subtitle")} actions={step < 6 && <Button variant="ghost" onClick={onClose} disabled={saving}>{t("common.cancel")}</Button>} />

      <ol className="mb-5 flex items-center gap-1" aria-label={t("wizard.progress")}>
        {STEPS.map((k, i) => {
          const n = i + 1;
          const state = n < step ? "done" : n === step ? "current" : "todo";
          return (
            <li key={k} className="flex min-w-0 flex-1 items-center gap-1.5" aria-current={state === "current" ? "step" : undefined}>
              <span
                className={cn(
                  "flex size-6 shrink-0 items-center justify-center rounded-full border text-xs font-semibold",
                  state === "done" && "border-success bg-success-bg text-success",
                  state === "current" && "border-primary bg-primary text-primary-foreground",
                  state === "todo" && "text-muted-foreground",
                )}
              >
                {state === "done" ? <Check className="size-3.5" aria-hidden /> : n}
              </span>
              <span className={cn("truncate text-xs", state === "current" ? "font-medium" : "text-muted-foreground")}>{t(k === "wizard.step2" && auto ? "wizard.step2auto" : k)}</span>
              {n < 6 && <span className="mx-1 h-px min-w-2 flex-1 bg-border" aria-hidden />}
            </li>
          );
        })}
      </ol>

      <Card className="min-h-[22rem] p-5">
        {step === 1 && (
          <div>
            {mode === "add" && (
              <div role="radiogroup" aria-label={t("wizard.step1")} className="grid gap-2.5">
                <ChoiceCard
                  selected={auto}
                  disabled={persisted}
                  onSelect={() => {
                    setErrors({});
                    setRoute("auto");
                  }}
                  title={t("auto.route.auto")}
                  hint={t("auto.route.autoHint")}
                  badge={<span className="rounded bg-info-bg px-1.5 py-px text-[11px] font-medium text-info">{t("wizard.recommended")}</span>}
                />
                <ChoiceCard
                  selected={!auto}
                  disabled={persisted}
                  onSelect={() => {
                    setErrors({});
                    setRoute("manual");
                  }}
                  title={t("auto.route.manual")}
                  hint={t("auto.route.manualHint")}
                />
              </div>
            )}
            {auto && mode === "add" ? (
              <AutoStartForm input={input} method={adminMethod} errors={errors} locked={persisted} onChange={changeAuto} onMethod={setAdminMethod} />
            ) : (
              <div className={mode === "add" ? "mt-4" : undefined}>
                <div role="radiogroup" aria-label={t("wizard.step1")} className="grid gap-2.5">
                {AUTH_KINDS.map((k) => {
                  const sel = input.auth === k;
                  return (
                    <button
                      key={k}
                      type="button"
                      role="radio"
                      aria-checked={sel}
                      onClick={() => set("auth", k)}
                      className={cn(
                        "flex cursor-pointer items-start gap-3 rounded-lg border p-3.5 text-left transition-colors hover:bg-accent/50 disabled:cursor-not-allowed disabled:opacity-45",
                        sel && "border-primary bg-accent/60 ring-1 ring-primary",
                      )}
                    >
                      <span className={cn("mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border", sel ? "border-primary bg-primary text-primary-foreground" : "border-input")} aria-hidden>
                        {sel && <Check className="size-3" strokeWidth={3} />}
                      </span>
                      <span className="min-w-0">
                        <span className="flex items-center gap-2 font-medium">
                          {t(`auth.${k}`)}
                          {k === "device_code" && <span className="rounded bg-info-bg px-1.5 py-px text-[11px] font-medium text-info">{t("wizard.recommended")}</span>}
                          {k === "password" && <span className="rounded bg-warning-bg px-1.5 py-px text-[11px] font-medium text-warning">{t("wizard.notRecommended")}</span>}
                        </span>
                        <span className="mt-0.5 block text-[13px] text-muted-foreground">{t(`auth.${k}.hint`)}</span>
                      </span>
                    </button>
                  );
                })}
              </div>
              </div>
            )}
          </div>
        )}

        {step === 2 && auto && (
          <AppSetupStep
            key={setupRestart}
            input={input}
            method={adminMethod}
            done={setup}
            onResult={onSetupResult}
            onRestart={() => setSetupRestart((n) => n + 1)}
            onAbort={() => setStep(1)}
            onManual={() => {
              void discardSetup();
              setRoute("manual");
              setSetup(null);
              setStep(1);
            }}
          />
        )}
        {step === 2 && !auto && (
          <form
            className="grid gap-4 sm:grid-cols-2"
            onSubmit={(e) => {
              e.preventDefault();
              void next();
            }}
            autoComplete="off"
          >
            <Field id="w-name" label={t("wizard.name")} hint={t("wizard.nameHint")} error={err("name")}>
              <Input {...fieldAria("w-name", err("name"), "x")} value={input.name} disabled={mode === "edit" || persisted} maxLength={32} onChange={(e) => set("name", e.target.value)} />
            </Field>
            <Field id="w-tenant" label={t("wizard.tenant")} hint={t("wizard.tenantHint")} error={err("tenant")}>
              <Input {...fieldAria("w-tenant", err("tenant"), "x")} value={input.tenant} onChange={(e) => set("tenant", e.target.value)} placeholder="contoso.onmicrosoft.com" spellCheck={false} />
            </Field>
            <Field id="w-client" label="Client ID" hint={t("wizard.clientHint")} error={err("clientId")} className="sm:col-span-2">
              <Input {...fieldAria("w-client", err("clientId"), "x")} value={input.clientId} onChange={(e) => set("clientId", e.target.value)} placeholder="00000000-0000-0000-0000-000000000000" className="font-mono" spellCheck={false} />
            </Field>
            {needsUsername(input.auth) && (
              <Field id="w-user" label={t(input.auth === "password" ? "wizard.usernamePwd" : "wizard.usernameApp")} hint={t(input.auth === "password" ? "wizard.usernamePwdHint" : "wizard.usernameAppHint")} error={err("username")}>
                <Input {...fieldAria("w-user", err("username"), "x")} value={input.username} onChange={(e) => set("username", e.target.value)} placeholder="user@contoso.onmicrosoft.com" spellCheck={false} />
              </Field>
            )}
            {needsSecret(input.auth) && (
              <Field id="w-secret" label={t(input.auth === "password" ? "wizard.password" : "wizard.secret")} hint={t(needSecretNow ? "wizard.secretHint" : "wizard.secretKeepHint")} error={err("secret")}>
                <Input {...fieldAria("w-secret", err("secret"), "x")} type="password" autoComplete="new-password" value={input.secret ?? ""} onChange={(e) => set("secret", e.target.value)} />
              </Field>
            )}
            {input.auth === "client_secret" && (
              <Field id="w-exp" label={t("wizard.secretExpires")} hint={t("wizard.secretExpiresHint")} error={err("secretExpires")}>
                <Input {...fieldAria("w-exp", err("secretExpires"), "x")} type="date" value={input.secretExpires} onChange={(e) => set("secretExpires", e.target.value)} />
              </Field>
            )}
            <p className="text-xs text-muted-foreground sm:col-span-2">
              {t("wizard.needRegistration")}{" "}
              <button type="button" className="cursor-pointer text-primary underline-offset-2 hover:underline" onClick={() => void api.system.openExternal(DOCS_URL)}>
                {t("overview.appDocs")} <ExternalLink className="inline size-3" aria-hidden />
              </button>
            </p>
            <button type="submit" className="hidden" aria-hidden tabIndex={-1} />
          </form>
        )}

        {step === 3 && (
          <LoginStep
            key={restart}
            input={input}
            auto={autoLogin}
            afterSetup={auto && !!setup}
            consentUrl={auto && setup && !setup.consentGranted ? setup.adminConsentUrl : ""}
            alreadyDone={loginDone}
            onDone={(d) => {
              setLoginDone(d);
              if (d) setRec(null); // a fresh sign-in: ask again on the API step
            }}
            onIdle={setIdle}
            onRestart={() => setRestart((n) => n + 1)}
            onStart={() => {
              setAutoLogin(true);
              setIdle(false);
              setRestart((n) => n + 1);
            }}
          />
        )}
        {step === 4 && (
          <CategoriesStep
            categories={allCategories}
            error={catError}
            selected={input.categories}
            onChange={(c) => set("categories", c)}
            rec={rec}
            onRetry={() => void probe()}
            invalid={!!errors.categories}
          />
        )}
        {step === 5 && <TestStep name={input.name} />}
        {step === 6 && <DoneStep name={input.name} />}
      </Card>

      {cleaning && (
        <p role="status" className="mt-3 flex items-center gap-2 text-[13px] text-muted-foreground">
          <Spinner /> {t("auto.cleaning")}
        </p>
      )}
      {saveError && <ErrorBlock className="mt-3" message={saveError} />}

      <div className="mt-4 flex items-center justify-between">
        <Button variant="outline" onClick={back} disabled={step === 1 || step === 6 || saving}>
          <ArrowLeft /> {t("wizard.back")}
        </Button>
        <div className="flex gap-2">
          {step < 6 ? (
            <Button onClick={() => void next()} disabled={saving || cleaning || (step === 3 && !canContinue) || (step === 4 && (!rec || rec.phase === "run")) || (step === 2 && auto && !setup)}>
              {saving && <Spinner />}
              {step === 2 ? t("wizard.saveAndLogin") : step === 4 ? t("wizard.saveAndTest") : t("wizard.next")} {!saving && <ArrowRight />}
            </Button>
          ) : (
            <Button onClick={onClose}>{t("wizard.finish")}</Button>
          )}
        </div>
      </div>
    </>
  );
}

function CategoryName({ id }: { id: string }) {
  const { tl } = useI18n();
  return <span className="truncate">{tl(`cat.${id}`, id)}</span>;
}

/** What the API step knows about the account. */
type RecState = { phase: "run" | "ok" | "login" | "fail"; result: RecommendResult | null; message: string };

const BADGE: Record<string, { key: DictKey; cls: string }> = {
  recommended: { key: "rec.badge.recommended", cls: "bg-success-bg text-success" },
  unavailable: { key: "rec.badge.unavailable", cls: "bg-muted text-muted-foreground" },
  maybe: { key: "rec.badge.maybe", cls: "bg-warning-bg text-warning" },
};

function CategoriesStep({
  categories, error, selected, onChange, rec, onRetry, invalid,
}: {
  categories: string[] | null; error: string | null; selected: string[]; onChange: (c: string[]) => void;
  rec: RecState | null; onRetry: () => void; invalid: boolean;
}) {
  const { t, tl, lang } = useI18n();
  if (error) return <ErrorBlock message={error} />;
  if (!categories || !rec || rec.phase === "run") {
    return (
      <div className="flex min-h-[18rem] flex-col items-center justify-center gap-3" role="status">
        <Spinner className="size-8 text-primary" />
        <p className="text-[13px] text-muted-foreground">{t("rec.checking")}</p>
      </div>
    );
  }
  const advice = adviceByCategory(rec.result);
  const recommended = preselect(categories, rec.result);
  const diff = rec.phase === "ok" ? diffFromRecommended(categories, selected, rec.result) : { drop: [], add: [] };
  const names = (ids: string[]) => ids.map((c) => tl(`cat.${c}`, c)).join(lang === "zh" ? "、" : ", ");
  return (
    <div>
      <p className="mb-3 text-[13px] text-muted-foreground">{t("wizard.categoriesHint")}</p>
      {rec.phase === "ok" && <p className="mb-3 text-[13px] text-muted-foreground">{t("rec.summary", { n: recommended.length })}</p>}
      {(diff.drop.length > 0 || diff.add.length > 0) && (
        <p role="status" className="mb-3 rounded-md border border-warning/40 bg-warning-bg px-3 py-2 text-[13px] text-warning">
          {diff.drop.length > 0 && <span className="block">{t("rec.diff.drop", { names: names(diff.drop) })}</span>}
          {diff.add.length > 0 && <span className="block">{t("rec.diff.add", { names: names(diff.add) })}</span>}
        </p>
      )}
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
          <Button size="sm" variant="outline" onClick={() => onChange(recommended)}>{t("wizard.selectRecommended")}</Button>
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

function LoginStep({
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

function TestStep({ name }: { name: string }) {
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

function DoneStep({ name }: { name: string }) {
  const { t } = useI18n();
  return (
    <div className="flex min-h-[18rem] flex-col items-center justify-center gap-3 text-center">
      <CheckCircle2 className="size-14 text-success" aria-hidden />
      <p className="text-lg font-semibold">{t("wizard.doneTitle", { name })}</p>
      <p className="max-w-md text-[13px] text-muted-foreground">{t("wizard.doneHint")}</p>
    </div>
  );
}
