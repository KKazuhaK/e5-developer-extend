import { useCallback, useEffect, useState } from "react";
import { Download, PackageOpen, Upload } from "lucide-react";
import { api } from "@/api";
import type { BackupNote, BackupProblem, ImportMode, ImportSummary } from "@/api";
import { useApp } from "@/state/app";
import { useI18n, type DictKey, type TFn } from "@/i18n";
import { useToast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Field, Spinner, fieldAria } from "@/components/common";
import { SHOW_DETAIL, checkExportPassphrase, modeChoice, noteKey, problemKey, partKey, type PassphraseCode } from "@/lib/backup";
import { errMsg } from "@/lib/utils";

const CODE_MSG: Record<PassphraseCode, DictKey> = {
  empty: "backup.err.empty", short: "backup.err.short", mismatch: "backup.err.mismatch",
};

/** The text of a backend problem; unknown codes fall back to the backend's English detail. */
function problemText(t: TFn, p: BackupProblem): string {
  const key = problemKey(p);
  if (!key) return p.detail || p.code;
  const text = t(key, { names: p.names.join(", ") });
  return SHOW_DETAIL.has(p.code) && p.detail ? `${text} (${p.detail})` : text;
}

function noteText(t: TFn, n: BackupNote): string {
  const key = noteKey(n);
  const parts = n.args.map((a) => { const k = partKey(a); return k ? t(k) : a; });
  return key ? t(key, { file: n.args[0] ?? "", names: n.code === "stats_not_imported" ? parts.join(", ") : n.args.join(", ") }) : n.code;
}

/** Export and import of the single-file backup (docs/user/backup.md). */
export function BackupCard({ onImported }: { onImported: () => void }) {
  const { t } = useI18n();
  const toast = useToast();
  const { snapshot } = useApp();
  const [hasData, setHasData] = useState<boolean | null>(null);
  const [probeFailed, setProbeFailed] = useState(false);
  const [confirmReplace, setConfirmReplace] = useState(false);
  const [busy, setBusy] = useState<"export" | "import" | null>(null);

  const [pass, setPass] = useState("");
  const [confirm, setConfirm] = useState("");
  const [withLogs, setWithLogs] = useState(false);
  const [showErr, setShowErr] = useState(false);

  const [importPass, setImportPass] = useState("");
  const [mode, setMode] = useState<Exclude<ImportMode, "default">>("merge");
  const [imported, setImported] = useState<ImportSummary | null>(null);

  const loadHasData = useCallback(() => {
    api.backup.targetHasData().then((v) => { setHasData(v); setProbeFailed(false); }).catch(() => { setHasData(null); setProbeFailed(true); });
  }, []);
  useEffect(loadHasData, [loadHasData]);

  const choice = modeChoice(hasData, probeFailed);
  const effectiveMode: ImportMode = choice === "default" ? "default" : choice === "replaceOnly" ? "replace" : mode;
  const localNames = snapshot.data?.accounts.map((a) => a.name) ?? [];
  const code = checkExportPassphrase(pass, confirm);
  const exportError = showErr && code ? t(CODE_MSG[code]) : undefined;

  const doExport = async () => {
    setShowErr(true);
    if (code) return;
    setBusy("export");
    try {
      const r = await api.backup.export(pass, withLogs, t("backup.exportDialog"));
      if (r.problem) {
        toast.error(problemText(t, r.problem));
      } else if (!r.cancelled) {
        const notes = r.summary.noteCodes.map((n) => noteText(t, n)).join(" ");
        toast.success(`${t("backup.exported", { n: r.summary.accounts.length })}. ${t("backup.exportedTo", { path: r.summary.path })}${notes ? ` ${notes}` : ""}`);
        setPass("");
        setConfirm("");
        setShowErr(false);
      }
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setBusy(null);
    }
  };

  const doImport = async () => {
    if (!importPass) return;
    setBusy("import");
    setImported(null);
    try {
      const r = await api.backup.import(importPass, effectiveMode, t("backup.importDialog"));
      if (r.problem) {
        toast.error(problemText(t, r.problem));
      } else if (!r.cancelled) {
        setImported(r.summary);
        setImportPass("");
        toast.success(t("backup.imported", { n: r.summary.accounts.length }));
        loadHasData();
        onImported();
      }
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><PackageOpen className="size-4" aria-hidden /> {t("settings.backup")}</CardTitle>
        <CardDescription>{t("settings.backupDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-6 sm:grid-cols-2">
        <form className="grid content-start gap-3" autoComplete="off" onSubmit={(e) => { e.preventDefault(); void doExport(); }}>
          <p className="text-[13px] font-medium">{t("backup.export")}</p>
          <Field id="b-pass" label={t("backup.passphrase")} hint={t("backup.passphraseHint")} error={code === "short" || code === "empty" ? exportError : undefined}>
            <Input {...fieldAria("b-pass", code === "short" || code === "empty" ? exportError : undefined, "x")} type="password" autoComplete="new-password" value={pass} onChange={(e) => setPass(e.target.value)} />
          </Field>
          <Field id="b-confirm" label={t("backup.confirm")} error={code === "mismatch" ? exportError : undefined}>
            <Input {...fieldAria("b-confirm", code === "mismatch" ? exportError : undefined)} type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
          </Field>
          <label className="flex cursor-pointer items-center gap-2 text-[13px]">
            <Checkbox checked={withLogs} onCheckedChange={(v) => setWithLogs(v === true)} />
            {t("backup.withLogs")}
          </label>
          <div>
            <Button type="submit" disabled={busy !== null}>
              {busy === "export" ? <Spinner /> : <Download />} {t("backup.exportButton")}
            </Button>
          </div>
        </form>

        <form className="grid content-start gap-3" autoComplete="off" onSubmit={(e) => { e.preventDefault(); if (effectiveMode === "replace") setConfirmReplace(true); else void doImport(); }}>
          <p className="text-[13px] font-medium">{t("backup.import")}</p>
          <Field id="b-ipass" label={t("backup.importPassphrase")}>
            <Input {...fieldAria("b-ipass")} type="password" autoComplete="off" value={importPass} onChange={(e) => setImportPass(e.target.value)} />
          </Field>
          {choice !== "default" && (
            <Field id="b-mode" label={t("backup.mode")} hint={choice === "replaceOnly" ? t("backup.probeFailed") : t("backup.modeHint")}>
              <Select value={effectiveMode === "default" ? "merge" : effectiveMode} onValueChange={(v) => setMode(v as typeof mode)} disabled={choice === "replaceOnly"}>
                <SelectTrigger id="b-mode"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {choice === "choose" && <SelectItem value="merge">{t("backup.modeMerge")}</SelectItem>}
                  <SelectItem value="replace">{t("backup.modeReplace")}</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          )}
          <div>
            <Button type="submit" variant={effectiveMode === "replace" ? "destructive" : "default"} disabled={busy !== null || !importPass}>
              {busy === "import" ? <Spinner /> : <Upload />} {t("backup.importButton")}
            </Button>
          </div>
          {imported && <ImportResult s={imported} />}
        </form>
      </CardContent>

      <Dialog open={confirmReplace} onOpenChange={setConfirmReplace}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle>{t("backup.replaceTitle")}</DialogTitle>
            <DialogDescription>{t("backup.replaceBody")}</DialogDescription>
          </DialogHeader>
          <p className="text-[13px] font-medium">
            {localNames.length > 0 ? t("backup.replaceAccounts", { names: localNames.join(", ") }) : t("backup.replaceNoAccounts")}
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmReplace(false)}>{t("common.cancel")}</Button>
            <Button variant="destructive" onClick={() => { setConfirmReplace(false); void doImport(); }}>{t("backup.replaceConfirm")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

function ImportResult({ s }: { s: ImportSummary }) {
  const { t } = useI18n();
  return (
    <div role="status" className="space-y-1 rounded-md border bg-muted/40 p-3 text-xs">
      <p className="font-medium">{t("backup.imported", { n: s.accounts.length })}{s.accounts.length > 0 && `: ${s.accounts.join(", ")}`}</p>
      <p className="text-muted-foreground">{t("backup.from", { os: s.sourceOS, version: s.sourceVersion })}</p>
      {s.newMasterKey && <p>{t("backup.newKey")}</p>}
      {s.skippedCodes.length > 0 && <p className="text-muted-foreground">{t("backup.skipped")}: {s.skippedCodes.map((n) => noteText(t, n)).join(" ")}</p>}
      {s.noteCodes.map((n) => <p key={n.code + n.args.join()} className="text-muted-foreground">{noteText(t, n)}</p>)}
      <p className="text-muted-foreground">{t("backup.hint")}</p>
    </div>
  );
}
