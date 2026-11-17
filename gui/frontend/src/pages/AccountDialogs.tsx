import { useEffect, useState } from "react";
import { api } from "@/api";
import type { AccountInput } from "@/api";
import { useI18n } from "@/i18n";
import { useApp } from "@/state/app";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ErrorBlock, LoadingBlock } from "@/components/common";
import { errMsg } from "@/lib/utils";
import { LoginStep, TestStep } from "./AccountSteps";

/** Loads the account the dialog is about; the secret is never part of it. */
function useAccountInput(name: string | null) {
  const [input, setInput] = useState<AccountInput | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    setInput(null);
    setError(null);
    if (!name) return;
    let alive = true;
    api.accounts
      .get(name)
      .then((a) => alive && setInput(a))
      .catch((e) => alive && setError(errMsg(e)));
    return () => {
      alive = false;
    };
  }, [name]);
  return { input, error };
}

/** Signs an account in (device code, browser or a check of the stored secret), outside the add wizard. */
export function SignInDialog({ name, onClose }: { name: string | null; onClose: () => void }) {
  const { t } = useI18n();
  const { snapshot } = useApp();
  const { input, error } = useAccountInput(name);
  const [restart, setRestart] = useState(0);
  const [done, setDone] = useState(false);
  useEffect(() => setDone(false), [name]);

  return (
    <Dialog open={!!name} onOpenChange={(o) => !o && onClose()}>
      <DialogContent closeLabel={t("common.close")} className="max-w-xl" aria-describedby={undefined}>
        <DialogHeader>
          <DialogTitle>{t("dialog.signInTitle", { name: name ?? "" })}</DialogTitle>
        </DialogHeader>
        {error ? (
          <ErrorBlock message={error} />
        ) : !input ? (
          <LoadingBlock rows={2} />
        ) : (
          <LoginStep
            key={`${name}-${restart}`}
            input={input}
            auto
            afterSetup={false}
            consentUrl=""
            alreadyDone={false}
            onDone={setDone}
            onIdle={() => undefined}
            onRestart={() => setRestart((n) => n + 1)}
            onStart={() => setRestart((n) => n + 1)}
          />
        )}
        <DialogFooter>
          <Button
            variant={done ? "default" : "outline"}
            onClick={() => {
              void snapshot.refresh();
              onClose();
            }}
          >
            {t("common.close")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Gets a token silently and shows what the account can use. */
export function TestDialog({ name, onClose }: { name: string | null; onClose: () => void }) {
  const { t } = useI18n();
  return (
    <Dialog open={!!name} onOpenChange={(o) => !o && onClose()}>
      <DialogContent closeLabel={t("common.close")} className="max-w-xl" aria-describedby={undefined}>
        <DialogHeader>
          <DialogTitle>{t("dialog.testTitle", { name: name ?? "" })}</DialogTitle>
        </DialogHeader>
        {name && <TestStep key={name} name={name} />}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>{t("common.close")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
