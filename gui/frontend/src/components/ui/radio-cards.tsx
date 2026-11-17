import { useRef, type KeyboardEvent, type ReactNode } from "react";
import { cn } from "@/lib/utils";

export interface RadioCardOption<V extends string> {
  value: V;
  label: ReactNode;
  description?: ReactNode;
}

/**
 * A single-choice group drawn as cards. `value` may be null (or a value that
 * is not an option) when nothing is selected, e.g. a "custom" state.
 * Arrow keys move the selection, as for native radios.
 */
export function RadioCards<V extends string>({
  value, onChange, options, labelledBy, className,
}: {
  value: string | null; onChange: (v: V) => void; options: RadioCardOption<V>[]; labelledBy: string; className?: string;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const idx = options.findIndex((o) => o.value === value);
  const onKey = (e: KeyboardEvent, i: number) => {
    const step = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const n = (i + step + options.length) % options.length;
    refs.current[n]?.focus();
    onChange(options[n].value);
  };
  return (
    <div role="radiogroup" aria-labelledby={labelledBy} className={cn("grid gap-2", className)}>
      {options.map((o, i) => {
        const on = i === idx;
        return (
          <button
            key={o.value}
            ref={(el) => void (refs.current[i] = el)}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on || (idx < 0 && i === 0) ? 0 : -1}
            onClick={() => onChange(o.value)}
            onKeyDown={(e) => onKey(e, i)}
            className={cn(
              "cursor-pointer rounded-md border px-3 py-2 text-left text-[13px] transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
              on ? "border-primary bg-primary/5 ring-1 ring-primary" : "bg-card hover:bg-accent",
            )}
          >
            <span className="block font-medium">{o.label}</span>
            {o.description && <span className="mt-0.5 block text-xs text-muted-foreground">{o.description}</span>}
          </button>
        );
      })}
    </div>
  );
}
