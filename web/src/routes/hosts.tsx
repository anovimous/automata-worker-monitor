import { Link } from "@tanstack/react-router";
import { Badge } from "@/components/ui/badge";
import { Dot } from "@/components/badges";
import { CordonButton } from "@/components/Cordon";
import { memoryUsed } from "@/components/HostCard";
import { ErrorBox, PageTitle } from "@/components/Shell";
import { useUIConfig } from "@/config";
import { ago, bytes, percent } from "@/format";
import { useHosts } from "@/live";
import { cn } from "@/lib/cn";

const th = "px-3 py-2 text-left text-[11px] font-medium uppercase tracking-wide text-muted-foreground whitespace-nowrap";
const td = "px-3 py-2 align-top whitespace-nowrap";

export function HostsPage() {
  const { data: hosts = [], error } = useHosts();
  const { data: cfg } = useUIConfig();
  return (
    <div className="space-y-4">
      <PageTitle>Hosts</PageTitle>
      <ErrorBox error={error} />
      <div className="overflow-x-auto rounded-lg border border-border">
        <table className="w-full text-sm">
          <thead className="border-b border-border bg-muted/40">
            <tr>
              <th className={th}>Host</th>
              <th className={th}>Docker</th>
              <th className={th}>CPU</th>
              <th className={th}>Load</th>
              <th className={th}>Memory</th>
              <th className={th}>Disks</th>
              <th className={th}>Worker volumes</th>
              <th className={th}>Workers</th>
              <th className={th}>Capacity (limits)</th>
              <th className={th} />
            </tr>
          </thead>
          <tbody>
            {hosts.map((h) => {
              const m = h.metrics;
              const used = memoryUsed(h);
              return (
                <tr key={h.name} className="border-b border-border last:border-0">
                  <td className={td}>
                    <div className="flex items-center gap-2">
                      <Dot ok={h.reachable} />
                      <Link to="/hosts/$name" params={{ name: h.name }} className="font-medium hover:underline">
                        {h.name}
                      </Link>
                      {h.cordoned && (
                        <Badge variant="warning" title={h.cordonedReason}>
                          cordoned
                        </Badge>
                      )}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      {h.reachable ? `controller ${h.workerMachineControllerVersion}` : `not answering, last seen ${ago(h.lastSeenAt)}`}
                    </div>
                    {h.cordoned && h.cordonedReason && <div className="text-xs text-warning">{h.cordonedReason}</div>}
                  </td>
                  <td className={td}>{m?.dockerVersion || "—"}</td>
                  <td className={cn(td, "tabular-nums")}>
                    {percent(m?.cpuUsageRatio)} <span className="text-muted-foreground">of {m?.cpus ?? "—"}</span>
                  </td>
                  <td className={cn(td, "tabular-nums")}>{m ? `${m.load1.toFixed(2)} ${m.load5.toFixed(2)} ${m.load15.toFixed(2)}` : "—"}</td>
                  <td className={cn(td, "tabular-nums")}>
                    {bytes(used)} <span className="text-muted-foreground">/ {bytes(m?.memoryTotalBytes, 0)}</span>
                  </td>
                  <td className={cn(td, "tabular-nums")}>
                    {m?.disks.map((d) => (
                      <div key={d.path}>
                        <span className="font-mono text-xs">{d.path}</span> {percent(d.totalBytes ? (d.totalBytes - d.freeBytes) / d.totalBytes : null)}{" "}
                        <span className="text-muted-foreground">({bytes(d.freeBytes, 0)} free)</span>
                      </div>
                    )) ?? "—"}
                  </td>
                  <td className={cn(td, "tabular-nums")}>{bytes(m?.volumesBytes)}</td>
                  <td className={cn(td, "tabular-nums")}>
                    {h.workers.total} / {h.maxWorkers}
                    <div className="text-xs text-muted-foreground">
                      {h.workers.running} running · {h.workers.responsive} responsive · {h.workers.busy} busy
                    </div>
                  </td>
                  <td className={cn(td, "tabular-nums")}>
                    {h.capacity.cpusReserved} / {h.capacity.cpus} CPUs
                    <div className="text-xs text-muted-foreground">
                      {bytes(h.capacity.memoryBytesReserved, 0)} / {bytes(h.capacity.memoryBytes, 0)}
                    </div>
                  </td>
                  <td className={cn(td, "text-right")}>
                    <CordonButton host={h} />
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className="text-xs text-muted-foreground">
        Storage: the monitor's database is {bytes(cfg?.databaseBytes)}; history is kept {cfg?.historyRetentionDays ?? "—"} days.
      </p>
    </div>
  );
}
