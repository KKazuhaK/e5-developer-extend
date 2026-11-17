import { Copy, ExternalLink } from "lucide-react";
import { api } from "@/api";
import { useI18n } from "@/i18n";
import { useToast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/common";
import { copyText } from "@/lib/utils";

/** The device code, where to enter it, and the buttons that help. Shared by the account sign-in and the administrator's. */
export function DevicePrompt({ message, userCode, verificationUri }: { message: string; userCode?: string; verificationUri?: string }) {
  const { t } = useI18n();
  const toast = useToast();
  const copy = async () => {
    if (userCode && (await copyText(userCode))) toast.success(t("wizard.copied"));
  };
  return (
    <>
      <p className="text-[13px] text-muted-foreground">{t("wizard.deviceHint")}</p>
      {userCode ? (
        <div className="rounded-xl border bg-muted/50 px-8 py-4">
          <p className="text-xs text-muted-foreground">{t("wizard.yourCode")}</p>
          <p className="mt-1 font-mono text-3xl font-semibold tracking-[0.2em] select-all" aria-label={t("wizard.yourCode")}>
            {userCode}
          </p>
        </div>
      ) : (
        // No code in the event: Microsoft's own instructions are in the message.
        <p className="max-w-md rounded-xl border bg-muted/50 px-5 py-3 text-[13px] break-words select-all">{message}</p>
      )}
      <p className="max-w-md text-xs break-all text-muted-foreground">{verificationUri}</p>
      <div className="flex gap-2">
        <Button variant="outline" disabled={!userCode} onClick={() => void copy()}>
          <Copy /> {t("wizard.copy")}
        </Button>
        <Button onClick={() => verificationUri && void api.system.openExternal(verificationUri)}>
          <ExternalLink /> {t("wizard.openBrowser")}
        </Button>
      </div>
      <p className="flex items-center gap-2 text-[13px] text-muted-foreground">
        <Spinner /> {t("wizard.waiting")}
      </p>
    </>
  );
}

/** The sign-in page was opened in the browser. */
export function BrowserPrompt() {
  const { t } = useI18n();
  return (
    <>
      <Spinner className="size-8 text-primary" />
      <p className="text-base font-medium">{t("wizard.browserOpened")}</p>
      <p className="text-[13px] text-muted-foreground">{t("wizard.browserOpenedHint")}</p>
    </>
  );
}
