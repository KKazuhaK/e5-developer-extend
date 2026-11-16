import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from "react";
import { CheckCircle2, XCircle, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";

type Kind = "success" | "error";
interface Toast { id: number; kind: Kind; text: string }
interface Ctx { success(text: string): void; error(text: string): void }

const ToastContext = createContext<Ctx | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Toast[]>([]);
  const seq = useRef(0);
  const { t } = useI18n();

  const dismiss = useCallback((id: number) => setItems((l) => l.filter((x) => x.id !== id)), []);
  const push = useCallback(
    (kind: Kind, text: string) => {
      const id = ++seq.current;
      setItems((l) => [...l.slice(-3), { id, kind, text }]);
      setTimeout(() => dismiss(id), kind === "error" ? 9000 : 3500);
    },
    [dismiss],
  );
  const ctx = useRef<Ctx>({ success: (x) => push("success", x), error: (x) => push("error", x) });

  return (
    <ToastContext.Provider value={ctx.current}>
      {children}
      <div className="pointer-events-none fixed right-4 bottom-4 z-[60] flex w-80 flex-col gap-2" role="region" aria-label={t("toast.region")}>
        {items.map((x) => (
          <div
            key={x.id}
            role={x.kind === "error" ? "alert" : "status"}
            className={cn(
              "pointer-events-auto flex items-start gap-2 rounded-lg border bg-popover p-3 text-[13px] shadow-lg",
              x.kind === "error" ? "border-danger/40" : "border-success/40",
            )}
          >
            {x.kind === "error" ? <XCircle className="mt-0.5 size-4 shrink-0 text-danger" /> : <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-success" />}
            <span className="min-w-0 flex-1 break-words">{x.text}</span>
            <button type="button" className="cursor-pointer text-muted-foreground hover:text-foreground" onClick={() => dismiss(x.id)} aria-label={t("common.close")}>
              <X className="size-4" />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast(): Ctx {
  const c = useContext(ToastContext);
  if (!c) throw new Error("useToast outside ToastProvider");
  return c;
}
