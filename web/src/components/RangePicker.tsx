import { rangesFor } from "@/charts";
import { cn } from "@/lib/cn";
import type { Range } from "@/types";

export function RangePicker({ value, onChange, retentionDays }: { value: Range; onChange: (r: Range) => void; retentionDays?: number }) {
  return (
    <div className="inline-flex rounded-md border border-border p-0.5" role="group" aria-label="Range">
      {rangesFor(retentionDays).map((r) => (
        <button
          key={r.value}
          type="button"
          onClick={() => onChange(r.value)}
          aria-pressed={value === r.value}
          className={cn(
            "rounded px-2 py-0.5 text-xs",
            value === r.value ? "bg-secondary text-foreground" : "text-muted-foreground hover:text-foreground",
          )}
        >
          {r.label}
        </button>
      ))}
    </div>
  );
}
