import { describe, expect, it } from "vitest";
import {
  CHANNEL_EVENTS, CHANNEL_GROUPS, CHANNEL_TYPES, PLAIN_FIELDS, PREVIEW_EVENTS, SECRET_FIELDS, emptyChannel, isValidCooldown,
  isValidQuietHours, quietHours, splitList, splitQuietHours, suggestId, validateChannel,
} from "./notify";
import { NOTIFY_EVENTS } from "./settings";
import { dictionaries } from "@/i18n";

const opts = { creating: true, takenIds: [] as string[], secretSet: [] as string[] };

describe("channel metadata", () => {
  it("lists every type once, in exactly one group", () => {
    const grouped = CHANNEL_GROUPS.flatMap((g) => g.types);
    expect([...grouped].sort()).toEqual([...CHANNEL_TYPES].sort());
    expect(new Set(grouped).size).toBe(grouped.length);
    for (const t of CHANNEL_TYPES) {
      expect(SECRET_FIELDS[t], t).toBeDefined();
      expect(PLAIN_FIELDS[t], t).toBeDefined();
    }
  });
  it("offers the same events as the settings page", () => {
    expect([...CHANNEL_EVENTS]).toEqual([...NOTIFY_EVENTS]);
    expect(PREVIEW_EVENTS).toContain("test");
    expect(PREVIEW_EVENTS).toContain("digest");
  });
  it("has a label for every type, event, secret and field in both languages", () => {
    for (const d of Object.values(dictionaries)) {
      for (const t of CHANNEL_TYPES) expect(d[`alerts.type.${t}` as keyof typeof d], t).toBeTruthy();
      for (const ev of PREVIEW_EVENTS) expect(d[`event.${ev}` as keyof typeof d], ev).toBeTruthy();
      for (const t of CHANNEL_TYPES) {
        for (const s of SECRET_FIELDS[t]) expect(d[`alerts.secret.${s.name}` as keyof typeof d], s.name).toBeTruthy();
        for (const f of PLAIN_FIELDS[t]) expect(d[`alerts.f.${f.key}` as keyof typeof d], f.key).toBeTruthy();
      }
    }
  });
});

describe("validateChannel", () => {
  const withSecrets = (type: Parameters<typeof emptyChannel>[0], patch: Record<string, unknown>, secrets: Record<string, string>) => ({
    ...emptyChannel(type), id: "x", ...patch, secrets,
  }) as ReturnType<typeof emptyChannel>;

  it("accepts a complete telegram channel", () => {
    expect(validateChannel(withSecrets("telegram", { chatId: "1" }, { bot_token: "123:abc" }), opts)).toEqual({});
  });
  it("checks the id", () => {
    const c = withSecrets("slack", {}, { url: "https://h.example/x" });
    expect(validateChannel({ ...c, id: "Bad ID" }, opts).id).toBe("id");
    expect(validateChannel({ ...c, id: "" }, opts).id).toBe("id");
    expect(validateChannel({ ...c, id: "team" }, { ...opts, takenIds: ["team"] }).id).toBe("idTaken");
    // An edit does not look at the id.
    expect(validateChannel({ ...c, id: "team" }, { ...opts, creating: false, takenIds: ["team"] }).id).toBeUndefined();
  });
  it("requires the plain and the secret fields", () => {
    const e = validateChannel(emptyChannel("telegram"), { ...opts });
    expect(e.chatId).toBe("required");
    expect(e["secrets.bot_token"]).toBe("required");
    const m = validateChannel({ ...emptyChannel("email"), id: "m" }, opts);
    expect(m.smtpHost).toBe("required");
    expect(m.from).toBe("required");
    expect(m.to).toBe("required");
    expect(m["secrets.password"]).toBeUndefined(); // optional
    expect(validateChannel({ ...emptyChannel("ntfy"), id: "n" }, opts).topic).toBe("required");
  });
  it("lets an edit keep the stored secret, unless it is cleared", () => {
    const c = { ...emptyChannel("slack"), id: "s" };
    expect(validateChannel(c, { creating: false, takenIds: [], secretSet: ["url"] })).toEqual({});
    expect(validateChannel({ ...c, clearSecrets: ["url"] }, { creating: false, takenIds: [], secretSet: ["url"] })["secrets.url"]).toBe("required");
  });
  it("requires https addresses and never lets http through", () => {
    for (const bad of ["http://h.example/x", "not a url", "ftp://h.example"]) {
      expect(validateChannel(withSecrets("wecom", {}, { url: bad }), opts)["secrets.url"], bad).toBe("https");
    }
    expect(validateChannel(withSecrets("telegram", { chatId: "1", apiBase: "http://proxy" }, { bot_token: "x" }), opts).apiBase).toBe("https");
  });
  it("checks mail addresses, the port and the ntfy topic", () => {
    const m = withSecrets("email", { smtpHost: "h", from: "Alerts <a@b.co>", to: ["c@d.co", "oops"], smtpPort: 70000 }, {});
    const e = validateChannel(m, opts);
    expect(e.to).toBe("email");
    expect(e.smtpPort).toBe("port");
    expect(e.from).toBeUndefined();
    expect(validateChannel(withSecrets("ntfy", { topic: "a b" }, {}), opts).topic).toBe("topic");
  });
});

describe("helpers", () => {
  it("suggests a free id", () => {
    expect(suggestId("telegram", [])).toBe("telegram");
    expect(suggestId("telegram", ["telegram"])).toBe("telegram-2");
    expect(suggestId("telegram", ["telegram", "telegram-2"])).toBe("telegram-3");
  });
  it("splits address lists", () => {
    expect(splitList("a@b.co, c@d.co;e@f.co\ng@h.co")).toEqual(["a@b.co", "c@d.co", "e@f.co", "g@h.co"]);
    expect(splitList("  ")).toEqual([]);
  });
  it("validates quiet hours and cooldown", () => {
    expect(isValidQuietHours("")).toBe(true);
    expect(isValidQuietHours("23:00-08:00")).toBe(true);
    for (const bad of ["23:00", "8:00-9:00", "08:00-08:00", "25:00-26:00"]) expect(isValidQuietHours(bad), bad).toBe(false);
    expect(isValidCooldown("6h")).toBe(true);
    expect(isValidCooldown("0")).toBe(true);
    expect(isValidCooldown("")).toBe(false);
    expect(isValidCooldown("soon")).toBe(false);
  });
  it("joins and splits the quiet window", () => {
    expect(quietHours("23:00", "08:00")).toBe("23:00-08:00");
    expect(quietHours("23:00", "")).toBe("");
    expect(splitQuietHours("23:00-08:00")).toEqual(["23:00", "08:00"]);
    expect(splitQuietHours("")).toEqual(["", ""]);
  });
});
