import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { AlertTriangle, Check, CheckCircle2, Copy, ExternalLink, RefreshCw, ShieldCheck, XCircle } from "lucide-react";
import { api } from "@/api";
import type { AppSetupChoice, AppSetupEvent, AppSetupPlan, AppSetupResult, AppSetupStart, AccountInput } from "@/api";
import { DOCS_URL } from "@/config";
import { useI18n, type TFn } from "@/i18n";
import { useToast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Field, Spinner, fieldAria } from "@/components/common";
import { BrowserPrompt, DevicePrompt } from "@/components/signin";
import { actionMsg, authOfIdentity, identityOfAuth, newSetupId, progressMsg, type AutoIdentity, type Msg } from "@/lib/appsetup";
import { cn, copyText, errMsg } from "@/lib/utils";

export type AdminMethod = "device_code" | "browser";
export type FieldErrors = Partial<Record<string, string>>;

/** A radio card: the look of the method list of the wizard. */
export function ChoiceCard({ selected, onSelect, title, hint, badge, disabled }: {
  selected: boolean; onSelect: () => void; title: string; hint: string; badge?: ReactNode; disabled?: boolean;
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      disabled={disabled}
      onClick={onSelect}
      className={cn(
        "flex cursor-pointer items-start gap-3 rounded-lg border p-3.5 text-left transition-colors hover:bg-accent/50 disabled:cursor-not-allowed disabled:opacity-45",
        selected && "border-primary bg-accent/60 ring-1 ring-primary",
      )}
    >
      <span className={cn("mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border", selected ? "border-primary bg-primary text-primary-foreground" : "border-input")} aria-hidden>
        {selected && <Check className="size-3" strokeWidth={3} />}
      </span>
      <span className="min-w-0">
        <span className="flex items-center gap-2 font-medium">
          {title}
          {badge}
        </span>
        <span className="mt-0.5 block text-[13px] text-muted-foreground">{hint}</span>
      </span>
    </button>
  );
}

const msg = (t: TFn, m: Msg | null, fallback: string) => (m ? t(m.key, m.params) : fallback);

/** First step of the automatic path: name, identity and how the administrator signs in. */
export function AutoStartForm({ input, method, errors, locked, onChange, onMethod }: {
  input: AccountInput; method: AdminMethod; errors: FieldErrors; locked: boolean;
  onChange: (patch: Partial<AccountInput>) => void; onMethod: (m: AdminMethod) => void;
}) {
  const { t } = useI18n();
  const identity = identityOfAuth(input.auth);
  const [advanced, setAdvanced] = useState(identity === "app");
  const err = (f: string) => (errors[f] ? t(errors[f] === "name" ? "wizard.err.name" : "wizard.err.required") : undefined);
  const pick = (i: AutoIdentity) => onChange({ auth: authOfIdentity(i) });
  return (
    <div className="mt-4 grid gap-4">
      <Field id="a-name" label={t("wizard.name")} hint={t("wizard.nameHint")} error={err("name")}>
        <Input {...fieldAria("a-name", err("name"), "x")} value={input.name} disabled={locked} maxLength={32} onChange={(e) => onChange({ name: e.target.value })} autoComplete="off" />
      </Field>

      <div>
        <p className="mb-1.5 text-[13px] font-medium">{t("auto.identity")}</p>
        <div role="radiogroup" aria-label={t("auto.identity")} className="grid gap-2">
          {(["device_code", "browser"] as const).map((k) => (
            <ChoiceCard
              key={k}
              selected={identity === k}
              disabled={locked}
              onSelect={() => pick(k)}
              title={t(`auto.id.${k}`)}
              hint={t(`auto.id.${k}.hint`)}
              badge={k === "device_code" && <span className="rounded bg-info-bg px-1.5 py-px text-[11px] font-medium text-info">{t("wizard.recommended")}</span>}
            />
          ))}
          <button type="button" className="w-fit cursor-pointer text-xs text-primary underline-offset-2 hover:underline" aria-expanded={advanced} onClick={() => setAdvanced((a) => !a)}>
            {t("auto.advanced")} {advanced ? "▴" : "▾"}
          </button>
          {advanced && (
            <ChoiceCard selected={identity === "app"} disabled={locked} onSelect={() => pick("app")} title={t("auto.id.app")} hint={t("auto.id.app.hint")} />
          )}
        </div>
      </div>

      {identity === "app" && (
        <Field id="a-user" label={t("wizard.usernameApp")} hint={t("wizard.usernameAppHint")} error={err("username")}>
          <Input {...fieldAria("a-user", err("username"), "x")} value={input.username} disabled={locked} onChange={(e) => onChange({ username: e.target.value })} placeholder="user@contoso.onmicrosoft.com" spellCheck={false} />
        </Field>
      )}

      <div>
        <p className="mb-1.5 text-[13px] font-medium">{t("auto.method")}</p>
        <div className="flex gap-2" role="radiogroup" aria-label={t("auto.method")}>
          {(["device_code", "browser"] as const).map((m) => (
            <Button key={m} type="button" role="radio" aria-checked={method === m} size="sm" variant={method === m ? "default" : "outline"} onClick={() => onMethod(m)}>
              {t(`auto.method.${m}`)}
            </Button>
          ))}
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">{t("auto.methodHint")}</p>
      </div>

      <div className="space-y-1.5 rounded-lg border bg-info-bg/60 p-3 text-[13px]">
        <p className="flex items-start gap-2">
          <ShieldCheck className="mt-0.5 size-4 shrink-0 text-info" aria-hidden />
          <span>{t("auto.msTool")}</span>
        </p>
        <p className="pl-6 text-xs text-muted-foreground">{t(identity === "app" ? "auto.roles.app" : "auto.roles.delegated")}</p>
      </div>
    </div>
  );
}

/** The link an administrator opens to grant consent, with buttons to open and copy it. */
export function ConsentNotice({ url, className }: { url: string; className?: string }) {
  const { t } = useI18n();
  const toast = useToast();
  return (
    <div role="status" className={cn("rounded-lg border border-warning/50 bg-warning-bg p-3.5 text-left text-[13px]", className)}>
      <p className="flex items-center gap-2 font-medium">
        <AlertTriangle className="size-4 shrink-0 text-warning" aria-hidden /> {t("auto.consent.title")}
      </p>
      <p className="mt-1.5 text-foreground/80">{t("auto.consent.body")}</p>
      <p className="mt-2 rounded border bg-card/70 px-2 py-1.5 font-mono text-xs break-all select-all">{url}</p>
      <div className="mt-2 flex gap-2">
        <Button size="sm" onClick={() => void api.system.openExternal(url)}>
          <ExternalLink /> {t("auto.consent.open")}
        </Button>
        <Button size="sm" variant="outline" onClick={() => void copyText(url).then((ok) => ok && toast.success(t("wizard.copied")))}>
          <Copy /> {t("auto.consent.copy")}
        </Button>
      </div>
    </div>
  );
}

type Phase = "starting" | "signin" | "planning" | "plan" | "applying" | "done" | "error";

interface Failure { code: string; message: string; roles: string[] }

/**
 * The second step of the automatic path: administrator sign-in, the plan, the
 * confirmation and the progress. Leaving the step before it ends cancels the session.
 */
export function AppSetupStep({ input, method, done, onResult, onManual, onRestart, onAbort }: {
  input: AccountInput; method: AdminMethod; done: AppSetupResult | null;
  onResult: (r: AppSetupResult, sessionId: string) => void; onManual: () => void; onRestart: () => void; onAbort: () => void;
}) {
  const { t } = useI18n();
  const [phase, setPhase] = useState<Phase>(done ? "done" : "starting");
  const [ev, setEv] = useState<AppSetupEvent | null>(null);
  const [plan, setPlan] = useState<AppSetupPlan | null>(null);
  const [lines, setLines] = useState<string[]>([]);
  const [fail, setFail] = useState<Failure | null>(null);
  const [adopt, setAdopt] = useState(""); // "" = create new
  const [rename, setRename] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<AppSetupResult | null>(done);
  const id = useRef("");
  const phaseRef = useRef<Phase>(phase);
  phaseRef.current = phase;

  useEffect(() => {
    if (done) return;
    const sid = newSetupId();
    id.current = sid;
    let alive = true;
    const failWith = (f: Failure) => {
      setFail(f);
      setPhase("error");
    };
    const off = api.appSetup.onEvent(sid, (e) => {
      if (!alive) return;
      switch (e.kind) {
        case "device_code":
        case "browser_opened":
          setEv(e);
          setPhase("signin");
          break;
        case "signed_in":
          setPhase("planning");
          break;
        case "plan":
          if (e.plan) {
            setPlan(e.plan);
            setAdopt(e.plan.needsChoice ? (e.plan.candidates[0]?.clientId ?? "") : "");
            setPhase("plan");
          }
          break;
        case "progress":
          setLines((l) => (l[l.length - 1] === e.message ? l : [...l, e.message]));
          break;
        case "result":
          if (e.result) {
            setResult(e.result);
            setPhase("done");
            onResult(e.result, sid);
          }
          break;
        case "error":
          if (e.code !== "cancelled") failWith({ code: e.code ?? "other", message: e.message, roles: e.roles ?? [] });
          break;
      }
    });
    const start: AppSetupStart = {
      name: input.name.trim(), appOnly: input.auth === "client_secret", auth: input.auth === "browser" ? "browser" : "device_code",
      username: input.username.trim(), method,
    };
    api.appSetup.begin(sid, start).catch((ex) => alive && failWith({ code: "other", message: errMsg(ex), roles: [] }));
    return () => {
      alive = false;
      off();
      // Leaving before the end abandons the session; nothing half-made is kept.
      if (phaseRef.current !== "done" && phaseRef.current !== "error") void api.appSetup.cancel(sid).catch(() => undefined);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const confirm = async () => {
    if (!plan) return;
    const choice: AppSetupChoice = { adopt: plan.needsChoice ? adopt : "", createNew: plan.needsChoice && !adopt, rename: plan.needsChoice && !!adopt && rename };
    setSubmitting(true);
    try {
      await api.appSetup.apply(id.current, choice);
      setPhase("applying");
    } catch (ex) {
      // The session would stay open at the plan; end it so a retry can start a new one.
      void api.appSetup.cancel(id.current).catch(() => undefined);
      setFail({ code: "other", message: errMsg(ex), roles: [] });
      setPhase("error");
    }
    setSubmitting(false);
  };

  const body = (() => {
    if (phase === "done" && result) return <DoneView result={result} />;
    if (phase === "error" && fail) return <FailView fail={fail} onManual={onManual} onRestart={onRestart} />;
    if (phase === "signin" && ev) {
      return (
        <div className="flex min-h-[18rem] flex-col items-center justify-center gap-4 text-center" aria-live="polite">
          <p className="text-base font-medium">{t("auto.signinTitle")}</p>
          {ev.kind === "device_code" ? <DevicePrompt message={ev.message} userCode={ev.userCode} verificationUri={ev.verificationUri} /> : <BrowserPrompt />}
          <p className="max-w-md text-xs text-muted-foreground">{t("auto.msTool")}</p>
          <Button variant="ghost" size="sm" onClick={onAbort}>{t("common.cancel")}</Button>
        </div>
      );
    }
    if (phase === "plan" && plan) {
      return <PlanView id={id.current} plan={plan} adopt={adopt} rename={rename} submitting={submitting} onAdopt={setAdopt} onRename={setRename} onConfirm={() => void confirm()} onCancel={onAbort} />;
    }
    if (phase === "applying") return <ProgressView lines={lines} />;
    return (
      <div className="flex min-h-[18rem] flex-col items-center justify-center gap-3" role="status">
        <Spinner className="size-8 text-primary" />
        <p className="text-[13px] text-muted-foreground">{phase === "planning" ? t("auto.planning") : t("auto.starting")}</p>
        <Button variant="ghost" size="sm" onClick={onAbort}>{t("common.cancel")}</Button>
      </div>
    );
  })();
  return body;
}

function PlanView({ id, plan, adopt, rename, submitting, onAdopt, onRename, onConfirm, onCancel }: {
  id: string; plan: AppSetupPlan; adopt: string; rename: boolean; submitting: boolean;
  onAdopt: (id: string) => void; onRename: (r: boolean) => void; onConfirm: () => void; onCancel: () => void;
}) {
  const { t } = useI18n();
  // With candidates the steps depend on the selected option; the backend works them out without writing anything.
  const [shown, setShown] = useState<AppSetupPlan>(plan);
  const [loading, setLoading] = useState(plan.needsChoice);
  const [previewError, setPreviewError] = useState("");
  const seq = useRef(0);
  const adopting = adopt !== "";
  const renaming = adopting && rename;
  useEffect(() => {
    if (!plan.needsChoice) return;
    const n = ++seq.current;
    setLoading(true);
    setPreviewError("");
    api.appSetup
      .preview(id, { adopt, createNew: !adopting, rename: renaming })
      .then((p) => {
        if (n !== seq.current) return;
        setShown(p);
        setLoading(false);
      })
      .catch((ex) => {
        if (n !== seq.current) return;
        setPreviewError(errMsg(ex));
        setLoading(false);
      });
  }, [id, plan.needsChoice, adopt, adopting, renaming]);

  const missing = useMemo(() => new Set(shown.missing), [shown.missing]);
  const partial = missing.size > 0 && missing.size < shown.permissions.length;
  const adoptName = plan.candidates.find((c) => c.clientId === adopt)?.displayName;
  const nothing = !loading && !previewError && shown.actions.length === 0;
  return (
    <div className="space-y-4">
      <p className="text-base font-medium">{t("auto.planTitle")}</p>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-[13px]">
        <dt className="text-muted-foreground">{t("auto.planName")}</dt>
        <dd className="font-medium">{adoptName && !renaming ? adoptName : plan.existing ? plan.existing.displayName : plan.displayName}</dd>
        <dt className="text-muted-foreground">{t("auto.planTenant")}</dt>
        <dd className="font-mono text-xs break-all">{plan.tenantId}</dd>
      </dl>

      <div>
        <p className="mb-1.5 text-[13px] font-medium">
          {t("auto.planPerms", { n: plan.permissions.length })}
          {partial && <span className="ml-2 font-normal text-muted-foreground">{t("auto.planPermsNew", { n: missing.size })}</span>}
        </p>
        <ul className="flex max-h-28 flex-wrap gap-1.5 overflow-y-auto">
          {plan.permissions.map((p) => (
            <li key={p} className={cn("rounded border px-1.5 py-0.5 font-mono text-xs", partial && missing.has(p) ? "border-primary/50 bg-accent/60" : "bg-muted/50")}>
              {p}
            </li>
          ))}
        </ul>
        <p className="mt-1.5 text-xs text-muted-foreground">{t("auto.planKeep")}</p>
      </div>

      {plan.needsChoice && (
        <div>
          <p className="text-[13px] font-medium">{t("auto.choose")}</p>
          <p className="mb-2 text-xs text-muted-foreground">{t("auto.chooseHint")}</p>
          <div role="radiogroup" aria-label={t("auto.choose")} className="grid gap-2">
            {plan.candidates.map((c) => (
              <ChoiceCard key={c.clientId} selected={adopt === c.clientId} onSelect={() => onAdopt(c.clientId)} title={t("auto.adopt", { name: c.displayName })} hint={t("auto.clientIdIs", { id: c.clientId })} />
            ))}
            <ChoiceCard selected={adopt === ""} onSelect={() => onAdopt("")} title={t("auto.createNew")} hint={plan.displayName} />
          </div>
          {adopting && (
            <label className="mt-2.5 flex cursor-pointer items-center gap-2.5 text-[13px]">
              <Checkbox checked={rename} onCheckedChange={(v) => onRename(v === true)} />
              {t("auto.rename", { name: plan.displayName })}
            </label>
          )}
        </div>
      )}

      <div aria-live="polite">
        <p className="mb-1.5 text-[13px] font-medium">{t("auto.planActions")}</p>
        {loading ? (
          <p className="flex items-center gap-2 text-[13px] text-muted-foreground">
            <Spinner /> {t("auto.previewing")}
          </p>
        ) : previewError ? (
          <p role="alert" className="text-[13px] text-danger">{t("auto.previewFailed", { message: previewError })}</p>
        ) : nothing ? (
          <p className="flex items-center gap-2 text-[13px]">
            <CheckCircle2 className="size-4 text-success" aria-hidden /> {t("auto.planNone")}
          </p>
        ) : (
          <ul className="list-disc space-y-0.5 pl-5 text-[13px]">
            {shown.actions.map((a) => (
              <li key={a.code}>{msg(t, actionMsg(a, shown, adoptName), a.text)}</li>
            ))}
          </ul>
        )}
      </div>

      <div className="flex gap-2">
        <Button onClick={onConfirm} disabled={submitting || loading || !!previewError}>
          {submitting && <Spinner />}
          {nothing ? t("auto.confirmNone") : t("auto.confirm")}
        </Button>
        <Button variant="ghost" onClick={onCancel} disabled={submitting}>{t("common.cancel")}</Button>
      </div>
    </div>
  );
}

function ProgressView({ lines }: { lines: string[] }) {
  const { t } = useI18n();
  return (
    <div className="flex min-h-[18rem] flex-col items-center justify-center gap-4" aria-live="polite">
      <p className="text-base font-medium">{t("auto.applying")}</p>
      <ul className="w-full max-w-md space-y-1.5 text-[13px]">
        {lines.map((l, i) => {
          const last = i === lines.length - 1;
          return (
            <li key={i} className={cn("flex items-start gap-2", !last && "text-muted-foreground")}>
              {last ? <Spinner className="mt-0.5 size-3.5 shrink-0" /> : <Check className="mt-0.5 size-3.5 shrink-0 text-success" aria-hidden />}
              <span className="break-words">{msg(t, progressMsg(l), l)}</span>
            </li>
          );
        })}
        {lines.length === 0 && (
          <li className="flex items-center gap-2 text-muted-foreground">
            <Spinner className="size-3.5" /> {t("auto.starting")}
          </li>
        )}
      </ul>
    </div>
  );
}

function DoneView({ result }: { result: AppSetupResult }) {
  const { t } = useI18n();
  const what = result.createdApp
    ? t("auto.done.created", { name: result.displayName })
    : result.adoptedApp
      ? t(result.renamed ? "auto.done.adoptedRenamed" : "auto.done.adopted", { name: result.displayName })
      : t("auto.done.reused", { name: result.displayName });
  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <CheckCircle2 className="size-9 shrink-0 text-success" aria-hidden />
        <div>
          <p className="text-base font-medium">{t("auto.doneTitle")}</p>
          <p className="text-[13px] text-muted-foreground">{what}</p>
        </div>
      </div>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-[13px]">
        <dt className="text-muted-foreground">{t("auto.done.clientId")}</dt>
        <dd className="font-mono text-xs break-all select-all">{result.clientId}</dd>
        <dt className="text-muted-foreground">{t("auto.done.tenantId")}</dt>
        <dd className="font-mono text-xs break-all select-all">{result.tenantId}</dd>
      </dl>
      {result.consentGranted ? (
        <p className="flex items-center gap-2 text-[13px]">
          <CheckCircle2 className="size-4 text-success" aria-hidden /> {t("auto.done.consentOk")}
        </p>
      ) : (
        <ConsentNotice url={result.adminConsentUrl} />
      )}
      {result.secretCreated && <p className="text-[13px] text-muted-foreground">{t("auto.done.secret", { date: result.secretExpires })}</p>}
      {result.removedSecrets > 0 && <p className="text-[13px] text-muted-foreground">{t("auto.done.removedSecrets", { n: result.removedSecrets })}</p>}
      {result.notRemovedSecrets > 0 && (
        <p className="flex items-start gap-2 text-[13px] text-warning">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden /> {t("auto.done.notRemovedSecrets", { n: result.notRemovedSecrets })}
        </p>
      )}
      <p className="text-[13px] text-muted-foreground">{t("auto.done.next")}</p>
    </div>
  );
}

function FailView({ fail, onManual, onRestart }: { fail: Failure; onManual: () => void; onRestart: () => void }) {
  const { t } = useI18n();
  const forbidden = fail.code === "forbidden";
  const blocked = fail.code === "blocked";
  return (
    <div className="flex min-h-[18rem] flex-col items-center justify-center gap-3 text-center" role="alert">
      <XCircle className="size-12 text-danger" aria-hidden />
      <p className="text-base font-medium">{t(forbidden ? "auto.err.forbidden" : blocked ? "auto.err.blocked" : "auto.err.failed")}</p>
      <p className="max-w-md text-[13px] break-words text-muted-foreground">
        {forbidden ? t("auto.err.forbiddenBody", { roles: fail.roles.join(", ") }) : blocked ? t("auto.err.blockedBody") : fail.message}
      </p>
      <p className="max-w-md text-xs text-muted-foreground">{t("auto.err.kept")}</p>
      {(forbidden || blocked) && (
        <button type="button" className="cursor-pointer text-[13px] text-primary underline-offset-2 hover:underline" onClick={() => void api.system.openExternal(DOCS_URL)}>
          {t("overview.appDocs")} <ExternalLink className="inline size-3" aria-hidden />
        </button>
      )}
      <div className="flex flex-wrap justify-center gap-2">
        <Button variant="outline" onClick={onRestart}>
          <RefreshCw /> {t(forbidden ? "auto.otherAdmin" : "wizard.retry")}
        </Button>
        <Button variant={forbidden || blocked ? "default" : "outline"} onClick={onManual}>{t("auto.switchManual")}</Button>
      </div>
    </div>
  );
}
