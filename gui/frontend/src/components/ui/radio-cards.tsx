import { useRef, type KeyboardEvent, type ReactNode } from "react";
import { Check } from "lucide-react";
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
              "relative cursor-pointer rounded-md border px-3 py-2 text-left text-[13px] outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
              on ? "border-2 border-primary bg-primary/10 px-[11px] py-[7px]" : "bg-card hover:bg-accent",
            )}
          >
            {on && (
              <span aria-hidden className="absolute top-1.5 right-1.5 flex size-4 items-center justify-center rounded-full bg-primary text-primary-foreground">
                <Check className="size-3" strokeWidth={3} />
              </span>
            )}
            <span className={cn("block font-medium", on && "pr-5")}>{o.label}</span>
            {o.description && <span className="mt-0.5 block text-xs text-muted-foreground">{o.description}</span>}
          </button>
        );
      })}
    </div>
  );
}
