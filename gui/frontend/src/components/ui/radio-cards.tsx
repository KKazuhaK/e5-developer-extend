import { useRef, useState, type KeyboardEvent, type ReactNode } from "react";
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
 * With a selection, arrow keys (and Home/End) move and select, as for native
 * radios. With none, they only move focus; Space, Enter or a click selects.
 */
export function RadioCards<V extends string>({
  value, onChange, options, labelledBy, className,
}: {
  value: string | null; onChange: (v: V) => void; options: RadioCardOption<V>[]; labelledBy: string; className?: string;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const idx = options.findIndex((o) => o.value === value);
  const [focusIdx, setFocusIdx] = useState(0);
  const onKey = (e: KeyboardEvent, i: number) => {
    let n: number;
    if (e.key === "ArrowRight" || e.key === "ArrowDown") n = (i + 1) % options.length;
    else if (e.key === "ArrowLeft" || e.key === "ArrowUp") n = (i - 1 + options.length) % options.length;
    else if (e.key === "Home") n = 0;
    else if (e.key === "End") n = options.length - 1;
    else return;
    e.preventDefault();
    refs.current[n]?.focus();
    setFocusIdx(n);
    if (idx >= 0) onChange(options[n].value);
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
            tabIndex={idx >= 0 ? (on ? 0 : -1) : i === Math.min(focusIdx, options.length - 1) ? 0 : -1}
            onFocus={() => setFocusIdx(i)}
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
