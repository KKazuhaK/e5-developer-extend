import { Gauge, RotateCcw, ShieldOff } from "lucide-react";
import type { AccountSummary } from "@/api";
import { useI18n } from "@/i18n";
import { formatRelative } from "@/lib/time";

/**
 * What the engine does about an account by itself: calls Microsoft throttled
 * today, APIs left out for 24 hours, and the next automatic retry of an
 * account that was paused after failed rounds. Renders nothing when there is none.
 */
export function AccountNotes({ a }: { a: AccountSummary }) {
  const { t, lang } = useI18n();
  const autoResume = a.status === "paused" && a.autoResumeAt;
  if (a.todayThrottled <= 0 && a.pausedApis <= 0 && !autoResume) return null;
  return (
    <ul className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
      {a.todayThrottled > 0 && (
        <li className="flex items-center gap-1" title={t("account.throttledHint")}>
          <Gauge className="size-3.5" aria-hidden />
          {t("account.throttledN", { n: a.todayThrottled })}
        </li>
      )}
      {a.pausedApis > 0 && (
        <li className="flex items-center gap-1" title={t("account.pausedApisHint")}>
          <ShieldOff className="size-3.5" aria-hidden />
          {t("account.pausedApis", { n: a.pausedApis })}
        </li>
      )}
      {autoResume && (
        <li className="flex items-center gap-1" title={a.autoResumeAt ?? ""}>
          <RotateCcw className="size-3.5" aria-hidden />
          {t("account.autoResume", { when: formatRelative(a.autoResumeAt, lang) })}
          {a.autoResumeTries > 0 && <span> · {t("account.autoResumeTries", { n: a.autoResumeTries })}</span>}
        </li>
      )}
    </ul>
  );
}
