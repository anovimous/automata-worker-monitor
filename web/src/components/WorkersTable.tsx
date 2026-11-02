import * as React from "react";
import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api";
import { activityText } from "@/activity";
import { ActionsMenu } from "@/components/ConfirmAction";
import { Meter, WorkerStatus } from "@/components/badges";
import { Sparkline } from "@/components/Chart";
import { ago, bytes, cores, duration, percent, secondsSince } from "@/format";
import { healthRank } from "@/health";
import { cn } from "@/lib/cn";
import type { Availability, Timeseries, WorkerView } from "@/types";

// sortWorkers lists the problems first (the worst first), then by id.
export function sortWorkers(list: WorkerView[]): WorkerView[] {
  return list.slice().sort((a, b) => healthRank(a.health) - healthRank(b.health) || a.id.localeCompare(b.id));
}

// useVisible tells once an element has been on screen, so that a row loads its sparkline only then.
function useVisible<T extends Element>(): [React.RefObject<T>, boolean] {
  const ref = React.useRef<T>(null);
  const [visible, setVisible] = React.useState(false);
  React.useEffect(() => {
    const el = ref.current;
    if (!el || visible || typeof IntersectionObserver === "undefined") {
      return;
    }
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) {
        setVisible(true);
        io.disconnect();
      }
    });
    io.observe(el);
    return () => io.disconnect();
  }, [visible]);
  return [ref, visible];
}

function CpuCell({ w }: { w: WorkerView }) {
  const [ref, visible] = useVisible<HTMLDivElement>();
  const running = w.resources !== null;
  const { data } = useQuery({
    queryKey: ["spark", w.id],
    queryFn: () => api.get<Timeseries>(`/workers/${encodeURIComponent(w.id)}/timeseries?series=cpu&range=15m`),
    enabled: visible && running,
    refetchInterval: 30_000,
    staleTime: 25_000,
  });
  const cpu = data?.series.find((s) => s.name === "cpu");
  const values = (cpu?.points ?? []).slice(-60).map(([, v]) => (typeof v === "number" ? v : null));
  return (
    <div ref={ref} className="flex items-center gap-2">
      <span className="w-12 text-right tabular-nums">{cores(w.resources?.cpuCores)}</span>
      {running && <Sparkline values={values} max={w.resources?.cpuLimit ?? undefined} className="text-muted-foreground" />}
    </div>
  );
}

const th = "px-3 py-2 text-left text-[11px] font-medium uppercase tracking-wide text-muted-foreground whitespace-nowrap";
const td = "px-3 py-1.5 whitespace-nowrap";

export function WorkersTable({
  workers,
  availability,
  showHost = true,
}: {
  workers: WorkerView[];
  availability?: Record<string, Availability>;
  showHost?: boolean;
}) {
  const now = Date.now();
  if (workers.length === 0) {
    return <div className="rounded-lg border border-dashed border-border p-8 text-center text-sm text-muted-foreground">No workers</div>;
  }
  return (
    <div className="overflow-x-auto rounded-lg border border-border">
      <table className="w-full text-sm">
        <thead className="border-b border-border bg-muted/40">
          <tr>
            <th className={th}>Worker</th>
            {showHost && <th className={th}>Host</th>}
            <th className={th}>Health</th>
            <th className={th}>Activity</th>
            <th className={th}>CPU</th>
            <th className={th}>Memory</th>
            <th className={th}>Pids</th>
            <th className={th}>Restarts</th>
            <th className={th}>Uptime</th>
            <th className={th}>Last seen</th>
            <th className={th}>Version</th>
            {availability && <th className={th}>Avail 24h</th>}
            <th className={th} />
          </tr>
        </thead>
        <tbody>
          {sortWorkers(workers).map((w) => {
            const mem = w.resources;
            const memRatio = mem && mem.memoryLimitBytes ? mem.memoryWorkingSetBytes / mem.memoryLimitBytes : null;
            const seen = secondsSince(w.lastSeenAt, now);
            const avail = availability?.[w.id];
            return (
              <tr key={w.id} className="border-b border-border last:border-0 hover:bg-muted/30">
                <td className={td}>
                  <Link to="/workers/$id" params={{ id: w.id }} className="font-mono text-xs hover:underline">
                    {w.id}
                  </Link>
                </td>
                {showHost && (
                  <td className={td}>
                    <Link to="/hosts/$name" params={{ name: w.host }} className="hover:underline">
                      {w.host}
                    </Link>
                  </td>
                )}
                <td className={td}>
                  <WorkerStatus w={w} />
                </td>
                <td className={cn(td, "max-w-[22rem] truncate")} title={activityText(w.activity, now)}>
                  {activityText(w.activity, now)}
                  {w.activity?.stale && <span className="ml-1 text-xs text-muted-foreground">(last known)</span>}
                </td>
                <td className={td}>
                  <CpuCell w={w} />
                </td>
                <td className={td}>
                  {mem ? (
                    <div className="w-32 space-y-0.5" title={`${bytes(mem.memoryWorkingSetBytes)} of ${bytes(mem.memoryLimitBytes)}`}>
                      <div className="text-xs tabular-nums">
                        {bytes(mem.memoryWorkingSetBytes)} <span className="text-muted-foreground">/ {bytes(mem.memoryLimitBytes, 0)}</span>
                      </div>
                      <Meter ratio={memRatio} />
                    </div>
                  ) : (
                    "—"
                  )}
                </td>
                <td className={cn(td, "tabular-nums")}>{mem ? mem.pids : "—"}</td>
                <td className={cn(td, "tabular-nums", w.healthReasons.includes("RESTARTED_RECENTLY") && "text-destructive font-medium")}>
                  {w.restartCount}
                  {w.oomKills > 0 && <span className="ml-1 text-xs text-destructive">OOM {w.oomKills}</span>}
                </td>
                <td className={cn(td, "tabular-nums")}>{duration(w.uptimeSeconds)}</td>
                <td className={cn(td, "tabular-nums", w.lifecycle === "ACTIVE" && (seen === null || seen > 15) && "text-destructive")}>
                  {ago(w.lastSeenAt, now)}
                </td>
                <td className={cn(td, "font-mono text-xs")}>{w.version || "—"}</td>
                {availability && (
                  <td className={cn(td, "tabular-nums")} title={avail ? `busy ${percent(avail.utilisation)}` : undefined}>
                    {avail ? percent(avail.availability, 1) : "—"}
                  </td>
                )}
                <td className={cn(td, "text-right")}>
                  <ActionsMenu worker={w} />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
