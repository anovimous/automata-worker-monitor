import * as React from "react";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, Plus } from "lucide-react";
import { api } from "@/api";
import { byPrefix, pick, sourceLabel } from "@/charts";
import { Chart } from "@/components/Chart";
import { CordonButton } from "@/components/Cordon";
import { NewWorkerDialog } from "@/components/NewWorkerDialog";
import { RangePicker } from "@/components/RangePicker";
import { ErrorBox, PageTitle, Section } from "@/components/Shell";
import { WorkersTable } from "@/components/WorkersTable";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dot } from "@/components/badges";
import { useUIConfig } from "@/config";
import { ago, bytes, percent, rate } from "@/format";
import { useHosts, useWorkers } from "@/live";
import type { Range, Timeseries } from "@/types";

export function HostPage() {
  const { name } = useParams({ from: "/hosts/$name" });
  const search = useSearch({ from: "/hosts/$name" });
  const navigate = useNavigate({ from: "/hosts/$name" });
  const range: Range = search.range ?? "1h";
  const { data: cfg } = useUIConfig();
  const { data: hosts = [] } = useHosts();
  const { data: workers = [] } = useWorkers();
  const [creating, setCreating] = React.useState(false);
  const h = hosts.find((x) => x.name === name);
  const live = range === "15m" || range === "1h";
  const { data, error } = useQuery({
    queryKey: ["host-ts", name, range],
    queryFn: () => api.get<Timeseries>(`/hosts/${encodeURIComponent(name)}/timeseries?series=cpu,memory,load,disk,net&range=${range}`),
    refetchInterval: live ? 5_000 : 60_000,
  });
  const cpu = React.useMemo(() => pick(data, ["cpu", "cpuMax"]).map((s) => ({ series: s })), [data]);
  const memory = React.useMemo(
    () => pick(data, ["memoryUsed", "memoryTotal"]).map((s) => ({ series: s, dash: s.name === "memoryTotal", label: s.name === "memoryTotal" ? "total" : "used" })),
    [data],
  );
  const load = React.useMemo(() => pick(data, ["load1"]).map((s) => ({ series: s })), [data]);
  const disk = React.useMemo(() => byPrefix(data, "disk:").map((s) => ({ series: s, label: s.name.slice(5) })), [data]);
  const net = React.useMemo(() => pick(data, ["netRx", "netTx"]).map((s) => ({ series: s })), [data]);
  return (
    <div className="space-y-5">
      <Link to="/hosts" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
        <ArrowLeft className="h-3 w-3" /> Hosts
      </Link>
      <PageTitle
        right={
          h && (
            <>
              <CordonButton host={h} />
              <Button size="sm" onClick={() => setCreating(true)} disabled={!h.reachable || h.cordoned}>
                <Plus className="h-4 w-4" /> New worker
              </Button>
            </>
          )
        }
      >
        <span className="flex items-center gap-2">
          {h && <Dot ok={h.reachable} />} {name}
          {h?.cordoned && <Badge variant="warning">cordoned{h.cordonedReason ? `: ${h.cordonedReason}` : ""}</Badge>}
        </span>
      </PageTitle>
      {h && (
        <p className="text-sm text-muted-foreground">
          {h.reachable ? `${h.metrics?.cpus ?? "—"} CPUs, ${bytes(h.metrics?.memoryTotalBytes, 0)}, Docker ${h.metrics?.dockerVersion ?? "—"}, kernel ${h.metrics?.kernel ?? "—"}` : `Not answering; last seen ${ago(h.lastSeenAt)}.`}
        </p>
      )}
      <Section
        title="Charts"
        right={
          <>
            <span className="text-xs text-muted-foreground">{sourceLabel(data)}</span>
            <RangePicker value={range} onChange={(r) => navigate({ search: { range: r }, replace: true })} retentionDays={cfg?.historyRetentionDays} />
          </>
        }
      >
        <ErrorBox error={error} />
        <div className="grid gap-3 xl:grid-cols-2">
          <Chart title="CPU" lines={cpu} format={(v) => percent(v)} />
          <Chart title="Memory" lines={memory} format={(v) => bytes(v, 0)} />
          <Chart title="Load (1 min)" lines={load} format={(v) => v.toFixed(2)} />
          <Chart title="Disk used" lines={disk} format={(v) => percent(v)} />
          <Chart title="Network" lines={net} format={rate} />
        </div>
      </Section>
      <Section title="Workers">
        <WorkersTable workers={workers.filter((w) => w.host === name)} showHost={false} />
      </Section>
      <NewWorkerDialog open={creating} onOpenChange={setCreating} host={name} />
    </div>
  );
}
