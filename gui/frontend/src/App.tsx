import { lazy, Suspense, useEffect, useRef, type ComponentType } from "react";
import { Activity, HeartPulse, Info, LayoutDashboard, Settings as SettingsIcon, Users } from "lucide-react";
import { AppName } from "@/config";
import appIcon from "@/assets/appicon-128.png";
import { useI18n, type DictKey } from "@/i18n";
import { useApp, type Page } from "@/state/app";
import { cn } from "@/lib/utils";
import { engineState, needsAction } from "@/lib/status";
import { isPage } from "@/lib/route";
import { useToast } from "@/components/toast";
import { api } from "@/api";
import { ToneIcon } from "@/components/common";
import { Overview } from "@/pages/Overview";
import { Accounts } from "@/pages/Accounts";
import { Logs } from "@/pages/Logs";
import { Health } from "@/pages/Health";
import { SettingsPage } from "@/pages/SettingsPage";
import { About } from "@/pages/About";

// Dev-only panel for the fake backend. The literal check lets the bundler drop
// it (and mock.ts) from real builds.
const MockPanel = import.meta.env.VITE_MOCK === "1" ? lazy(() => import("@/api/MockPanel")) : null;

const NAV: { page: Page; icon: ComponentType<{ className?: string }>; label: DictKey }[] = [
  { page: "overview", icon: LayoutDashboard, label: "nav.overview" },
  { page: "accounts", icon: Users, label: "nav.accounts" },
  { page: "logs", icon: Activity, label: "nav.logs" },
  { page: "health", icon: HeartPulse, label: "nav.health" },
  { page: "settings", icon: SettingsIcon, label: "nav.settings" },
  { page: "about", icon: Info, label: "nav.about" },
];

const PAGES: Record<Page, ComponentType> = {
  overview: Overview, accounts: Accounts, logs: Logs, health: Health, settings: SettingsPage, about: About,
};

function RunDoneListener() {
  const { t } = useI18n();
  const toast = useToast();
  const { snapshot } = useApp();
  // Subscribe once; the handler always sees the latest t, toast and refresh.
  const latest = useRef({ t, toast, refresh: snapshot.refresh });
  latest.current = { t, toast, refresh: snapshot.refresh };
  useEffect(
    () =>
      api.status.onRunDone((e) => {
        const { t, toast, refresh } = latest.current;
        if (e.error) toast.error(t("toast.runDoneFailed", { error: e.error }));
        else toast.success(t("toast.runDone"));
        void refresh();
      }),
    [],
  );
  return null;
}

/** Backend "navigate" event: switch page if the payload names a known one. */
function NavigateListener() {
  const { navigate } = useApp();
  const nav = useRef(navigate);
  nav.current = navigate;
  useEffect(
    () =>
      api.system.onNavigate((page) => {
        if (isPage(page)) nav.current(page);
      }),
    [],
  );
  return null;
}

export function App() {
  const { t } = useI18n();
  const { route, navigate, snapshot } = useApp();
  const Current = PAGES[route.page];
  const mainRef = useRef<HTMLElement>(null);
  useEffect(() => {
    mainRef.current?.scrollTo(0, 0);
  }, [route.page, route.nonce]);
  const attention = snapshot.data?.accounts.filter((a) => needsAction(a.status)).length ?? 0;
  const eng = snapshot.data ? engineState(snapshot.data.engine) : null;

  return (
    <div className="flex h-full">
      <nav aria-label={t("nav.label")} className="flex w-52 shrink-0 flex-col border-r bg-sidebar px-3 py-4">
        <div className="mb-5 flex items-center gap-2 px-2">
          <img src={appIcon} alt="" aria-hidden className="size-7 shrink-0 select-none" draggable={false} />
          <span className="text-[15px] font-semibold tracking-tight">{AppName}</span>
        </div>
        <ul className="flex flex-col gap-0.5">
          {NAV.map(({ page, icon: Icon, label }) => {
            const active = route.page === page;
            const count = page === "accounts" ? attention : 0;
            return (
              <li key={page}>
                <button
                  type="button"
                  onClick={() => navigate(page)}
                  aria-current={active ? "page" : undefined}
                  className={cn(
                    "flex h-9 w-full cursor-pointer items-center gap-2.5 rounded-md px-2.5 text-sm transition-colors",
                    active ? "bg-accent font-medium text-accent-foreground" : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
                  )}
                >
                  <Icon className="size-4" />
                  <span className="flex-1 text-left">{t(label)}</span>
                  {count > 0 && (
                    <span className="min-w-5 rounded-full bg-danger px-1.5 text-center text-[11px] leading-5 font-semibold text-background" aria-label={t("nav.attentionCount", { count })}>
                      {count}
                    </span>
                  )}
                </button>
              </li>
            );
          })}
        </ul>
        <div className="mt-auto px-2 pt-4 text-xs text-muted-foreground" aria-live="polite">
          <div className="flex h-5 items-center gap-1.5">
            {eng && <ToneIcon name={eng === "stopped" ? "power" : "check"} className={cn("size-3.5", eng === "stopped" ? "" : "text-success")} />}
            <span>{eng ? t(`engine.${eng}`) : t("common.loading")}</span>
          </div>
        </div>
      </nav>
      <main ref={mainRef} className="scroll-stable min-w-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-5xl px-6 py-6">
          <Current key={`${route.page}-${route.nonce}`} />
        </div>
      </main>
      <RunDoneListener />
      <NavigateListener />
      {MockPanel && (
        <Suspense fallback={null}>
          <MockPanel />
        </Suspense>
      )}
    </div>
  );
}
