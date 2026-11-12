import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/primitives";
import { healthStyles, lifecycleText, reasonText } from "@/health";
import { cn } from "@/lib/cn";
import type { Health, WorkerView } from "@/types";

// HealthBadge shows the health with its icon; the tooltip has the message and one sentence per reason code.
export function HealthBadge({
  health,
  reasons = [],
  message = "",
}: {
  health: Health;
  reasons?: string[];
  message?: string;
}) {
  const s = healthStyles[health] ?? healthStyles.UNKNOWN;
  if (health === "NONE") {
    return null;
  }
  const Icon = s.icon;
  const badge = (
    <Badge className={cn("cursor-default", s.className)} data-health={health}>
      {Icon && <Icon className={cn("h-3 w-3", s.spin && "animate-spin")} />}
      {s.label}
    </Badge>
  );
  if (!message && reasons.length === 0) {
    return badge;
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="inline-flex">{badge}</span>
      </TooltipTrigger>
      <TooltipContent className="max-w-sm space-y-1 text-xs">
        {message && <p>{message}</p>}
        {reasons.map((r) => (
          <p key={r} className="text-muted-foreground">
            <span className="font-mono">{r}</span>: {reasonText(r)}
          </p>
        ))}
      </TooltipContent>
    </Tooltip>
  );
}

// LifecycleBadge is shown only when the worker is not ACTIVE.
export function LifecycleBadge({ lifecycle, reason }: { lifecycle: string; reason?: string }) {
  if (lifecycle === "ACTIVE") {
    return null;
  }
  return (
    <Badge variant="outline" className="normal-case tracking-normal" title={reason}>
      {lifecycleText(lifecycle, reason ?? "")}
    </Badge>
  );
}

export function WorkerStatus({ w }: { w: WorkerView }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      <HealthBadge health={w.health} reasons={w.healthReasons} message={w.healthMessage} />
      <LifecycleBadge lifecycle={w.lifecycle} reason={w.lifecycleReason} />
    </span>
  );
}

export function Dot({ ok, title }: { ok: boolean; title?: string }) {
  return (
    <span
      title={title}
      className={cn("inline-block h-2 w-2 shrink-0 rounded-full", ok ? "bg-success" : "bg-destructive animate-pulse-dot")}
    />
  );
}

// Meter is a small bar of used / limit; red at 90 % and above.
export function Meter({ ratio, className }: { ratio: number | null; className?: string }) {
  const r = ratio === null || !Number.isFinite(ratio) ? 0 : Math.min(1, Math.max(0, ratio));
  return (
    <div className={cn("h-1.5 w-full overflow-hidden rounded-full bg-muted", className)}>
      <div
        className={cn("h-full rounded-full", r >= 0.9 ? "bg-destructive" : r >= 0.75 ? "bg-warning" : "bg-foreground/60")}
        style={{ width: `${r * 100}%` }}
      />
    </div>
  );
}
