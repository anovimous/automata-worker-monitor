import { Link } from "@tanstack/react-router";
import { Badge } from "@/components/ui/badge";
import { Dot, Meter } from "@/components/badges";
import { bytes, percent } from "@/format";
import type { HostView } from "@/types";

export function fullestDisk(h: HostView): { path: string; ratio: number } | null {
  let best: { path: string; ratio: number } | null = null;
  for (const d of h.metrics?.disks ?? []) {
    if (d.totalBytes > 0) {
      const ratio = (d.totalBytes - d.freeBytes) / d.totalBytes;
      if (!best || ratio > best.ratio) {
        best = { path: d.path, ratio };
      }
    }
  }
  return best;
}

export function memoryUsed(h: HostView): number | null {
  return h.metrics ? h.metrics.memoryTotalBytes - h.metrics.memoryAvailableBytes : null;
}

// HostCard is one host of the hosts strip of the fleet page.
export function HostCard({ h }: { h: HostView }) {
  const m = h.metrics;
  const disk = fullestDisk(h);
  const used = memoryUsed(h);
  return (
    <Link
      to="/hosts/$name"
      params={{ name: h.name }}
      className="block min-w-[220px] flex-1 space-y-2 rounded-lg border border-border bg-card p-3 hover:border-foreground/30"
    >
      <div className="flex items-center gap-2">
        <Dot ok={h.reachable} title={h.reachable ? "reachable" : "the worker machine controller does not answer"} />
        <span className="font-medium">{h.name}</span>
        {h.cordoned && (
          <Badge variant="warning" title={h.cordonedReason}>
            cordoned
          </Badge>
        )}
        <span className="ml-auto text-xs text-muted-foreground tabular-nums">
          {h.workers.total} / {h.maxWorkers} workers
        </span>
      </div>
      <div className="grid grid-cols-[3.5rem_1fr_4.5rem] items-center gap-x-2 gap-y-1 text-xs">
        <span className="text-muted-foreground">CPU</span>
        <Meter ratio={m?.cpuUsageRatio ?? null} />
        <span className="text-right tabular-nums">{percent(m?.cpuUsageRatio)}</span>
        <span className="text-muted-foreground">Memory</span>
        <Meter ratio={m && used !== null ? used / m.memoryTotalBytes : null} />
        <span className="text-right tabular-nums" title={`${bytes(used)} of ${bytes(m?.memoryTotalBytes)}`}>
          {bytes(used, 0)}
        </span>
        <span className="text-muted-foreground">Disk</span>
        <Meter ratio={disk?.ratio ?? null} />
        <span className="text-right tabular-nums" title={disk?.path}>
          {percent(disk?.ratio)}
        </span>
      </div>
    </Link>
  );
}
