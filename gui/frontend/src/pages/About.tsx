import { ExternalLink, RefreshCw, ShieldAlert } from "lucide-react";
import { api } from "@/api";
import { APP_VERSION, AppName, DOCS_URL, ISSUES_URL, PROJECT_URL } from "@/config";
import { useI18n } from "@/i18n";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { PageHeader, Spinner } from "@/components/common";
import { UpdateBanner } from "@/components/UpdateBanner";
import { useUpdate } from "@/state/update";
import { formatWhen } from "@/lib/time";

/** The version row of the About page, with "check now"; an available upgrade shows below it. */
function UpdateCard() {
  const { t } = useI18n();
  const u = useUpdate();
  const { info } = u;
  const status = !info
    ? ""
    : info.dev
      ? t("update.about.dev")
      : info.available
        ? ""
        : info.latest
          ? t("update.about.upToDate")
          : t("update.about.unknown");
  return (
    <>
      <Card className="mb-4">
        <CardHeader><CardTitle>{t("update.about.title")}</CardTitle></CardHeader>
        <CardContent className="space-y-3">
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1.5 text-[13px]">
            <dt className="text-muted-foreground">{t("update.about.current")}</dt>
            <dd className="tabular-nums">{info?.current ?? APP_VERSION}</dd>
            <dt className="text-muted-foreground">{t("update.about.latest")}</dt>
            <dd className="tabular-nums">
              {info?.latest || "—"}
              {info?.prerelease ? ` (${t("update.about.prerelease")})` : ""}
            </dd>
            <dt className="text-muted-foreground">{t("update.about.checkedAt")}</dt>
            <dd>{info?.checkedAt ? formatWhen(info.checkedAt) : t("update.about.never")}</dd>
          </dl>
          {status && <p className="text-[13px] text-muted-foreground">{status}</p>}
          {info && !info.checkEnabled && <p className="text-[13px] text-muted-foreground">{t("update.about.checkOff")}</p>}
          {u.checkError && <p role="alert" className="break-words text-[13px] text-danger">{t("update.about.checkFailed", { error: u.checkError })}</p>}
          <Button size="sm" variant="outline" disabled={u.checking || u.install.phase === "running"} onClick={() => void u.check(true)}>
            {u.checking ? <Spinner /> : <RefreshCw />} {u.checking ? t("update.about.checking") : t("update.about.checkNow")}
          </Button>
        </CardContent>
      </Card>
      <UpdateBanner always />
    </>
  );
}

export function About() {
  const { t } = useI18n();
  const links = [
    { label: t("about.project"), url: PROJECT_URL },
    { label: t("about.docs"), url: DOCS_URL },
    { label: t("about.issues"), url: ISSUES_URL },
  ];
  return (
    <>
      <PageHeader title={t("nav.about")} />
      <Card className="mb-4">
        <CardContent className="flex items-center gap-4 pt-4">
          <div className="flex size-14 items-center justify-center rounded-xl bg-primary text-xl font-bold text-primary-foreground" aria-hidden>E5</div>
          <div>
            <p className="text-lg font-semibold">{AppName}</p>
            <p className="text-[13px] text-muted-foreground">{t("about.tagline")}</p>
            <p className="mt-1 text-xs text-muted-foreground tabular-nums">{t("about.version")} {APP_VERSION}</p>
          </div>
        </CardContent>
      </Card>
      <UpdateCard />
      <Card className="mb-4">
        <CardHeader><CardTitle>{t("about.links")}</CardTitle></CardHeader>
        <CardContent className="flex flex-wrap gap-2">
          {links.map((l) => (
            <Button key={l.url} variant="outline" size="sm" onClick={() => void api.system.openExternal(l.url)}>
              <ExternalLink /> {l.label}
            </Button>
          ))}
        </CardContent>
      </Card>
      <Card className="border-warning/40">
        <CardHeader><CardTitle className="flex items-center gap-2"><ShieldAlert className="size-4 text-warning" aria-hidden /> {t("about.disclaimer")}</CardTitle></CardHeader>
        <CardContent>
          <ul className="list-disc space-y-1.5 pl-5 text-[13px]">
            <li>{t("about.d1")}</li>
            <li>{t("about.d2")}</li>
            <li>{t("about.d3")}</li>
            <li>{t("about.d4")}</li>
          </ul>
        </CardContent>
      </Card>
    </>
  );
}
