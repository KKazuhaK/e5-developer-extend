import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { api } from "@/api";
import type { UpdateInfo } from "@/api";
import { errMsg, safeStorage } from "@/lib/utils";
import { dismiss as rememberDismissal, isDismissed, reduceProgress, type InstallState } from "@/lib/update";

interface UpdateCtx {
  info: UpdateInfo | null;
  /** A check is running. */
  checking: boolean;
  /** The reason the last check failed, "" when it worked. */
  checkError: string;
  install: InstallState;
  /** Versions the person put off with "Later". */
  dismissed: (version: string) => boolean;
  check: (force: boolean) => Promise<void>;
  start: () => Promise<void>;
  restart: () => Promise<void>;
  dismiss: (version: string) => void;
  /** Forgets a finished or failed upgrade, to show the banner again. */
  reset: () => void;
}

const Ctx = createContext<UpdateCtx | null>(null);

/**
 * Update state shared by the Overview banner and the About page: what the last
 * check found, a check made when the window opens (it only goes out when the
 * daily check is due), and the progress of a running upgrade. The upgrade runs
 * in the backend; this only follows its "update:progress" reports.
 */
export function UpdateProvider({ children }: { children: ReactNode }) {
  const [info, setInfo] = useState<UpdateInfo | null>(null);
  const [checking, setChecking] = useState(false);
  const [checkError, setCheckError] = useState("");
  const [install, setInstall] = useState<InstallState>({ phase: "idle" });
  const [, bump] = useState(0);
  const inflight = useRef(false);

  const check = useCallback(async (force: boolean) => {
    if (inflight.current) return;
    inflight.current = true;
    setChecking(true);
    try {
      setInfo(await api.update.check(force));
      setCheckError("");
    } catch (e) {
      setCheckError(errMsg(e));
      // What the backend knew before still stands.
      await api.update.info().then(setInfo).catch(() => undefined);
    } finally {
      inflight.current = false;
      setChecking(false);
    }
  }, []);

  // What is known now, then the check that is due.
  useEffect(() => {
    let alive = true;
    api.update
      .info()
      .then((i) => alive && setInfo(i))
      .catch(() => undefined)
      .finally(() => {
        if (alive) void check(false);
      });
    return () => {
      alive = false;
    };
  }, [check]);

  useEffect(() => api.update.onProgress((p) => setInstall((s) => reduceProgress(s, p))), []);

  const start = useCallback(async () => {
    setInstall({ phase: "running", progress: null });
    try {
      await api.update.install();
    } catch (e) {
      setInstall({ phase: "error", code: "other", message: errMsg(e), result: null });
    }
  }, []);

  const restart = useCallback(async () => {
    try {
      await api.update.restart();
    } catch (e) {
      setInstall((s) => (s.phase === "done" ? s : { phase: "error", code: "other", message: errMsg(e), result: null }));
    }
  }, []);

  const dismiss = useCallback((version: string) => {
    rememberDismissal(safeStorage, version);
    bump((n) => n + 1);
  }, []);
  const dismissed = useCallback((version: string) => isDismissed(safeStorage, version), []);
  const reset = useCallback(() => setInstall({ phase: "idle" }), []);

  const value = useMemo<UpdateCtx>(
    () => ({ info, checking, checkError, install, dismissed, check, start, restart, dismiss, reset }),
    [info, checking, checkError, install, dismissed, check, start, restart, dismiss, reset],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useUpdate(): UpdateCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error("useUpdate outside UpdateProvider");
  return c;
}
