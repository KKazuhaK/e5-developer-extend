import { AlertTriangle } from "lucide-react";
import type { ServiceResult } from "@/api";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";

/** What an install, start or uninstall did: steps left to the user, warnings and actions. */
export function ServiceResultPanel({ result, className, showActions = true }: { result: ServiceResult; className?: string; showActions?: boolean }) {
  const { t } = useI18n();
  const actions = showActions ? result.actions : [];
  if (!result.manual.length && !result.warnings.length && !actions.length) return null;
  return (
    <div className={cn("space-y-2", className)} role="status">
      {result.manual.length > 0 && (
        <div className="rounded-md border-2 border-warning/60 bg-warning-bg px-3 py-2.5 text-[13px]">
          <p className="mb-1 flex items-center gap-1.5 font-semibold text-warning"><AlertTriangle className="size-4" aria-hidden /> {t("health.manual")}</p>
          <ul className="list-disc space-y-1 pl-5 break-words">{result.manual.map((m) => <li key={m}>{m}</li>)}</ul>
        </div>
      )}
      {result.warnings.length > 0 && (
        <div className="rounded-md bg-muted px-3 py-2 text-[13px]">
          <p className="mb-1 font-medium">{t("health.warnings")}</p>
          <ul className="list-disc space-y-0.5 pl-5 break-words">{result.warnings.map((m) => <li key={m}>{m}</li>)}</ul>
        </div>
      )}
      {actions.length > 0 && (
        <div className="rounded-md bg-muted px-3 py-2 text-[13px]">
          <p className="mb-1 font-medium">{t("health.actions")}</p>
          <ul className="list-disc space-y-0.5 pl-5 break-words">{actions.map((m) => <li key={m}>{m}</li>)}</ul>
        </div>
      )}
    </div>
  );
}
