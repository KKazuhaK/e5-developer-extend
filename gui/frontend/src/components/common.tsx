import type { ReactNode } from "react";
import {
  AlertTriangle, CalendarX, CheckCircle2, Clock, Info, LogIn, Loader2, PauseCircle, Power, RefreshCw, XCircle,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import { displayStatus, levelBadge, statusBadge, type BadgeSpec, type IconName } from "@/lib/status";
import type { AccountSummary, Level } from "@/api";

const ICONS: Record<IconName, typeof Info> = {
  clock: Clock,
  check: CheckCircle2, alert: AlertTriangle, pause: PauseCircle, "calendar-x": CalendarX,
  "log-in": LogIn, x: XCircle, info: Info, power: Power,
};

export function SpecBadge({ spec, className }: { spec: BadgeSpec; className?: string }) {
  const { t } = useI18n();
  const Icon = ICONS[spec.icon];
  return (
    <Badge tone={spec.tone} className={className}>
      <Icon aria-hidden />
      {t(spec.label)}
    </Badge>
  );
}
export const StatusBadge = ({ account, className }: { account: Pick<AccountSummary, "status" | "lastRun" | "lastSuccess">; className?: string }) => (
  <SpecBadge spec={statusBadge(displayStatus(account))} className={className} />
);
export const LevelBadge = ({ level }: { level: Level }) => <SpecBadge spec={levelBadge(level)} />;
export const ToneIcon = ({ name, className }: { name: IconName; className?: string }) => {
  const Icon = ICONS[name];
  return <Icon aria-hidden className={className} />;
};

export function PageHeader({
  title, description, actions, sticky,
}: { title: string; description?: string; actions?: ReactNode; sticky?: boolean }) {
  return (
    <header
      className={cn(
        "mb-5 flex min-h-10 items-start justify-between gap-4",
        sticky && "sticky top-0 z-20 -mx-6 -mt-6 mb-3 bg-background px-6 pt-6 pb-3",
      )}
    >
      <div className="min-w-0">
        <h1 className="text-xl leading-tight font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-1 text-[13px] text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </header>
  );
}

export function LoadingBlock({ rows = 3, className }: { rows?: number; className?: string }) {
  const { t } = useI18n();
  return (
    <div className={cn("space-y-3", className)} role="status" aria-label={t("common.loading")}>
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-20 w-full" />
      ))}
    </div>
  );
}

export function ErrorBlock({ message, onRetry, className }: { message: string; onRetry?: () => void; className?: string }) {
  const { t } = useI18n();
  return (
    <div role="alert" className={cn("flex items-start gap-3 rounded-lg border border-danger/40 bg-danger-bg p-4 text-[13px]", className)}>
      <XCircle className="mt-0.5 size-4 shrink-0 text-danger" aria-hidden />
      <div className="min-w-0 flex-1">
        <p className="font-medium text-danger">{t("common.loadFailed")}</p>
        <p className="mt-1 break-words text-foreground/80">{message}</p>
      </div>
      {onRetry && (
        <Button size="sm" variant="outline" onClick={onRetry}>
          <RefreshCw /> {t("common.retry")}
        </Button>
      )}
    </div>
  );
}

export function EmptyBlock({ icon, title, hint, children }: { icon: ReactNode; title: string; hint?: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed px-6 py-12 text-center">
      <div className="flex size-12 items-center justify-center rounded-full bg-muted text-muted-foreground [&_svg]:size-6" aria-hidden>
        {icon}
      </div>
      <div>
        <p className="font-medium">{title}</p>
        {hint && <p className="mx-auto mt-1 max-w-md text-[13px] text-muted-foreground">{hint}</p>}
      </div>
      {children}
    </div>
  );
}

export function Spinner({ className }: { className?: string }) {
  return <Loader2 aria-hidden className={cn("size-4 animate-spin", className)} />;
}

/** Label + control + hint + error, wired with aria attributes. */
export function Field({
  id, label, hint, error, children, className,
}: { id: string; label: string; hint?: string; error?: string; children: ReactNode; className?: string }) {
  return (
    <div className={cn("space-y-1.5", className)}>
      <label htmlFor={id} className="block text-[13px] leading-none font-medium">
        {label}
      </label>
      {children}
      {hint && !error && (
        <p id={`${id}-hint`} className="text-xs text-muted-foreground">
          {hint}
        </p>
      )}
      {error && (
        <p id={`${id}-err`} role="alert" className="flex items-start gap-1 text-xs text-danger">
          <AlertTriangle className="mt-px size-3.5 shrink-0" aria-hidden />
          <span>{error}</span>
        </p>
      )}
    </div>
  );
}
export const fieldAria = (id: string, error?: string, hint?: string) => ({
  id,
  "aria-invalid": error ? true : undefined,
  "aria-describedby": error ? `${id}-err` : hint ? `${id}-hint` : undefined,
});
