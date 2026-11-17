import { useEffect, useMemo, useState } from "react";
import { CheckCircle2, FlaskConical, KeyRound, LogIn, Pause, Pencil, Play, Plus, RefreshCw, Search, Trash2, Users, XCircle } from "lucide-react";
import { api } from "@/api";
import type { AccountSummary } from "@/api";
import { useApp, type AccountFilter } from "@/state/app";
import { useAccountActions } from "@/state/actions";
import { useI18n } from "@/i18n";
import { useToast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { EmptyBlock, ErrorBlock, Field, LoadingBlock, PageHeader, Spinner, StatusBadge, fieldAria } from "@/components/common";
import { SecretExpiry } from "@/components/AccountCard";
import { SignInFrequencyNotice } from "@/components/SignInFrequencyNotice";
import { AccountWizard } from "./AccountWizard";
import { AccountEdit } from "./AccountEdit";
import { SignInDialog, TestDialog } from "./AccountDialogs";
import { compareStatus, displayStatus, needsAction } from "@/lib/status";
import { formatRelative } from "@/lib/time";
import { isSignInFrequency, needsSecret } from "@/lib/account";
import { isValidDate } from "@/lib/settings";
import { cn, errMsg } from "@/lib/utils";

export function Accounts() {
  const { t, lang } = useI18n();
  const { route, snapshot, navigate } = useApp();
  const act = useAccountActions();
  const toast = useToast();
  const [wizard, setWizard] = useState(route.wizard ?? null);
  const [filter, setFilter] = useState<AccountFilter>(route.filter ?? "all");
  const [query, setQuery] = useState("");
  const [deleting, setDeleting] = useState<AccountSummary | null>(null);
  const [secretFor, setSecretFor] = useState<AccountSummary | null>(null);
  const [signIn, setSignIn] = useState<string | null>(route.signIn ?? null);
  const [testFor, setTestFor] = useState<string | null>(null);

  useEffect(() => {
    setSignIn(route.signIn ?? null);
    setWizard(route.wizard ?? null);
    setFilter(route.filter ?? "all");
  }, [route]);

  const snap = snapshot.data;
  const all = useMemo(() => [...(snap?.accounts ?? [])].sort((a, b) => compareStatus(a.status, b.status) || a.name.localeCompare(b.name)), [snap]);
  const counts = { all: all.length, attention: all.filter((a) => needsAction(a.status)).length, paused: all.filter((a) => a.paused).length };
  const shown = all.filter((a) => {
    if (filter === "attention" && !needsAction(a.status)) return false;
    if (filter === "paused" && !a.paused) return false;
    const q = query.trim().toLowerCase();
    return !q || a.name.toLowerCase().includes(q) || a.tenant.toLowerCase().includes(q);
  });

  if (wizard) {
    const close = () => {
      setWizard(null);
      void snapshot.refresh();
    };
    return wizard.mode === "edit" ? (
      <AccountEdit key={route.nonce} name={wizard.name} onClose={close} />
    ) : (
      <AccountWizard key={route.nonce} from={wizard.from} onClose={close} />
    );
  }

  const confirmDelete = async () => {
    if (!deleting) return;
    try {
      await api.accounts.remove(deleting.name);
      toast.success(t("accounts.deleted", { name: deleting.name }));
      setDeleting(null);
      await snapshot.refresh();
    } catch (e) {
      toast.error(errMsg(e));
      setDeleting(null);
    }
  };

  const FILTERS: { id: AccountFilter; label: string }[] = [
    { id: "all", label: t("accounts.filterAll") },
    { id: "attention", label: t("accounts.filterAttention") },
    { id: "paused", label: t("accounts.filterPaused") },
  ];

  return (
    <>
      <PageHeader
        title={t("nav.accounts")}
        actions={
          <Button onClick={() => setWizard({ mode: "add" })}>
            <Plus /> {t("accounts.add")}
          </Button>
        }
      />

      {!snap ? (
        snapshot.error ? <ErrorBlock message={snapshot.error} onRetry={() => void snapshot.refresh()} /> : <LoadingBlock rows={3} />
      ) : all.length === 0 ? (
        <EmptyBlock icon={<Users />} title={t("overview.emptyTitle")} hint={t("overview.emptyHint")}>
          <Button onClick={() => setWizard({ mode: "add" })}>
            <Plus /> {t("accounts.add")}
          </Button>
        </EmptyBlock>
      ) : (
        <>
          {snapshot.error && <ErrorBlock className="mb-3" message={snapshot.error} onRetry={() => void snapshot.refresh()} />}
          <div className="mb-4 flex flex-wrap items-center gap-3">
            <div role="group" aria-label={t("accounts.filter")} className="inline-flex rounded-md border bg-card p-0.5">
              {FILTERS.map((f) => (
                <button
                  key={f.id}
                  type="button"
                  aria-pressed={filter === f.id}
                  onClick={() => setFilter(f.id)}
                  className={cn(
                    "cursor-pointer rounded-[5px] px-3 py-1 text-[13px] transition-colors",
                    filter === f.id ? "bg-accent font-medium text-accent-foreground" : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {f.label} <span className="tabular-nums opacity-70">{counts[f.id]}</span>
                </button>
              ))}
            </div>
            <div className="relative ml-auto w-56 max-w-full">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
              <Input className="pl-8" type="search" placeholder={t("accounts.search")} aria-label={t("accounts.search")} value={query} onChange={(e) => setQuery(e.target.value)} />
            </div>
          </div>

          {shown.length === 0 ? (
            <EmptyBlock icon={<Search />} title={t("accounts.noMatch")} />
          ) : (
            <ul className="grid gap-3">
              {shown.map((a) => {
                const busy = act.busy.has(a.name);
                const attention = needsAction(a.status);
                return (
                  <li key={a.name}>
                    <Card className={cn("p-4", a.status === "needs_attention" && "border-danger/50")}>
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0">
                          <div className="flex items-center gap-2">
                            <h3 className="truncate text-[15px] font-semibold">{a.name}</h3>
                            <StatusBadge account={a} />
                            <SecretExpiry date={a.secretExpires} />
                          </div>
                          <p className="mt-0.5 truncate text-xs text-muted-foreground" title={a.tenant}>
                            {t(`auth.${a.auth}`)} · {a.tenant}
                          </p>
                        </div>
                        <div className="flex shrink-0 items-center gap-0.5">
                          {attention ? (
                            <Button size="sm" onClick={() => setSignIn(a.name)}>
                              <LogIn /> {a.status === "not_signed_in" ? t("account.signIn") : t("account.reLogin")}
                            </Button>
                          ) : (
                            <IconBtn label={t("account.runNow")} disabled={a.status !== "ok" || busy} onClick={() => void act.runNow([a.name])}>
                              {busy ? <Spinner /> : <RefreshCw />}
                            </IconBtn>
                          )}
                          {a.paused ? (
                            <IconBtn label={t("account.resume")} disabled={busy} onClick={() => void act.resume(a.name)}><Play /></IconBtn>
                          ) : (
                            <IconBtn label={t("account.pause")} disabled={busy} onClick={() => void act.pause(a.name)}><Pause /></IconBtn>
                          )}
                          <IconBtn label={t("account.test")} disabled={attention} onClick={() => setTestFor(a.name)}><FlaskConical /></IconBtn>
                          <IconBtn label={t("common.edit")} onClick={() => setWizard({ mode: "edit", name: a.name })}><Pencil /></IconBtn>
                          {needsSecret(a.auth) && <IconBtn label={t("accounts.changeSecret")} onClick={() => setSecretFor(a)}><KeyRound /></IconBtn>}
                          <IconBtn label={t("common.delete")} onClick={() => setDeleting(a)} danger><Trash2 /></IconBtn>
                        </div>
                      </div>
                      {a.status !== "ok" && isSignInFrequency(a.reason) && (
                        <div className="mt-2"><SignInFrequencyNotice a={a} onSwitch={(n) => navigate("accounts", { wizard: { mode: "add", from: n } })} /></div>
                      )}
                      {a.status !== "ok" && (a.reason || a.fix) && !isSignInFrequency(a.reason) && (
                        <p className="mt-2 rounded-md bg-muted/60 px-3 py-1.5 text-[13px]">
                          {a.reason}
                          {a.fix && <span className="text-muted-foreground"> · {t("account.fixPrefix")}{a.fix}</span>}
                        </p>
                      )}
                      <div className="mt-2.5 flex flex-wrap gap-x-5 gap-y-1 text-[13px] tabular-nums text-muted-foreground">
                        <span>{t("account.lastSuccess")} <span className="text-foreground">{formatRelative(a.lastSuccess, lang)}</span></span>
                        <span className="flex items-center gap-2">
                          {t("account.today")}
                          <span className="flex items-center gap-1 text-foreground"><CheckCircle2 className="size-3.5 text-success" aria-hidden /><span className="sr-only">{t("account.todayOk")}</span>{a.todayOk}</span>
                          <span className={cn("flex items-center gap-1", a.todayFailed > 0 ? "font-medium text-danger" : "text-foreground")}><XCircle className="size-3.5" aria-hidden /><span className="sr-only">{t("account.todayFailed")}</span>{a.todayFailed}</span>
                        </span>
                        <span>{t("account.apis")} <span className="text-foreground">{displayStatus(a) === "waiting" ? "—" : `${a.usableApis}/${a.totalApis}`}</span></span>
                      </div>
                    </Card>
                  </li>
                );
              })}
            </ul>
          )}
        </>
      )}

      <Dialog open={!!deleting} onOpenChange={(o) => !o && setDeleting(null)}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle>{t("accounts.deleteTitle", { name: deleting?.name ?? "" })}</DialogTitle>
            <DialogDescription>{t("accounts.deleteBody")}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleting(null)}>{t("common.cancel")}</Button>
            <Button variant="destructive" onClick={() => void confirmDelete()}><Trash2 /> {t("accounts.deleteConfirm")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <SecretDialog account={secretFor} onClose={() => setSecretFor(null)} />
      <SignInDialog name={signIn} onClose={() => setSignIn(null)} />
      <TestDialog name={testFor} onClose={() => setTestFor(null)} />
    </>
  );
}

function IconBtn({ label, danger, children, ...rest }: { label: string; danger?: boolean } & React.ComponentProps<typeof Button>) {
  return (
    <Button variant="ghost" size="icon" title={label} aria-label={label} className={cn(danger && "text-danger hover:text-danger")} {...rest}>
      {children}
    </Button>
  );
}

function SecretDialog({ account, onClose }: { account: AccountSummary | null; onClose: () => void }) {
  const { t } = useI18n();
  const toast = useToast();
  const { snapshot } = useApp();
  const [secret, setSecret] = useState("");
  const [expires, setExpires] = useState("");
  const [orig, setOrig] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const name = account?.name;

  useEffect(() => {
    setSecret("");
    setError(null);
    setExpires("");
    if (!name) return;
    api.accounts.get(name).then((a) => { setExpires(a.secretExpires); setOrig(a.secretExpires); }).catch(() => undefined);
  }, [name]);

  const isPwd = account?.auth === "password";
  const dateBad = !!expires && !isValidDate(expires);

  const submit = async () => {
    if (!name || !secret || dateBad) return;
    setBusy(true);
    setError(null);
    try {
      await api.accounts.setSecret(name, secret);
      if (!isPwd && expires !== orig) {
        const cur = await api.accounts.get(name);
        await api.accounts.update({ ...cur, secretExpires: expires });
      }
      setSecret(""); // do not keep it around
      toast.success(t("accounts.secretSaved", { name }));
      void snapshot.refresh();
      onClose();
    } catch (e) {
      setError(errMsg(e));
      setSecret("");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={!!account} onOpenChange={(o) => { if (!o) { setSecret(""); onClose(); } }}>
      <DialogContent closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle>{t(isPwd ? "accounts.changePasswordTitle" : "accounts.changeSecretTitle", { name: name ?? "" })}</DialogTitle>
          <DialogDescription>{t("accounts.changeSecretBody")}</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          autoComplete="off"
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
        >
          <Field id="cs-secret" label={t(isPwd ? "wizard.password" : "wizard.secret")}>
            <Input {...fieldAria("cs-secret")} type="password" autoComplete="new-password" autoFocus value={secret} onChange={(e) => setSecret(e.target.value)} />
          </Field>
          {!isPwd && (
            <Field id="cs-exp" label={t("wizard.secretExpires")} error={dateBad ? t("wizard.err.date") : undefined} hint={t("wizard.secretExpiresHint")}>
              <Input {...fieldAria("cs-exp", dateBad ? "x" : undefined)} type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
            </Field>
          )}
          {error && <p role="alert" className="text-[13px] text-danger">{error}</p>}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" disabled={!secret || busy || dateBad}>{busy && <Spinner />} {t("common.save")}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
