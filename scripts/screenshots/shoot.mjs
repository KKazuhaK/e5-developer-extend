#!/usr/bin/env node
// Takes the screenshots in docs/images/<lang>/ from the real frontend running on the mock backend
// with the demo data set (?demo=1). See README.md in this folder.
import { spawn, spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, "../..");
const frontend = path.join(repo, "gui/frontend");
// puppeteer-core is a devDependency of the frontend; resolve it from there.
const puppeteer = createRequire(path.join(frontend, "package.json"))("puppeteer-core");

const CHROME = process.env.CHROME_PATH || "/usr/bin/google-chrome";
const PORT = Number(process.env.PORT || 4173);
const OUT = path.resolve(process.env.OUT_DIR || path.join(repo, "docs/images"));
const QUALITY = Number(process.env.WEBP_QUALITY || 80);
const LANGS = (process.env.LANGS || "zh,en").split(",");
const BASE = `http://127.0.0.1:${PORT}/`;
const noBuild = process.argv.includes("--no-build");

// ---- UI strings: read from the frontend's own dictionaries so button labels never drift ----
function dict(lang) {
  const text = fs.readFileSync(path.join(frontend, "src/i18n", `${lang}.ts`), "utf8");
  const out = {};
  for (const m of text.matchAll(/^\s*"([\w.]+)":\s*("(?:[^"\\]|\\.)*"),?\s*$/gm)) out[m[1]] = JSON.parse(m[2]);
  return out;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitText(page, text, timeout = 30000) {
  await page.waitForFunction((t) => document.body.innerText.includes(t), { timeout }, text);
}

/** Clicks the first button-like element whose text contains `text`. */
async function click(page, text) {
  const ok = await page.evaluate((t) => {
    const el = [...document.querySelectorAll("button, [role=radio], [role=tab], a")].find((e) => e.textContent.trim().includes(t));
    if (el) el.click();
    return Boolean(el);
  }, text);
  if (!ok) throw new Error(`nothing to click: "${text}"`);
  await sleep(250);
}

/** Scrolls the page so the element with this exact text (or matching selector) sits near the top. */
async function scrollToText(page, text) {
  await page.evaluate((t) => {
    const el = [...document.querySelectorAll("*")].find((e) => [...e.childNodes].filter((n) => n.nodeType === 3).map((n) => n.textContent).join("").trim() === t);
    if (!el) throw new Error(`no element with the text "${t}"`);
    // only the main pane scrolls; leave room for its sticky header
    const main = document.querySelector("main");
    main.scrollTop += el.getBoundingClientRect().top - main.getBoundingClientRect().top - 100;
    window.scrollTo(0, 0);
  }, text);
  await sleep(250);
}

async function main() {
  const dist = fs.mkdtempSync(path.join(os.tmpdir(), "e5-mock-dist-"));
  if (!noBuild) {
    console.log("building the mock frontend ...");
    const r = spawnSync("npm", ["run", "build:mock", "--", "--outDir", dist, "--emptyOutDir"], { cwd: frontend, stdio: "inherit" });
    if (r.status !== 0) throw new Error("build:mock failed");
  }
  // --no-build serves whatever the default dist folder holds (must be a build:mock build).
  const serveDir = noBuild ? path.join(frontend, "dist") : dist;
  const preview = spawn(process.execPath, [path.join(frontend, "node_modules/vite/bin/vite.js"), "preview", "--host", "127.0.0.1", "--port", String(PORT), "--strictPort", "--outDir", serveDir], { cwd: frontend, stdio: "ignore" });
  const browser = await (async () => {
    for (let i = 0; i < 50; i++) {
      try { if ((await fetch(BASE)).ok) break; } catch { /* not up yet */ }
      await sleep(200);
    }
    return puppeteer.launch({ executablePath: CHROME, headless: true, args: ["--no-sandbox", "--hide-scrollbars", "--force-color-profile=srgb"] });
  })();

  try {
    for (const lang of LANGS) await shootLang(browser, lang);
  } finally {
    await browser.close();
    preview.kill();
    fs.rmSync(dist, { recursive: true, force: true });
  }
}

async function shootLang(chrome, lang) {
  // a fresh profile per language: the app remembers UI state (such as the open Advanced section) in localStorage
  const browser = await chrome.createBrowserContext();
  const d = dict(lang);
  const dir = path.join(OUT, lang);
  fs.mkdirSync(dir, { recursive: true });
  const url = (query = "") => `${BASE}?demo=1&lang=${lang}&theme=light${query}`;

  const open = async (query = "", theme = "light") => {
    const page = await browser.newPage();
    await page.setViewport({ width: 1280, height: 800, deviceScaleFactor: 1 });
    await page.emulateMediaFeatures([{ name: "prefers-color-scheme", value: theme }]);
    await page.goto(url(query).replace("theme=light", `theme=${theme}`), { waitUntil: "networkidle0" });
    // no transitions or spinners mid-frame, no focus ring left over from a click
    await page.addStyleTag({ content: "*,*::before,*::after{transition:none!important;animation:none!important;caret-color:transparent!important}" });
    return page;
  };
  const shot = async (page, name) => {
    await page.evaluate(() => document.activeElement?.blur?.());
    await sleep(150);
    const file = path.join(dir, `${name}.webp`);
    await page.screenshot({ path: file, type: "webp", quality: QUALITY });
    console.log(`${lang}/${name}.webp  ${(fs.statSync(file).size / 1024).toFixed(0)} KB`);
  };
  const nav = (page, key) => click(page, d[key]);

  // overview, with the subscription card
  let page = await open();
  await waitText(page, "DEVELOPERPACK_E5");
  await shot(page, "overview");
  await click(page, d["sub.history"].replace("{count}", "2"));
  await shot(page, "subscription");
  await page.close();

  page = await open("", "dark");
  await waitText(page, "DEVELOPERPACK_E5");
  await shot(page, "overview-dark");
  await page.close();

  // accounts list
  page = await open();
  await nav(page, "nav.accounts");
  await waitText(page, "contoso-app");
  await shot(page, "accounts");

  // add-account wizard: automatic app registration
  await click(page, d["accounts.add"]);
  await waitText(page, d["auto.route.auto"]);
  await shot(page, "wizard-method");
  await page.type("#a-name", "contoso-dev");
  await click(page, d["wizard.next"]);
  await waitText(page, "K4PD-82MQ");
  await shot(page, "admin-signin");
  await waitText(page, d["auto.planTitle"]);
  await shot(page, "plan");
  await click(page, d["auto.confirm"]);
  await sleep(2300);
  await shot(page, "progress");
  await waitText(page, d["auto.doneTitle"]);
  await shot(page, "app-ready");
  await click(page, d["wizard.saveAndLogin"]);
  await waitText(page, d["wizard.loginDone"]);
  await click(page, d["wizard.next"]);
  await waitText(page, d["rec.badge.unavailable"]);
  await shot(page, "api-categories");
  await click(page, d["wizard.saveAndTest"]);
  await waitText(page, d["wizard.testOk"]);
  await shot(page, "test");
  await page.close();

  // settings: running + pace, backup, alerts
  page = await open("&page=settings");
  await waitText(page, d["settings.advanced"]);
  await shot(page, "settings");
  await scrollToText(page, d["settings.backup"]);
  await shot(page, "backup");
  await click(page, d["settings.advanced"]);
  await waitText(page, "Telegram");
  await scrollToText(page, d["settings.notify"]);
  await shot(page, "alerts");
  await page.close();

  // health check
  page = await open("&page=health");
  await waitText(page, d["health.recheck"]);
  await sleep(1500);
  await shot(page, "health");
  await page.close();
  await browser.close();
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
