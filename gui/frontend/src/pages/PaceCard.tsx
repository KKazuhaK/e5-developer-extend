import { Gauge } from "lucide-react";
import type { Settings } from "@/api";
import { useI18n, type DictKey } from "@/i18n";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { RadioCards } from "@/components/ui/radio-cards";
import { DAYS_FIELDS, PACES, PACE_FIELDS, PACE_IDS, applyDays, applyPace, detectDays, detectPace, type DaysChoice, type PaceId } from "@/lib/pace";

/** Call pace presets and the every-day / weekdays choice. Edits only the draft. */
export function PaceCard({
  draft, upd, clearRemote,
}: { draft: Settings; upd: (fn: (d: Settings) => void) => void; clearRemote: (field: string) => void }) {
  const { t } = useI18n();
  const pace = detectPace(draft.schedule);
  const days = detectDays(draft.schedule.workDays);
  return (
    <Card>
      <CardHeader>
        <CardTitle id="pace-l" className="flex items-center gap-2"><Gauge className="size-4" aria-hidden /> {t("settings.pace")}</CardTitle>
        <CardDescription>{t("settings.paceDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <div className="grid gap-2">
          <RadioCards<PaceId>
            labelledBy="pace-l"
            className="sm:grid-cols-3"
            value={pace === "custom" ? null : pace}
            onChange={(id) => { PACE_FIELDS.forEach(clearRemote); upd((d) => applyPace(d.schedule, id)); }}
            options={PACE_IDS.map((id) => ({
              value: id,
              label: t(`settings.pace${id[0].toUpperCase()}${id.slice(1)}` as DictKey),
              description: t("settings.paceSummary", {
                imin: PACES[id].intervalMin, imax: PACES[id].intervalMax, amin: PACES[id].apisPerRoundMin, amax: PACES[id].apisPerRoundMax,
              }),
            }))}
          />
          {pace === "rescue" && (
            <p role="note" className="rounded-md border border-amber-500/50 bg-amber-500/10 px-3 py-2 text-xs">{t("settings.paceRescueNote")}</p>
          )}
          {pace === "custom" && (
            <p role="status" className="text-xs text-muted-foreground">
              <span className="mr-1.5 rounded-md border bg-muted px-1.5 py-0.5 font-medium text-foreground">{t("settings.paceCustom")}</span>
              {t("settings.paceCustomHint")}
            </p>
          )}
        </div>
        <div className="grid gap-2">
          <p id="days-l" className="text-[13px] font-medium">{t("settings.days")}</p>
          <RadioCards<Exclude<DaysChoice, "custom">>
            labelledBy="days-l"
            className="sm:grid-cols-2"
            value={days === "custom" ? null : days}
            onChange={(c) => { DAYS_FIELDS.forEach(clearRemote); upd((d) => applyDays(d.schedule, c)); }}
            options={[
              { value: "every", label: t("settings.daysEvery") },
              { value: "weekdays", label: t("settings.daysWeekdays") },
            ]}
          />
          {days === "custom" && <p role="status" className="text-xs text-muted-foreground">{t("settings.daysCustomHint")}</p>}
        </div>
      </CardContent>
    </Card>
  );
}
