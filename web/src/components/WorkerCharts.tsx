import * as React from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api";
import { byPrefix, pick, sourceLabel } from "@/charts";
import { Chart, HealthStrip, type Line } from "@/components/Chart";
import { RangePicker } from "@/components/RangePicker";
import { ErrorBox } from "@/components/Shell";
import { useUIConfig } from "@/config";
import { bytes, cores, percent, rate } from "@/format";
import type { Availability, Range, Timeseries } from "@/types";

const limitColor = "hsl(0 72% 55%)";
const countFormat = (v: number) => String(Math.round(v));

function lines(ts: Timeseries | undefined, names: string[], limit?: string): Line[] {
  return [
    ...pick(ts, names).map((s) => ({ series: s })),
    ...(limit ? pick(ts, [limit]).map((s) => ({ series: s, color: limitColor, dash: true, label: "limit" })) : []),
  ];
}

function AvailabilityLine({ id }: { id: string }) {
  const q = (range: string) => ({
    queryKey: ["availability", id, range],
    queryFn: () => api.get<Availability>(`/workers/${encodeURIComponent(id)}/availability?range=${range}`),
    refetchInterval: 60_000,
  });
  const day = useQuery(q("24h"));
  const week = useQuery(q("7d"));
  const text = (a: Availability | undefined) =>
    a && a.polls > 0 ? `responsive ${percent(a.availability, 1)}, busy ${percent(a.utilisation)}` : "no history yet";
  return (
    <div className="flex flex-wrap gap-x-6 gap-y-1 text-sm">
      <span>
        <span className="text-muted-foreground">Last 24 h: </span>
        {text(day.data)}
      </span>
      <span>
        <span className="text-muted-foreground">Last 7 days: </span>
        {text(week.data)}
      </span>
    </div>
  );
}

// WorkerCharts is the Charts tab: the health strip over CPU, memory, pids, block I/O and jobs, for every range.
export function WorkerCharts({ id, range, setRange }: { id: string; range: Range; setRange: (r: Range) => void }) {
  const { data: cfg } = useUIConfig();
  const live = range === "15m" || range === "1h";
  const { data, error, isFetching } = useQuery({
    queryKey: ["worker-ts", id, range],
    queryFn: () => api.get<Timeseries>(`/workers/${encodeURIComponent(id)}/timeseries?series=cpu,memory,pids,io,health,jobs&range=${range}`),
    refetchInterval: live ? 5_000 : 60_000,
  });
  const cpu = React.useMemo(() => lines(data, ["cpu", "cpuMax"], "cpuLimit"), [data]);
  const memory = React.useMemo(() => lines(data, ["memory", "memoryMax"], "memoryLimit"), [data]);
  const pids = React.useMemo(() => lines(data, ["pids"]), [data]);
  const io = React.useMemo(() => lines(data, ["blockRead", "blockWrite"]), [data]);
  const jobs = React.useMemo(() => byPrefix(data, "jobs:").map((s) => ({ series: s, label: s.name.slice(5) })), [data]);
  const health = data?.series.find((s) => s.name === "health");
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <RangePicker value={range} onChange={setRange} retentionDays={cfg?.historyRetentionDays} />
        <span className="text-xs text-muted-foreground">
          {sourceLabel(data)}
          {isFetching && " · loading…"}
        </span>
      </div>
      <AvailabilityLine id={id} />
      <ErrorBox error={error} />
      <HealthStrip series={health} />
      <div className="grid gap-3 xl:grid-cols-2">
        <Chart title="CPU (cores)" lines={cpu} format={cores} />
        <Chart title="Memory (working set)" lines={memory} format={(v) => bytes(v, 0)} />
        <Chart title="Pids" lines={pids} format={countFormat} />
        <Chart title="Block I/O" lines={io} format={rate} />
        <Chart title="Jobs ended, by outcome" lines={jobs} format={countFormat} bars />
      </div>
      <p className="text-xs text-muted-foreground">Network per worker and the CPU of the job and of its tool are not collected.</p>
    </div>
  );
}
