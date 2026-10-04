import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

const tones: Record<string, string> = {
  succeeded: "border-success/20 bg-success/10 text-success",
  released: "border-success/20 bg-success/10 text-success",
  failed: "border-destructive/20 bg-destructive/10 text-destructive",
  running: "border-primary/20 bg-primary/10 text-primary",
  ready: "border-primary/20 bg-primary/10 text-primary",
  provisioning: "border-warning/20 bg-warning/10 text-warning",
  finalizing: "border-warning/20 bg-warning/10 text-warning",
};

export function RunStatus({ value }: { value: string }) {
  return (
    <Badge
      variant="outline"
      className={cn(
        "status gap-1.5 capitalize",
        tones[value] ?? "text-muted-foreground",
      )}
    >
      <span
        className="size-1.5 shrink-0 rounded-full bg-current"
        aria-hidden="true"
      />
      {value.replaceAll("_", " ")}
    </Badge>
  );
}
