import type { TaskPlanningDraft } from "@/lib/task-schedules";
import { scheduleError, shiftSchedule } from "@/lib/task-schedules";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

type ScheduleDraft = Pick<
  TaskPlanningDraft,
  "planned_start_date" | "planned_finish_date"
>;

export function TaskScheduleFields({
  draft,
  onChange,
}: {
  draft: ScheduleDraft;
  onChange: (patch: Partial<ScheduleDraft>) => void;
}) {
  const error = scheduleError(
    draft.planned_start_date,
    draft.planned_finish_date,
  );
  return (
    <section
      aria-label="Planned schedule"
      className="space-y-3 rounded-lg border p-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-medium">Planned schedule</h2>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={!draft.planned_start_date && !draft.planned_finish_date}
          onClick={() =>
            onChange({ planned_start_date: "", planned_finish_date: "" })
          }
        >
          Clear schedule
        </Button>
      </div>
      <div className="grid min-w-0 gap-4 sm:grid-cols-2">
        {(
          [
            ["planned_start_date", "Planned start"],
            ["planned_finish_date", "Planned finish"],
          ] as const
        ).map(([field, label]) => (
          <div key={field} className="grid min-w-0 gap-2">
            <Label htmlFor={`detail-${field}`}>{label}</Label>
            <Input
              id={`detail-${field}`}
              type="date"
              min="0001-01-01"
              max="9999-12-31"
              value={draft[field]}
              onChange={(event) => onChange({ [field]: event.target.value })}
              aria-invalid={!!error}
              aria-describedby={
                error
                  ? "detail-schedule-help detail-schedule-error"
                  : "detail-schedule-help"
              }
              className="min-w-0"
            />
          </div>
        ))}
      </div>
      <div className="flex flex-wrap gap-2">
        {([-1, 1] as const).map((days) => {
          const shifted = shiftSchedule(
            draft.planned_start_date,
            draft.planned_finish_date,
            days,
          );
          return (
            <Button
              key={days}
              type="button"
              variant="outline"
              size="sm"
              disabled={!shifted}
              onClick={() => shifted && onChange(shifted)}
            >
              {days < 0 ? "Earlier" : "Later"} by one day
            </Button>
          );
        })}
      </div>
      <p id="detail-schedule-help" className="text-xs text-muted-foreground">
        Finish is inclusive. Add both dates to schedule this Task; either date
        can be left empty.
      </p>
      {error && (
        <p
          id="detail-schedule-error"
          role="alert"
          className="text-sm text-destructive"
        >
          {error}
        </p>
      )}
    </section>
  );
}
