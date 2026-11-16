import { useCallback, useState } from "react";
import { api } from "@/api";
import { useApp } from "@/state/app";
import { useToast } from "@/components/toast";
import { useI18n } from "@/i18n";
import { errMsg } from "@/lib/utils";

/** Run now / pause / resume with busy tracking, toasts and an immediate refresh. */
export function useAccountActions() {
  const { snapshot } = useApp();
  const toast = useToast();
  const { t } = useI18n();
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set());

  const wrap = useCallback(
    async (key: string, fn: () => Promise<void>, okText: string) => {
      setBusy((b) => new Set(b).add(key));
      try {
        await fn();
        toast.success(okText);
        await snapshot.refresh();
      } catch (e) {
        toast.error(errMsg(e));
      } finally {
        setBusy((b) => {
          const n = new Set(b);
          n.delete(key);
          return n;
        });
      }
    },
    [snapshot, toast],
  );

  return {
    busy,
    runNow: (names: string[]) => wrap(names.length === 1 ? names[0] : "*", () => api.status.runNow(names), t("toast.runQueued")),
    pause: (name: string) => wrap(name, () => api.accounts.pause(name), t("toast.paused", { name })),
    resume: (name: string) => wrap(name, () => api.accounts.resume(name), t("toast.resumed", { name })),
  };
}
