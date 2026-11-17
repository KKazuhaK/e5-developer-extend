# Screenshots for the docs

`docs/images/<lang>/<name>.webp` are taken from the real frontend running on its mock backend with the
`?demo=1` data set: a made-up Contoso tenant (plus a second, Fabrikam, on the subscription cards), two healthy accounts, E5 subscription cards for two tenants, a Telegram alert
channel and the background service running. No real tenant data is involved.

## Regenerate

```bash
cd gui/frontend && npm install        # puppeteer-core is a devDependency there
cd ../..
node scripts/screenshots/shoot.mjs
```

The script builds the mock frontend into a temporary folder (`npm run build:mock`, so `gui/frontend/dist` is not
touched), serves it with `vite preview` on `127.0.0.1`, drives the system Chrome at 1280×800, scale 1, and writes
the files. It takes about a minute. Review the result with `git diff --stat docs/images` and look at the pictures.

| Variable | Default | Meaning |
|---|---|---|
| `CHROME_PATH` | `/usr/bin/google-chrome` | Chrome or Chromium executable |
| `PORT` | `4173` | port of the preview server (127.0.0.1 only) |
| `OUT_DIR` | `docs/images` | where `<lang>/<name>.webp` go |
| `LANGS` | `zh,en` | languages to capture |
| `WEBP_QUALITY` | `80` | WebP quality |

`--no-build` serves an existing `gui/frontend/dist` instead (it must be a `build:mock` build).

## What is captured

Light theme, both languages: `overview`, `subscription` (renewal history open), `accounts`, `wizard-method`,
`admin-signin`, `plan`, `progress`, `app-ready`, `api-categories`, `test`, `settings`, `backup`, `alerts`, `health`;
and one `overview-dark` per language.

Button labels are read from `gui/frontend/src/i18n/{zh,en}.ts`, so renaming a string needs no change here. If a
screen changes so much that the script cannot find something, it stops with the missing text. Dates (such as the
subscription end date) are relative to the day the script runs; the date, the "next run" time and the heartbeat age
therefore differ between runs. That is expected.
