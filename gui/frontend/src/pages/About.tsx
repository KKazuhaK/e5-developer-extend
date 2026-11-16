import { ExternalLink, ShieldAlert } from "lucide-react";
import { api } from "@/api";
import { APP_VERSION, AppName, DOCS_URL, ISSUES_URL, PROJECT_URL } from "@/config";
import { useI18n } from "@/i18n";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { PageHeader } from "@/components/common";

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
