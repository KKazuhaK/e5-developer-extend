import type { AccountSummary, SubscriptionView, TenantSubscriptions } from "@/api/types";
import type { DictKey } from "@/i18n";

export type Tone = "success" | "warning" | "danger" | "muted" | "info";

/** At this many days or fewer the end date is shown as urgent (the engine's doctor check warns here too). */
export const URGENT_DAYS = 7;
/** At this many days or fewer it is shown as approaching (the first alert goes out here). */
export const APPROACHING_DAYS = 30;

/** Label key and tone of a subscription status; an unknown status keeps its own name. */
export function statusBadge(status: string): { label: DictKey | null; tone: Tone } {
  switch (status) {
    case "Enabled": return { label: "sub.status.Enabled", tone: "success" };
    case "Warning": return { label: "sub.status.Warning", tone: "warning" };
    case "Suspended": return { label: "sub.status.Suspended", tone: "danger" };
    case "LockedOut": return { label: "sub.status.LockedOut", tone: "danger" };
    case "Deleted": return { label: "sub.status.Deleted", tone: "danger" };
  }
  return { label: null, tone: "muted" };
}

/** Tone of the days left: nothing known is muted. */
export function daysTone(days: number | null): Tone {
  if (days === null) return "muted";
  if (days <= URGENT_DAYS) return "danger";
  if (days <= APPROACHING_DAYS) return "warning";
  return "success";
}

/** The subscriptions the settings watch (the ones that alert), in the order the backend sent them. */
export const watched = (tv: TenantSubscriptions): SubscriptionView[] => tv.subscriptions.filter((s) => s.watched);

/**
 * The account to start the automatic app setup from, to give the tenant a
 * sign-in that can read the subscriptions: any account that is not already an
 * application identity (those get the permission from `e5extend app setup`).
 */
export function fixPermissionsFrom(tv: TenantSubscriptions, accounts: AccountSummary[]): string | null {
  for (const name of tv.accounts) {
    const a = accounts.find((x) => x.name === name);
    if (a && a.auth !== "client_secret") return name;
  }
  return null;
}

const DATE_RE = /^\d{4}-\d{2}-\d{2}$/;

/** True for a real calendar date written YYYY-MM-DD. */
export function validDate(s: string): boolean {
  if (!DATE_RE.test(s)) return false;
  const d = new Date(`${s}T00:00:00Z`);
  return !Number.isNaN(d.getTime()) && d.toISOString().slice(0, 10) === s;
}

/** The account whose config line holds the tenant's hand-written date. */
export const dateOwner = (tv: TenantSubscriptions): string => tv.accounts[0] ?? "";
