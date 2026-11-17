import { ShieldAlert } from "lucide-react";
import type { AccountSummary } from "@/api";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import { reasonHead } from "@/lib/account";

/** What to do when a Conditional Access sign-in frequency policy keeps ending the sign-in: the three ways out. */
export function SignInFrequencyNotice({ a, onSwitch }: { a: AccountSummary; onSwitch: (name: string) => void }) {
  const { t } = useI18n();
  return (
    <div className="space-y-2 rounded-md bg-muted/60 px-3 py-2 text-[13px]" data-testid="sign-in-frequency">
      <p className="flex items-start gap-2 font-medium">
        <ShieldAlert className="mt-0.5 size-4 shrink-0 text-danger" aria-hidden />
        <span>{reasonHead(a.reason)}</span>
      </p>
      <ol className="list-decimal space-y-1 pl-9 text-muted-foreground">
        <li>{t("freq.app")}</li>
        <li>{t("freq.password")}</li>
        <li>{t("freq.exclude")}</li>
      </ol>
      <Button size="sm" variant="outline" onClick={() => onSwitch(a.name)}>
        {t("freq.switch")}
      </Button>
    </div>
  );
}
