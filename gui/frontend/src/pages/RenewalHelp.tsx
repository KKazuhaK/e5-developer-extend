import { useState } from "react";
import { CircleHelp, ExternalLink } from "lucide-react";
import { api } from "@/api";
import { FAQ_URL, LEGACY_RETIREMENT_URL } from "@/config";
import { useApp } from "@/state/app";
import { useI18n, type DictKey } from "@/i18n";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";

const link = "cursor-pointer text-primary underline-offset-2 hover:underline";

/** The red "Renewal failed?" button of the Overview header and the checklist it opens (same as docs/user/faq.md). */
export function RenewalHelp() {
  const { t } = useI18n();
  const { navigate } = useApp();
  const [open, setOpen] = useState(false);
  const open_ = (url: string) => void api.system.openExternal(url);
  const steps: { key: DictKey; extra?: { label: DictKey; run: () => void } }[] = [
    { key: "renewal.s1", extra: { label: "renewal.s1Link", run: () => open_(LEGACY_RETIREMENT_URL) } },
    { key: "renewal.s2" },
    { key: "renewal.s3" },
    { key: "renewal.s4", extra: { label: "renewal.s4Link", run: () => { setOpen(false); navigate("health"); } } },
    { key: "renewal.s5" },
  ];
  return (
    <>
      <Button
        variant="outline"
        className="border-danger text-danger hover:bg-danger-bg hover:text-danger"
        onClick={() => setOpen(true)}
      >
        <CircleHelp /> {t("renewal.button")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t("common.close")} className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{t("renewal.title")}</DialogTitle>
            <DialogDescription>{t("renewal.desc")}</DialogDescription>
          </DialogHeader>
          <ol className="grid list-decimal gap-2.5 pl-5 text-[13px]">
            {steps.map((s) => (
              <li key={s.key}>
                {t(s.key)}
                {s.extra && (
                  <>
                    {" "}
                    <button type="button" className={link} onClick={s.extra.run}>{t(s.extra.label)}</button>
                  </>
                )}
              </li>
            ))}
          </ol>
          <DialogFooter>
            <Button variant="outline" onClick={() => open_(FAQ_URL)}>
              <ExternalLink /> {t("renewal.faq")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
