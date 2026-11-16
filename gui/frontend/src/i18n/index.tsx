import { createContext, useContext, useMemo, type ReactNode } from "react";
import { AppName } from "@/config";
import { zh } from "./zh";
import { en, enOne } from "./en";
import type { Lang } from "@/lib/time";

export type DictKey = keyof typeof zh;
export type Dict = Record<DictKey, string>;
export const dictionaries: Record<Lang, Dict> = { zh, en };

export type TParams = Record<string, string | number>;
export type TFn = (key: DictKey, params?: TParams) => string;

/** Replaces {name} placeholders. {app} is always available. */
export function translate(lang: Lang, key: DictKey, params?: TParams): string {
  // Plurals: English has a singular form for count === 1; Chinese has a single form.
  const one = lang === "en" && params?.count === 1 ? enOne[key] : undefined;
  const raw = one ?? dictionaries[lang][key] ?? dictionaries.zh[key] ?? key;
  const all: TParams = { app: AppName, ...params };
  return raw.replace(/\{(\w+)\}/g, (m, k: string) => (k in all ? String(all[k]) : m));
}

/** Looks up a key that is only known at run time (e.g. an API category id). */
export function translateLoose(lang: Lang, key: string, fallback: string): string {
  return key in dictionaries[lang] ? translate(lang, key as DictKey) : fallback;
}

interface Ctx {
  lang: Lang;
  t: TFn;
  /** For run-time keys with a fallback string. */
  tl: (key: string, fallback: string) => string;
}

const I18nContext = createContext<Ctx | null>(null);

export function I18nProvider({ lang, children }: { lang: Lang; children: ReactNode }) {
  const value = useMemo<Ctx>(
    () => ({
      lang,
      t: (key, params) => translate(lang, key, params),
      tl: (key, fallback) => translateLoose(lang, key, fallback),
    }),
    [lang],
  );
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): Ctx {
  const c = useContext(I18nContext);
  if (!c) throw new Error("useI18n outside I18nProvider");
  return c;
}
