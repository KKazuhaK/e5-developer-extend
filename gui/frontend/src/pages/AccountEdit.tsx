import { useCallback, useEffect, useRef, useState } from "react";
import { CheckCircle2, ExternalLink, LogIn } from "lucide-react";
import { api } from "@/api";
import type { AccountInput } from "@/api";
import { DOCS_URL } from "@/config";
import { useI18n } from "@/i18n";
import { useApp } from "@/state/app";
import { useToast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { ErrorBlock, Field, LoadingBlock, PageHeader, Spinner, fieldAria } from "@/components/common";
import { EditSaveError, needsSecret, needsUsername, saveAccountEdit } from "@/lib/account";
import type { AccountErrorCode } from "@/lib/account";
import { preselect } from "@/lib/recommend";
import { errMsg } from "@/lib/utils";
import { CategoriesStep, type RecState } from "./AccountSteps";
import { SignInDialog } from "./AccountDialogs";

/**
 * Edits an existing account on one page: every editable field at once, one Save. The name may change (rename
 * first, then the update). Signing in and testing are separate actions, not steps here.
 */
export function AccountEdit({ name, onClose }: { name: string; onClose: () => void }) {
  const { t } = useI18n();
  const { snapshot } = useApp();
  const toast = useToast();
  // The account as stored; after a rename that went through it carries the new name.
  const [original, setOriginal] = useState<AccountInput | null>(null);
  const [input, setInput] = useState<AccountInput | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [errors, setErrors] = useState<Partial<Record<string, AccountErrorCode>>>({});
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [wasPaused, setWasPaused] = useState(false);
  const [paused, setPaused] = useState(false);
  const [cleared, setCleared] = useState<string | null>(null); // the name to sign in, once the sign-in was discarded
  const [signIn, setSignIn] = useState<string | null>(null);

  const [allCategories, setAllCategories] = useState<string[] | null>(null);
  const [defaultCategories, setDefaultCategories] = useState<string[]>([]);
  const [catError, setCatError] = useState<string | null>(null);
  const [rec, setRec] = useState<RecState | null>(null);
  const probeSeq = useRef(0);

  useEffect(() => {
    let alive = true;
    api.accounts
      .get(name)
      .then((a) => {
        if (!alive) return;
        setOriginal({ ...a, secret: undefined });
        setInput({ ...a, secret: undefined });
      })
      .catch((e) => alive && setLoadError(errMsg(e)));
    return () => {
      alive = false;
    };
  }, [name]);

  // Paused is a property of the live account, not of the stored input.
  const summary = snapshot.data?.accounts.find((a) => a.name === (original?.name ?? name));
  const pausedSeen = useRef(false);
  useEffect(() => {
    if (summary && !pausedSeen.current) {
      pausedSeen.current = true;
      setWasPaused(summary.paused);
      setPaused(summary.paused);
    }
  }, [summary]);

  const loadCategories = useCallback(() => {
    setCatError(null);
    Promise.all([api.catalog.categories(), api.catalog.defaultCategories()])
      .then(([all, dflt]) => {
        setAllCategories(all);
        setDefaultCategories(dflt);
      })
      .catch((e) => setCatError(errMsg(e)));
  }, []);
  useEffect(() => loadCategories(), [loadCategories]);

  const probe = useCallback(async () => {
    if (!allCategories || !original) return;
    const seq = ++probeSeq.current;
    setRec({ phase: "run", result: null, message: "" });
    const fallback = () => setInput((i) => (i && !i.categories.length ? { ...i, categories: defaultCategories.filter((c) => allCategories.includes(c)) } : i));
    try {
      const result = await api.accounts.recommend(original.name);
      if (seq !== probeSeq.current) return;
      if (result.needsLogin) {
        setRec({ phase: "login", result: null, message: "" });
        fallback();
      } else {
        setRec({ phase: "ok", result, message: "" });
        // An account with no explicit choice starts from the recommendation; an existing choice is kept and compared.
        setInput((i) => (i && !i.categories.length ? { ...i, categories: preselect(allCategories, result, []) } : i));
      }
    } catch (e) {
      if (seq !== probeSeq.current) return;
      setRec({ phase: "fail", result: null, message: errMsg(e) });
      fallback();
    }
  }, [allCategories, defaultCategories, original]);
  useEffect(() => {
    if (original && allCategories && !rec) void probe();
  }, [original, allCategories, rec, probe]);

  const set = <K extends keyof AccountInput>(k: K, v: AccountInput[K]) => {
    setInput((i) => (i ? { ...i, [k]: v } : i));
    setErrors((e) => ({ ...e, [k]: undefined }));
  };

  const err = (f: string): string | undefined => {
    const c = errors[f];
    if (!c) return undefined;
    return c === "guid" ? t("wizard.err.guid") : c === "name" ? t("edit.nameErr") : c === "date" ? t("wizard.err.date") : t("wizard.err.required");
  };

  const save = async () => {
    if (!original || !input || saving) return;
    setSaveError(null);
    setSaving(true);
    try {
      const out = await saveAccountEdit(api.accounts, original, input, { was: wasPaused, now: paused }, defaultCategories);
      if ("errors" in out) {
        setErrors(out.errors);
        setSaveError(t("edit.fixErrors"));
        return;
      }
      void snapshot.refresh();
      const { result } = out;
      if (result.warnings.length) toast.error(t("edit.renameWarnings", { name: result.name, detail: result.warnings.join("; ") }));
      if (result.signInCleared) {
        setCleared(result.name);
        return;
      }
      toast.success(t("edit.saved", { name: result.name }));
      onClose();
    } catch (e) {
      if (e instanceof EditSaveError && e.stage === "paused") {
        // Renamed and updated; only the pause switch failed. Carry on from what is stored now.
        setOriginal({ ...input, name: e.account, secret: undefined });
        setInput((i) => (i ? { ...i, name: e.account } : i));
        setSaveError(t("edit.pausedFailed", { detail: e.message }));
        void snapshot.refresh();
      } else if (e instanceof EditSaveError && e.renamed) {
        // The rename went through: carry on under the new name so a second try does not rename again.
        setOriginal((o) => (o ? { ...o, name: e.account } : o));
        setSaveError(t("edit.renamedOnly", { old: original.name, name: e.account, detail: e.message }));
        void snapshot.refresh();
      } else {
        setSaveError(errMsg(e));
      }
      setInput((i) => (i ? { ...i, secret: undefined } : i)); // never keep the secret around after a failed save
    } finally {
      setSaving(false);
    }
  };

  const title = t("edit.title", { name: original?.name ?? name });
  if (loadError) {
    return (
      <>
        <PageHeader title={title} />
        <ErrorBlock message={loadError} onRetry={onClose} />
      </>
    );
  }
  if (!input || !original) {
    return (
      <>
        <PageHeader title={title} />
        <LoadingBlock rows={2} />
      </>
    );
  }

  if (cleared) {
    return (
      <>
        <PageHeader title={title} />
        <Card className="flex flex-col items-center gap-3 p-8 text-center" role="status">
          <CheckCircle2 className="size-12 text-warning" aria-hidden />
          <p className="text-base font-medium">{t("edit.clearedTitle", { name: cleared })}</p>
          <p className="max-w-md text-[13px] text-muted-foreground">{t("edit.clearedBody")}</p>
          <div className="flex gap-2">
            <Button onClick={() => setSignIn(cleared)}>
              <LogIn /> {t("edit.signInNow")}
            </Button>
            <Button variant="outline" onClick={onClose}>{t("edit.later")}</Button>
          </div>
        </Card>
        <SignInDialog name={signIn} onClose={() => setSignIn(null)} />
      </>
    );
  }

  const isPwd = input.auth === "password";
  return (
    <>
      <PageHeader title={title} description={t("edit.subtitle")} actions={<Button variant="ghost" onClick={onClose} disabled={saving}>{t("common.cancel")}</Button>} />
      <form
        autoComplete="off"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
        className="grid gap-4"
      >
        <Card className="grid gap-4 p-5 sm:grid-cols-2">
          <Field id="e-name" label={t("wizard.name")} hint={t("edit.nameHint")} error={err("name")} className="sm:col-span-2">
            <Input {...fieldAria("e-name", err("name"), "x")} value={input.name} maxLength={64} onChange={(e) => set("name", e.target.value)} autoComplete="off" spellCheck={false} />
          </Field>
          <div className="space-y-1.5 sm:col-span-2">
            <p className="text-[13px] leading-none font-medium">{t("edit.authLabel")}</p>
            <p className="text-sm">{t(`auth.${input.auth}`)}</p>
            <p className="text-xs text-muted-foreground">{t("edit.authFixed")}</p>
          </div>
          <Field id="e-tenant" label={t("wizard.tenant")} hint={t("wizard.tenantHint")} error={err("tenant")}>
            <Input {...fieldAria("e-tenant", err("tenant"), "x")} value={input.tenant} onChange={(e) => set("tenant", e.target.value)} placeholder="contoso.onmicrosoft.com" spellCheck={false} />
          </Field>
          <Field id="e-client" label="Client ID" hint={t("wizard.clientHint")} error={err("clientId")}>
            <Input {...fieldAria("e-client", err("clientId"), "x")} value={input.clientId} onChange={(e) => set("clientId", e.target.value)} placeholder="00000000-0000-0000-0000-000000000000" className="font-mono" spellCheck={false} />
          </Field>
          {needsUsername(input.auth) && (
            <Field id="e-user" label={t(isPwd ? "wizard.usernamePwd" : "wizard.usernameApp")} hint={t(isPwd ? "wizard.usernamePwdHint" : "wizard.usernameAppHint")} error={err("username")}>
              <Input {...fieldAria("e-user", err("username"), "x")} value={input.username} onChange={(e) => set("username", e.target.value)} placeholder="user@contoso.onmicrosoft.com" spellCheck={false} />
            </Field>
          )}
          {needsSecret(input.auth) && (
            <Field id="e-secret" label={t(isPwd ? "wizard.password" : "wizard.secret")} hint={t("wizard.secretKeepHint")} error={err("secret")}>
              <Input {...fieldAria("e-secret", err("secret"), "x")} type="password" autoComplete="new-password" placeholder={t("edit.secretPlaceholder")} value={input.secret ?? ""} onChange={(e) => set("secret", e.target.value)} />
            </Field>
          )}
          {input.auth === "client_secret" && (
            <Field id="e-exp" label={t("wizard.secretExpires")} hint={t("wizard.secretExpiresHint")} error={err("secretExpires")}>
              <Input {...fieldAria("e-exp", err("secretExpires"), "x")} type="date" value={input.secretExpires} onChange={(e) => set("secretExpires", e.target.value)} />
            </Field>
          )}
          <p className="text-xs text-muted-foreground sm:col-span-2">
            {t("wizard.needRegistration")}{" "}
            <button type="button" className="cursor-pointer text-primary underline-offset-2 hover:underline" onClick={() => void api.system.openExternal(DOCS_URL)}>
              {t("overview.appDocs")} <ExternalLink className="inline size-3" aria-hidden />
            </button>
          </p>
        </Card>

        <Card className="p-5">
          <h3 className="mb-3 text-sm font-semibold">{t("edit.apis")}</h3>
          <CategoriesStep
            categories={allCategories}
            error={catError}
            onReload={loadCategories}
            selected={input.categories}
            onChange={(c) => set("categories", c)}
            rec={rec}
            onRetry={() => void probe()}
            invalid={!!errors.categories}
          />
        </Card>

        {summary && (
          <Card className="flex items-center justify-between gap-4 p-5">
            <div>
              <label htmlFor="e-paused" className="text-sm font-semibold">{t("edit.paused")}</label>
              <p className="text-xs text-muted-foreground">{t("edit.pausedHint")}</p>
            </div>
            <Switch id="e-paused" checked={paused} onCheckedChange={setPaused} />
          </Card>
        )}

        {saveError && <ErrorBlock message={saveError} />}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onClose} disabled={saving}>{t("common.cancel")}</Button>
          <Button type="submit" disabled={saving || (!!allCategories && !input.categories.length && rec?.phase === "run")}>
            {saving && <Spinner />} {t(saving ? "edit.saving" : "edit.save")}
          </Button>
        </div>
      </form>
    </>
  );
}
