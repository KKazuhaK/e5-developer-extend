import { useState, useSyncExternalStore } from "react";
import { FlaskConical } from "lucide-react";
import { mockControls, mockNavigate } from "./mock";

/** Dev-only controls for the fake backend. Rendered only when VITE_MOCK=1. */
export default function MockPanel() {
  const c = useSyncExternalStore(mockControls.subscribe, mockControls.get);
  const [open, setOpen] = useState(false);
  // ?demo=1 is the screenshot data set: keep the panel out of the picture.
  if (new URLSearchParams(location.search).get("demo") === "1") return null;
  return (
    <div className="fixed bottom-12 left-3 z-[70] text-xs">
      {open ? (
        <div className="w-52 space-y-2 rounded-lg border bg-popover p-3 shadow-lg">
          <div className="flex items-center justify-between font-medium">
            Mock
            <button type="button" className="cursor-pointer text-muted-foreground" onClick={() => setOpen(false)} aria-label="Close mock panel">
              ×
            </button>
          </div>
          <label className="flex items-center justify-between gap-2">
            Engine
            <select className="rounded border bg-card px-1 py-0.5" value={c.host} onChange={(e) => mockControls.set({ host: e.target.value as "gui" | "service" | "none" })}>
              <option value="gui">gui</option>
              <option value="service">service</option>
              <option value="none">none</option>
            </select>
          </label>
          <label className="flex items-center justify-between gap-2">
            Slow responses
            <input type="checkbox" checked={c.slow} onChange={(e) => mockControls.set({ slow: e.target.checked })} />
          </label>
          <label className="flex items-center justify-between gap-2">
            navigate
            <select className="rounded border bg-card px-1 py-0.5" value="" onChange={(e) => e.target.value && mockNavigate(e.target.value)}>
              <option value="">send…</option>
              {["overview", "accounts", "logs", "health", "settings", "about", "bogus"].map((p) => <option key={p} value={p}>{p}</option>)}
            </select>
          </label>
          <p className="text-muted-foreground">No accounts: reload with ?empty=1</p>
          <p className="text-muted-foreground">App setup: reload with ?appsetup=candidates, existing, noconsent, forbidden or blocked</p>
        </div>
      ) : (
        <button
          type="button"
          onClick={() => setOpen(true)}
          className="flex size-7 cursor-pointer items-center justify-center rounded-full border bg-popover text-muted-foreground opacity-60 shadow hover:opacity-100"
          aria-label="Open mock panel"
        >
          <FlaskConical className="size-3.5" />
        </button>
      )}
    </div>
  );
}
