import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { api } from "@/api";
import type { Settings, Snapshot } from "@/api";
import { SNAPSHOT_POLL_MS } from "@/config";
import { errMsg, safeStorage } from "@/lib/utils";
import { resolveLang, type Lang } from "@/lib/time";
import { I18nProvider } from "@/i18n";
import { pageFromSearch } from "@/lib/route";
import { ToastProvider } from "@/components/toast";

export type Page = "overview" | "accounts" | "logs" | "health" | "settings" | "about";
export type AccountFilter = "all" | "attention" | "paused";
export interface Route {
  page: Page;
  /** account list filter to preselect */
  filter?: AccountFilter;
  /** open the wizard (add) or edit the named account */
  wizard?: { mode: "add" } | { mode: "edit"; name: string; step?: number };
  /** bumps on every navigate so the same target can be opened twice */
  nonce: number;
}

type LangPref = Settings["ui"]["language"];
type ThemePref = Settings["ui"]["theme"];

interface SnapshotState {
  data: Snapshot | null;
  error: string | null;
  loading: boolean;
  refresh: () => Promise<void>;
}

interface AppCtx {
  route: Route;
  navigate: (page: Page, opts?: Pick<Route, "filter" | "wizard">) => void;
  snapshot: SnapshotState;
  prefs: { language: LangPref; theme: ThemePref };
  setPrefs: (p: { language: LangPref; theme: ThemePref }) => void;
}

const Ctx = createContext<AppCtx | null>(null);

const LS_LANG = "ui.language";
const LS_THEME = "ui.theme";

function loadPrefs(): { language: LangPref; theme: ThemePref } {
  const l = safeStorage.get(LS_LANG);
  const th = safeStorage.get(LS_THEME);
  return {
    language: l === "zh" || l === "en" || l === "auto" ? l : "auto",
    theme: th === "light" || th === "dark" || th === "system" ? th : "system",
  };
}

function applyTheme(theme: ThemePref) {
  const dark = theme === "dark" || (theme === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
}

export function AppProvider({ children }: { children: ReactNode }) {
  const [route, setRoute] = useState<Route>(() => ({ page: pageFromSearch(location.search) ?? "overview", nonce: 0 }));
  const [prefs, setPrefsState] = useState(loadPrefs);
  const [data, setData] = useState<Snapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const inflight = useRef(false);

  const navigate = useCallback<AppCtx["navigate"]>((page, opts) => {
    setRoute((r) => ({ page, ...opts, nonce: r.nonce + 1 }));
  }, []);

  const setPrefs = useCallback((p: { language: LangPref; theme: ThemePref }) => {
    setPrefsState(p);
    safeStorage.set(LS_LANG, p.language);
    safeStorage.set(LS_THEME, p.theme);
  }, []);

  // Theme: apply now and follow the OS when set to "system".
  useEffect(() => {
    applyTheme(prefs.theme);
    if (prefs.theme !== "system") return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const on = () => applyTheme("system");
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, [prefs.theme]);

  // Saved UI settings win over the local cache.
  useEffect(() => {
    api.settings
      .get()
      .then((s) => setPrefs({ language: s.ui.language, theme: s.ui.theme }))
      .catch(() => undefined);
  }, [setPrefs]);

  const refresh = useCallback(async () => {
    if (inflight.current) return;
    inflight.current = true;
    try {
      const s = await api.status.snapshot();
      setData(s);
      setError(null);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      inflight.current = false;
      setLoading(false);
    }
  }, []);

  // Poll every 3 s while the document is visible; refresh right away on return.
  useEffect(() => {
    let timer: number | undefined;
    const start = () => {
      if (timer !== undefined) return;
      void refresh();
      timer = window.setInterval(() => void refresh(), SNAPSHOT_POLL_MS);
    };
    const stop = () => {
      if (timer !== undefined) window.clearInterval(timer);
      timer = undefined;
    };
    const onVis = () => (document.visibilityState === "visible" ? start() : stop());
    if (document.visibilityState === "visible") start();
    document.addEventListener("visibilitychange", onVis);
    return () => {
      document.removeEventListener("visibilitychange", onVis);
      stop();
    };
  }, [refresh]);

  const lang: Lang = resolveLang(prefs.language, navigator.language);
  useEffect(() => {
    document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
  }, [lang]);

  const value = useMemo<AppCtx>(
    () => ({ route, navigate, snapshot: { data, error, loading, refresh }, prefs, setPrefs }),
    [route, navigate, data, error, loading, refresh, prefs, setPrefs],
  );

  return (
    <Ctx.Provider value={value}>
      <I18nProvider lang={lang}>
        <ToastProvider>{children}</ToastProvider>
      </I18nProvider>
    </Ctx.Provider>
  );
}

export function useApp(): AppCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error("useApp outside AppProvider");
  return c;
}
