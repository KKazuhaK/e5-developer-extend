import { useCallback, useEffect, useMemo, useState } from "react";
import {
  AlertTriangle, BellRing, Bell, CheckCircle2, Eye, Globe, Mail, MessageCircle, Pencil, Plus, Send, Smartphone, Trash2, XCircle,
} from "lucide-react";
import { api } from "@/api";
import type { ChannelInput, ChannelType, ChannelView, EmailPreset, MessagePreview, Settings } from "@/api";
import { useI18n, type DictKey } from "@/i18n";
import { useToast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Field, Spinner, fieldAria } from "@/components/common";
import { formatRelative } from "@/lib/time";
import {
  CHANNEL_EVENTS, CHANNEL_GROUPS, LEVELS, PLAIN_FIELDS, PREVIEW_EVENTS, SECRET_FIELDS, emptyChannel, quietHours, splitList, splitQuietHours,
  suggestId, validateChannel, type ChannelCode,
} from "@/lib/notify";
import { NOTIFY_EVENTS } from "@/lib/settings";
import { cn, errMsg } from "@/lib/utils";

const TYPE_ICON: Record<ChannelType, typeof Bell> = {
  telegram: Send, discord: MessageCircle, slack: MessageCircle, wecom: MessageCircle, feishu: MessageCircle, dingtalk: MessageCircle,
  bark: Smartphone, ntfy: Smartphone, serverchan: Smartphone, email: Mail, webhook: Globe,
};
const CODE_MSG: Record<ChannelCode, DictKey> = {
  id: "alerts.err.id", idTaken: "alerts.err.idTaken", required: "alerts.err.required", https: "alerts.err.https",
  email: "alerts.err.email", topic: "alerts.err.topic", port: "alerts.err.port",
};
const textarea =
  "flex min-h-28 w-full rounded-md border border-input bg-card px-3 py-2 font-mono text-[13px] shadow-xs transition-colors placeholder:text-muted-foreground/70 aria-invalid:border-destructive aria-invalid:ring-1 aria-invalid:ring-destructive/40";

/** The Alerts card of the advanced settings: channels, what to alert on, and how loud. */
export function AlertsCard({
  draft, upd, fieldErr, clearRemote, onChannelsChanged,
}: {
  draft: Settings; upd: (fn: (d: Settings) => void) => void; fieldErr: (f: string) => string | undefined;
  clearRemote: (f: string) => void; onChannelsChanged: (count: number, webhook: boolean) => void;
}) {
  const { t, lang } = useI18n();
  const toast = useToast();
  const [channels, setChannels] = useState<ChannelView[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState<{ view: ChannelView | null } | null>(null); // view null: adding
  const [previewing, setPreviewing] = useState<ChannelView | null>(null);
  const [deleting, setDeleting] = useState<ChannelView | null>(null);
  const [busy, setBusy] = useState<string | null>(null); // "test:<id>" | "test:" | "delete"

  const reload = useCallback(async () => {
    try {
      const list = await api.settings.listChannels();
      setChannels(list);
      setError(null);
      onChannelsChanged(list.length, list.some((c) => c.id === "webhook"));
    } catch (e) {
      setError(errMsg(e));
    }
  }, [onChannelsChanged]);
  useEffect(() => {
    void reload();
  }, [reload]);

  const test = async (id: string) => {
    setBusy(`test:${id}`);
    try {
      const res = await api.settings.testChannel(id);
      for (const r of res) {
        if (r.ok) toast.success(t("alerts.testOk", { id: r.id }));
        else toast.error(t("alerts.testFail", { id: r.id, error: r.error }));
      }
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setBusy(null);
      void reload();
    }
  };
  const remove = async () => {
    if (!deleting) return;
    setBusy("delete");
    try {
      await api.settings.removeChannel(deleting.id);
      toast.success(t("alerts.removed"));
      setDeleting(null);
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setBusy(null);
      void reload();
    }
  };

  const n = draft.notify;
  const [qFrom, qTo] = splitQuietHours(n.quietHours);
  const setQuiet = (from: string, to: string) => {
    clearRemote("notify.quietHours");
    upd((d) => void (d.notify.quietHours = quietHours(from, to)));
  };

  return (
    <Card id="alerts-card">
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><BellRing className="size-4" aria-hidden /> {t("settings.notify")}</CardTitle>
        <CardDescription>{t("settings.notifyDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-6">
        <section aria-labelledby="alerts-ch-l" className="grid gap-3">
          <div className="flex items-center justify-between gap-2">
            <h3 id="alerts-ch-l" className="text-[13px] font-semibold">{t("alerts.channels")}</h3>
            <div className="flex gap-2">
              {channels && channels.length > 1 && (
                <Button size="sm" variant="outline" onClick={() => void test("")} disabled={busy !== null}>
                  {busy === "test:" ? <Spinner /> : <Send />} {t("alerts.testAll")}
                </Button>
              )}
              <Button size="sm" onClick={() => setEditing({ view: null })}><Plus /> {t("alerts.add")}</Button>
            </div>
          </div>
          {error && <p role="alert" className="text-xs text-danger">{error}</p>}
          {channels && channels.length === 0 && (
            <div className="rounded-lg border border-dashed px-4 py-6 text-center">
              <p className="text-[13px] font-medium">{t("alerts.none")}</p>
              <p className="mt-1 text-xs text-muted-foreground">{t("alerts.noneHint")}</p>
            </div>
          )}
          <ul className="grid gap-2">
            {(channels ?? []).map((c) => (
              <ChannelRow
                key={c.id} c={c} lang={lang} busy={busy}
                onTest={() => void test(c.id)} onPreview={() => setPreviewing(c)} onEdit={() => setEditing({ view: c })} onDelete={() => setDeleting(c)}
              />
            ))}
          </ul>
        </section>

        <section aria-labelledby="alerts-noise-l" className="grid gap-4">
          <div>
            <h3 id="alerts-noise-l" className="text-[13px] font-semibold">{t("alerts.noise")}</h3>
            <p className="text-xs text-muted-foreground">{t("alerts.noiseDesc")}</p>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="n-stale" label={t("settings.staleAfter")} hint={t("settings.staleHint")} error={fieldErr("notify.staleAfter")}>
              <Input {...fieldAria("n-stale", fieldErr("notify.staleAfter"), "x")} value={n.staleAfter} onChange={(e) => { clearRemote("notify.staleAfter"); upd((d) => void (d.notify.staleAfter = e.target.value)); }} />
            </Field>
            <Field id="n-cooldown" label={t("alerts.cooldown")} hint={t("alerts.cooldownHint")} error={fieldErr("notify.cooldown")}>
              <Input {...fieldAria("n-cooldown", fieldErr("notify.cooldown"), "x")} value={n.cooldown} onChange={(e) => { clearRemote("notify.cooldown"); upd((d) => void (d.notify.cooldown = e.target.value)); }} />
            </Field>
            <div className="sm:col-span-2">
              <p className="mb-1.5 text-[13px] font-medium" id="n-quiet-l">{t("alerts.quiet")}</p>
              <div role="group" aria-labelledby="n-quiet-l" className="flex flex-wrap items-center gap-2">
                <label className="flex items-center gap-2 text-[13px]">{t("alerts.quietFrom")}
                  <Input id="n-quiet" type="time" className="w-32" value={qFrom} aria-invalid={fieldErr("notify.quietHours") ? true : undefined} onChange={(e) => setQuiet(e.target.value, qTo)} />
                </label>
                <label className="flex items-center gap-2 text-[13px]">{t("alerts.quietTo")}
                  <Input type="time" className="w-32" value={qTo} aria-invalid={fieldErr("notify.quietHours") ? true : undefined} onChange={(e) => setQuiet(qFrom, e.target.value)} />
                </label>
                {n.quietHours && <Button size="sm" variant="ghost" onClick={() => setQuiet("", "")}>{t("alerts.quietClear")}</Button>}
              </div>
              <p className={cn("mt-1.5 text-xs", fieldErr("notify.quietHours") ? "text-danger" : "text-muted-foreground")} role={fieldErr("notify.quietHours") ? "alert" : undefined}>
                {fieldErr("notify.quietHours") ?? t("alerts.quietHint")}
              </p>
            </div>
          </div>

          <div>
            <p className="text-[13px] font-medium">{t("alerts.spike")}</p>
            <p className="mb-2 text-xs text-muted-foreground">{t("alerts.spikeHint")}</p>
            <div className="grid gap-4 sm:grid-cols-3">
              <Field id="n-spike-rounds" label={t("alerts.spikeRounds")} error={fieldErr("notify.spikeRounds")}>
                <Input {...fieldAria("n-spike-rounds", fieldErr("notify.spikeRounds"))} type="number" min={1} max={50} value={Number.isNaN(n.spikeRounds) ? "" : n.spikeRounds} onChange={(e) => { clearRemote("notify.spikeRounds"); upd((d) => void (d.notify.spikeRounds = e.target.valueAsNumber)); }} />
              </Field>
              <Field id="n-spike-rate" label={t("alerts.spikeRate")} error={fieldErr("notify.spikeRate")}>
                <Input {...fieldAria("n-spike-rate", fieldErr("notify.spikeRate"))} type="number" min={1} max={100} value={Number.isNaN(n.spikeRate) ? "" : Math.round(n.spikeRate * 100)} onChange={(e) => { clearRemote("notify.spikeRate"); upd((d) => void (d.notify.spikeRate = e.target.valueAsNumber / 100)); }} />
              </Field>
              <Field id="n-spike-min" label={t("alerts.spikeMin")} error={fieldErr("notify.spikeMinFailures")}>
                <Input {...fieldAria("n-spike-min", fieldErr("notify.spikeMinFailures"))} type="number" min={1} value={Number.isNaN(n.spikeMinFailures) ? "" : n.spikeMinFailures} onChange={(e) => { clearRemote("notify.spikeMinFailures"); upd((d) => void (d.notify.spikeMinFailures = e.target.valueAsNumber)); }} />
              </Field>
            </div>
          </div>

          <div>
            <p className="mb-1.5 text-[13px] font-medium" id="n-ev-l">{t("alerts.moreEvents")}</p>
            <div role="group" aria-labelledby="n-ev-l" className="grid gap-1.5 sm:grid-cols-2">
              {NOTIFY_EVENTS.map((ev) => (
                <label key={ev} className="flex cursor-pointer items-center gap-2 text-[13px]">
                  <Checkbox
                    checked={n.events.includes(ev)}
                    onCheckedChange={(v) => { clearRemote("notify.events"); upd((d) => void (d.notify.events = v ? NOTIFY_EVENTS.filter((x) => x === ev || d.notify.events.includes(x)) : d.notify.events.filter((x) => x !== ev))); }}
                  />
                  {t(`event.${ev}` as DictKey)}
                </label>
              ))}
            </div>
            <p className="mt-1.5 text-xs text-muted-foreground">{fieldErr("notify.events") ?? t("settings.eventsHint")}</p>
          </div>
        </section>
      </CardContent>

      {editing && (
        <ChannelDialog
          view={editing.view} taken={(channels ?? []).map((c) => c.id)}
          onClose={() => setEditing(null)} onSaved={() => { setEditing(null); void reload(); }}
        />
      )}
      {previewing && <PreviewDialog view={previewing} onClose={() => setPreviewing(null)} />}
      <Dialog open={deleting !== null} onOpenChange={(o) => !o && setDeleting(null)}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle>{t("alerts.deleteTitle", { id: deleting?.id ?? "" })}</DialogTitle>
            <DialogDescription>{t("alerts.deleteBody")}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleting(null)}>{t("common.cancel")}</Button>
            <Button variant="destructive" onClick={() => void remove()} disabled={busy === "delete"}>
              {busy === "delete" ? <Spinner /> : <Trash2 />} {t("alerts.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

function ChannelRow({
  c, lang, busy, onTest, onPreview, onEdit, onDelete,
}: {
  c: ChannelView; lang: "zh" | "en"; busy: string | null; onTest: () => void; onPreview: () => void; onEdit: () => void; onDelete: () => void;
}) {
  const { t } = useI18n();
  const Icon = TYPE_ICON[c.type];
  const missing = c.secretFields.filter((f) => f.required && !f.set);
  return (
    <li className="rounded-lg border bg-card p-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
            <span className="truncate">{c.id}</span>
            <Badge tone="outline">{t(`alerts.type.${c.type}` as DictKey)}</Badge>
            <Badge tone="muted">{t(`alerts.level.${c.minLevel || "any"}` as DictKey)}</Badge>
            {c.events.length > 0 && <Badge tone="muted">{c.events.length} / {CHANNEL_EVENTS.length}</Badge>}
            {missing.length > 0 && <Badge tone="danger">{t("alerts.secretUnset")}: {missing.map((f) => t(`alerts.secret.${f.name}` as DictKey)).join(", ")}</Badge>}
          </p>
          <p className="mt-1 flex items-center gap-1 text-xs text-muted-foreground">
            {c.lastResult ? (
              c.lastResult.ok ? (
                <><CheckCircle2 className="size-3.5 text-success" aria-hidden /> {t("alerts.lastOk", { time: formatRelative(c.lastResult.at, lang) })}</>
              ) : (
                <><XCircle className="size-3.5 text-danger" aria-hidden /> {t("alerts.lastFail", { time: formatRelative(c.lastResult.at, lang) })}{c.lastResult.error ? `: ${c.lastResult.error}` : ""}</>
              )
            ) : (
              t("alerts.lastNone")
            )}
          </p>
          {c.warnings.map((w) => (
            <p key={w} className="mt-1 flex items-center gap-1 text-xs text-warning"><AlertTriangle className="size-3.5" aria-hidden /> {t(`alerts.warn.${w}` as DictKey)}</p>
          ))}
        </div>
        <div className="flex flex-wrap gap-1.5">
          <Button size="sm" variant="outline" onClick={onTest} disabled={busy !== null} aria-label={`${t("alerts.test")} ${c.id}`}>
            {busy === `test:${c.id}` ? <Spinner /> : <Send />} {t("alerts.test")}
          </Button>
          <Button size="sm" variant="outline" onClick={onPreview} aria-label={`${t("alerts.preview")} ${c.id}`}><Eye /> {t("alerts.preview")}</Button>
          <Button size="sm" variant="outline" onClick={onEdit} aria-label={`${t("alerts.edit")} ${c.id}`}><Pencil /> {t("alerts.edit")}</Button>
          <Button size="sm" variant="ghost" onClick={onDelete} aria-label={`${t("alerts.delete")} ${c.id}`}><Trash2 /></Button>
        </div>
      </div>
    </li>
  );
}

/** Add (view null) or edit a channel. Step one of adding picks the type. */
function ChannelDialog({
  view, taken, onClose, onSaved,
}: { view: ChannelView | null; taken: string[]; onClose: () => void; onSaved: () => void }) {
  const { t } = useI18n();
  const toast = useToast();
  const creating = view === null;
  const [type, setType] = useState<ChannelType | null>(view?.type ?? null);
  const [c, setC] = useState<ChannelInput | null>(view ? { ...structuredClone(view), secrets: {}, clearSecrets: [] } : null);
  const [remote, setRemote] = useState<Record<string, string>>({});
  const [showLocal, setShowLocal] = useState(false);
  const [saving, setSaving] = useState(false);
  const [presets, setPresets] = useState<EmailPreset[]>([]);
  const [previewFor, setPreviewFor] = useState<ChannelInput | null>(null);
  const secretSet = useMemo(() => (view?.secretFields ?? []).filter((f) => f.set).map((f) => f.name), [view]);

  useEffect(() => {
    if (type === "email") void api.settings.emailPresets().then(setPresets).catch(() => setPresets([]));
  }, [type]);

  const pick = (ty: ChannelType) => {
    setType(ty);
    setC({ ...emptyChannel(ty), id: suggestId(ty, taken) });
  };
  const upd = (patch: Partial<ChannelInput>) => {
    setC((x) => x && { ...x, ...patch });
    setRemote((r) => {
      const next = { ...r };
      for (const k of Object.keys(patch)) delete next[k];
      return next;
    });
  };
  const setSecret = (name: string, v: string) => {
    setC((x) => x && { ...x, secrets: { ...x.secrets, [name]: v }, clearSecrets: x.clearSecrets.filter((n) => n !== name) });
    setRemote((r) => ({ ...r, [`secrets.${name}`]: "" }));
  };

  const local = c ? validateChannel(c, { creating, takenIds: taken, secretSet }) : {};
  const err = (f: string): string | undefined => {
    if (remote[f]) return remote[f];
    const code = showLocal ? (local as Record<string, ChannelCode>)[f] : undefined;
    return code ? t(CODE_MSG[code]) : undefined;
  };

  const save = async () => {
    if (!c) return;
    setShowLocal(true);
    if (Object.keys(local).length) return;
    setSaving(true);
    try {
      const bad = await api.settings.saveChannel(c, creating);
      if (bad.length) {
        const m: Record<string, string> = {};
        for (const b of bad) m[b.field] ||= b.message;
        setRemote(m);
        return;
      }
      toast.success(t("alerts.saved"));
      onSaved();
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent closeLabel={t("common.close")} className="max-w-xl">
        {!c || !type ? (
          <>
            <DialogHeader>
              <DialogTitle>{t("alerts.addTitle")}</DialogTitle>
              <DialogDescription>{t("alerts.addDesc")}</DialogDescription>
            </DialogHeader>
            <div className="grid gap-4">
              {CHANNEL_GROUPS.map((g) => (
                <div key={g.id}>
                  <p className="mb-1.5 text-xs font-medium text-muted-foreground">{t(`alerts.group.${g.id}` as DictKey)}</p>
                  <div className="grid gap-2 sm:grid-cols-3">
                    {g.types.map((ty) => {
                      const Icon = TYPE_ICON[ty];
                      return (
                        <button
                          key={ty} type="button" onClick={() => pick(ty)}
                          className="flex cursor-pointer items-center gap-2 rounded-lg border bg-card px-3 py-2.5 text-left text-[13px] transition-colors hover:bg-accent"
                        >
                          <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
                          {t(`alerts.type.${ty}` as DictKey)}
                        </button>
                      );
                    })}
                  </div>
                </div>
              ))}
            </div>
          </>
        ) : (
          <form autoComplete="off" className="grid gap-4" onSubmit={(e) => { e.preventDefault(); void save(); }}>
            <DialogHeader>
              <DialogTitle>{creating ? t(`alerts.type.${type}` as DictKey) : t("alerts.editTitle", { id: c.id })}</DialogTitle>
              <DialogDescription>{t(`alerts.help.${type}` as DictKey)}</DialogDescription>
            </DialogHeader>

            {creating && (
              <Field id="c-id" label={t("alerts.f.id")} hint={t("alerts.hint.id")} error={err("id")}>
                <Input {...fieldAria("c-id", err("id"), "x")} value={c.id} spellCheck={false} onChange={(e) => upd({ id: e.target.value })} />
              </Field>
            )}

            {type === "email" && presets.length > 0 && (
              <Field id="c-preset" label={t("alerts.f.preset")}>
                <Select
                  value="none"
                  onValueChange={(v) => {
                    const p = presets.find((x) => x.name === v);
                    if (p) upd({ smtpHost: p.host, smtpPort: p.port, security: p.security });
                  }}
                >
                  <SelectTrigger id="c-preset"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">{t("alerts.f.presetNone")}</SelectItem>
                    {presets.map((p) => <SelectItem key={p.name} value={p.name}>{p.name}</SelectItem>)}
                  </SelectContent>
                </Select>
              </Field>
            )}

            <div className="grid gap-4 sm:grid-cols-2">
              {PLAIN_FIELDS[type].map((f) => {
                const id = `c-${f.key}`;
                const label = t(`alerts.f.${f.key}` as DictKey) + (f.required ? " *" : "");
                const hintKey = `alerts.hint.${f.key}` as DictKey;
                const hint = ["to", "smtpPort", "apiBase", "server", "format"].includes(f.key) ? t(hintKey) : undefined;
                return (
                  <Field key={f.key} id={id} label={label} hint={hint} error={err(f.key)} className={f.kind === "list" ? "sm:col-span-2" : undefined}>
                    {f.kind === "select" ? (
                      <Select value={String(c[f.key] || f.options![0])} onValueChange={(v) => upd({ [f.key]: v } as Partial<ChannelInput>)}>
                        <SelectTrigger id={id}><SelectValue /></SelectTrigger>
                        <SelectContent>
                          {f.options!.map((o) => <SelectItem key={o} value={o}>{t((f.key === "security" ? `alerts.security.${o}` : `alerts.format.${o}`) as DictKey)}</SelectItem>)}
                        </SelectContent>
                      </Select>
                    ) : f.kind === "number" ? (
                      <Input {...fieldAria(id, err(f.key), hint ? "x" : undefined)} type="number" min={0} max={65535} placeholder={f.placeholder} value={c.smtpPort || ""} onChange={(e) => upd({ smtpPort: e.target.value === "" ? 0 : e.target.valueAsNumber })} />
                    ) : f.kind === "list" ? (
                      <Input {...fieldAria(id, err(f.key), "x")} placeholder={f.placeholder} value={c.to.join(", ")} spellCheck={false} onChange={(e) => upd({ to: splitList(e.target.value) })} />
                    ) : (
                      <Input {...fieldAria(id, err(f.key), hint ? "x" : undefined)} placeholder={f.placeholder} value={String(c[f.key])} spellCheck={false} onChange={(e) => upd({ [f.key]: e.target.value } as Partial<ChannelInput>)} />
                    )}
                  </Field>
                );
              })}
            </div>

            {type === "webhook" && view?.allowHttp && (
              <label className="flex cursor-pointer items-start gap-2.5 text-[13px]">
                <Switch checked={c.allowHttp} onCheckedChange={(v) => upd({ allowHttp: v })} />
                <span>
                  {t("alerts.allowHttp")}
                  <span className="block text-xs text-muted-foreground">{t("alerts.allowHttpHint")}</span>
                </span>
              </label>
            )}

            <div className="grid gap-4">
              {SECRET_FIELDS[type].map((f) => {
                const id = `c-s-${f.name}`;
                const isSet = secretSet.includes(f.name);
                const cleared = c.clearSecrets.includes(f.name);
                const e = err(`secrets.${f.name}`);
                return (
                  <Field
                    key={f.name} id={id} label={t(`alerts.secret.${f.name}` as DictKey) + (f.required ? " *" : "")}
                    hint={isSet && !cleared ? t("alerts.secretKeep") : t("alerts.secretHint")} error={e}
                  >
                    <div className="flex items-center gap-2">
                      <Input
                        {...fieldAria(id, e, "x")} type="password" autoComplete="new-password" spellCheck={false}
                        placeholder={isSet && !cleared ? "••••••••" : f.name === "url" ? "https://" : ""}
                        value={c.secrets[f.name] ?? ""} onChange={(ev) => setSecret(f.name, ev.target.value)}
                      />
                      {isSet && !f.required && !cleared && (
                        <Button size="sm" variant="outline" type="button" onClick={() => setC((x) => x && { ...x, clearSecrets: [...x.clearSecrets, f.name], secrets: { ...x.secrets, [f.name]: "" } })}>{t("alerts.secretClear")}</Button>
                      )}
                      {cleared && <Badge tone="warning">{t("alerts.secretCleared")}</Badge>}
                    </div>
                  </Field>
                );
              })}
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field id="c-level" label={t("alerts.f.minLevel")} hint={t("alerts.hint.minLevel")} error={err("minLevel")}>
                <Select value={c.minLevel || "info"} onValueChange={(v) => upd({ minLevel: v === "info" ? "" : (v as ChannelInput["minLevel"]) })}>
                  <SelectTrigger id="c-level"><SelectValue /></SelectTrigger>
                  <SelectContent>{LEVELS.map((l) => <SelectItem key={l} value={l}>{t(`alerts.level.${l}` as DictKey)}</SelectItem>)}</SelectContent>
                </Select>
              </Field>
              {type !== "webhook" || c.format === "template" ? (
                <Field id="c-lang" label={t("alerts.f.language")} hint={t("alerts.hint.language")}>
                  <Select value={c.language || "auto"} onValueChange={(v) => upd({ language: v === "auto" ? "" : (v as ChannelInput["language"]) })}>
                    <SelectTrigger id="c-lang"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="auto">{t("alerts.langAuto")}</SelectItem>
                      <SelectItem value="zh">简体中文</SelectItem>
                      <SelectItem value="en">English</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              ) : null}
            </div>

            <div>
              <p className="mb-1.5 text-[13px] font-medium" id="c-ev-l">{t("alerts.f.events")}</p>
              <div role="group" aria-labelledby="c-ev-l" className="grid gap-1.5 sm:grid-cols-2">
                {CHANNEL_EVENTS.map((ev) => (
                  <label key={ev} className="flex cursor-pointer items-center gap-2 text-[13px]">
                    <Checkbox
                      checked={c.events.includes(ev)}
                      onCheckedChange={(v) => upd({ events: v ? CHANNEL_EVENTS.filter((x) => x === ev || c.events.includes(x)) : c.events.filter((x) => x !== ev) })}
                    />
                    {t(`event.${ev}` as DictKey)}
                  </label>
                ))}
              </div>
              <p className="mt-1.5 text-xs text-muted-foreground">{t("alerts.hint.events")}</p>
            </div>

            {(type !== "webhook" || c.format === "template") && (
              <Field id="c-template" label={t("alerts.f.template")} hint={t("alerts.hint.template")} error={err("template")}>
                <textarea
                  {...fieldAria("c-template", err("template"), "x")} className={textarea} spellCheck={false} value={c.template}
                  placeholder={type === "webhook" ? '{"text": {{json .Message}}}' : "{{.Level}} {{.Account}}: {{.Message}}"}
                  onChange={(e) => upd({ template: e.target.value })}
                />
                <p className="text-xs text-muted-foreground">{t("alerts.templateHelp")}</p>
              </Field>
            )}

            <DialogFooter className="flex-wrap">
              {creating && <Button variant="ghost" type="button" onClick={() => { setType(null); setC(null); }}>{t("alerts.back")}</Button>}
              <Button variant="outline" type="button" onClick={() => setPreviewFor(c)}><Eye /> {t("alerts.preview")}</Button>
              <Button variant="outline" type="button" onClick={onClose}>{t("common.cancel")}</Button>
              <Button type="submit" disabled={saving}>{saving && <Spinner />} {t("common.save")}</Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
      {previewFor && <PreviewDialog view={previewFor} onClose={() => setPreviewFor(null)} />}
    </Dialog>
  );
}

/** Shows the rendered example message of a channel (saved or being edited). */
function PreviewDialog({ view, onClose }: { view: Pick<ChannelInput, "id" | "type" | "language" | "template" | "format">; onClose: () => void }) {
  const { t } = useI18n();
  const [event, setEvent] = useState<string>("failure_spike");
  const [p, setP] = useState<MessagePreview | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setErr(null);
    api.settings.previewMessage(view.type, view.language, view.template, view.format, event).then(
      (r) => live && setP(r),
      (e) => { if (live) { setP(null); setErr(errMsg(e)); } },
    );
    return () => { live = false; };
  }, [view.type, view.language, view.template, view.format, event]);

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent closeLabel={t("common.close")} className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{t("alerts.previewTitle", { id: view.id || t(`alerts.type.${view.type}` as DictKey) })}</DialogTitle>
          <DialogDescription>{t("alerts.previewDesc")}</DialogDescription>
        </DialogHeader>
        <Field id="pv-event" label={t("alerts.previewEvent")}>
          <Select value={event} onValueChange={setEvent}>
            <SelectTrigger id="pv-event"><SelectValue /></SelectTrigger>
            <SelectContent>{PREVIEW_EVENTS.map((ev) => <SelectItem key={ev} value={ev}>{t(`event.${ev}` as DictKey)}</SelectItem>)}</SelectContent>
          </Select>
        </Field>
        {err && <p role="alert" className="text-xs text-danger">{err}</p>}
        {p && (
          <div className="grid gap-3">
            {p.title && (
              <div>
                <p className="mb-1 text-xs font-medium text-muted-foreground">{t("alerts.previewSubject")}</p>
                <p className="rounded-md border bg-muted/40 px-3 py-2 text-[13px] break-words">{p.title}</p>
              </div>
            )}
            <div>
              <p className="mb-1 text-xs font-medium text-muted-foreground">{t("alerts.previewText")} · {p.format}</p>
              <pre className="max-h-64 overflow-auto rounded-md border bg-muted/40 p-3 text-xs break-words whitespace-pre-wrap">{p.body}</pre>
            </div>
            {p.html && (
              <div>
                <p className="mb-1 text-xs font-medium text-muted-foreground">{t("alerts.previewHtml")}</p>
                <pre className="max-h-40 overflow-auto rounded-md border bg-muted/40 p-3 text-xs break-words whitespace-pre-wrap">{p.html}</pre>
              </div>
            )}
          </div>
        )}
        <DialogFooter><Button variant="outline" onClick={onClose}>{t("common.close")}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
