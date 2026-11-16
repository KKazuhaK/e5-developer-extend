import type { DictKey } from "@/i18n";

export interface TitleSpec {
  key: DictKey;
  params?: Record<string, string>;
}

const FIXED: Record<string, DictKey> = {
  config: "doctor.config", vault: "doctor.vault", engine: "doctor.engine", service: "doctor.service",
  linger: "doctor.linger", clock: "doctor.clock", network: "doctor.network", notify: "doctor.notify",
};
const PER_ACCOUNT: Record<string, DictKey> = {
  token: "doctor.account.token", scopes: "doctor.account.scopes", activity: "doctor.account.activity",
  state: "doctor.account.state", secret: "doctor.account.secret",
};

/**
 * Maps a DoctorCheck.id to a localized title spec: the fixed ids, or
 * "account.<name>.<token|scopes|activity|state|secret>". Null for unknown ids
 * (the caller then shows the backend's English title).
 */
export function doctorTitle(id: string): TitleSpec | null {
  if (FIXED[id]) return { key: FIXED[id] };
  const m = /^account\.(.+)\.(token|scopes|activity|state|secret)$/.exec(id);
  if (m) return { key: PER_ACCOUNT[m[2]], params: { name: m[1] } };
  return null;
}
