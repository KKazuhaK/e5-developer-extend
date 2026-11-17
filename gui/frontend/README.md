# GUI frontend

React + TypeScript + Vite + Tailwind CSS (v4) with shadcn/ui style components.
The component sources are committed under `src/components/ui/`; nothing is fetched at build or run time.
Spec: [docs/design/gui.md](../../docs/design/gui.md).

## Commands

```bash
npm ci
npm run dev:mock -- --host 127.0.0.1 --port 5174   # fake backend, plain browser
npm run dev                                        # real Wails bindings (needs the Wails shell)
npm run typecheck                                  # tsc --noEmit
npm test                                           # vitest run
npm run build                                      # type check + production build (real bindings)
npm run build:mock                                 # same with the fake backend
```

## Mock backend

`VITE_MOCK=1` makes `src/api/index.ts` use `src/api/mock.ts`, which implements the whole contract
(`src/api/types.ts`, `src/api/interface.ts`) with fake data: healthy, needs-attention, paused and
not-signed-in accounts, a log stream, doctor checks, a device-code login that emits `login:<name>` style
events over time. A small flask button at the bottom left opens a panel to switch the engine host
(`gui` / `service` / `none`) and slow down responses. URL switches: `?empty=1` (no accounts),
`?host=none|service|gui`, `?slow=1`, `?recommend=full|nomailbox|noonedrive|error` (what the API step of the account wizard is told).

Without `VITE_MOCK` the mock code is not part of the bundle (the choice is made with a build-time constant).

## Real bindings and the full build

The bindings are generated, not committed (`gui/frontend/bindings/` is gitignored). `src/api/wails.ts`
imports them directly, so generate before any typecheck or build. From the repo root:

```bash
export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"
wails3 generate bindings -d gui/frontend/bindings -ts ./gui
(cd gui/frontend && npm ci && npm run typecheck && npm test && npm run build)
go build -tags frontend ./gui          # the Go build needs -tags frontend (it embeds gui/frontend/dist)
```

`npm run build:mock` also needs the bindings generated, because `tsc` type-checks `wails.ts`.

`wails.ts` casts the generated model classes to the types in `src/api/types.ts` and turns Go `null` slices into `[]`.
Events are read with `Events.On(name, e => e.data)`: `login:<name>` carries a `LoginEvent`, `run:done` carries `{error}`.

## Layout

- `src/api/` contract types, `Api` interface, mock and Wails implementations
- `src/lib/` pure helpers with tests (relative time, status to badge, settings and account validation)
- `src/i18n/` own dictionaries (`zh.ts` is the key source, `en.ts` must have the same keys; a test checks it)
- `src/state/` app state (route, 3 s snapshot polling while visible, theme and language)
- `src/pages/` one file per screen; `src/components/` shared pieces and `ui/` shadcn style primitives
- `src/config.ts` the product name (`AppName`), links, poll intervals

## Behaviour notes

- `ValidationError.field` is a camelCase path (`schedule.intervalMin`, `notify.events`, ...); snake_case config paths are accepted too.
- `Login` returns at once; completion arrives only through `login:<name>` events (`done` / `error`).
- The wizard test step uses `AccountService.Test` (scopes, usable and total APIs, token expiry); the per-category list is a best-effort extra from `Catalog.List`.
- Health check titles are localized by `DoctorCheck.id` (`src/lib/doctor.ts`, `doctor.*` keys); unknown ids fall back to the backend title. `detail` and `fix` are shown as returned because they contain commands.
- Service install/uninstall results are shown after the action; the `manual` steps are highlighted.
- Secrets, passwords and the webhook address live only in password inputs and are cleared right after submit.
