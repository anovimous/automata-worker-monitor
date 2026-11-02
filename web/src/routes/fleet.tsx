import * as React from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Plus } from "lucide-react";
import { api } from "@/api";
import { byPrefix, pick, sourceLabel } from "@/charts";
import { Chart } from "@/components/Chart";
import { HostCard } from "@/components/HostCard";
import { NewWorkerDialog } from "@/components/NewWorkerDialog";
import { RangePicker } from "@/components/RangePicker";
import { ErrorBox, PageTitle, Section } from "@/components/Shell";
import { WorkersTable } from "@/components/WorkersTable";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/primitives";
import { useUIConfig } from "@/config";
import { matches, type WorkerFilter } from "@/filter";
import { healthOrder, healthStyles } from "@/health";
import { useHosts, useWorkers } from "@/live";
import { cn } from "@/lib/cn";
import type { Availability, Health, Range, Timeseries, WorkerView } from "@/types";

const workersFormat = (v: number) => (Number.isInteger(v) ? String(v) : v.toFixed(1));
const countFormat = (v: number) => String(Math.round(v));

function FleetChart() {
  const [open, setOpen] = React.useState(true);
  const [range, setRange] = React.useState<Range>("24h");
  const { data: cfg } = useUIConfig();
  const { data, error } = useQuery({
    queryKey: ["fleet-ts", range],
    queryFn: () => api.get<Timeseries>(`/fleet/timeseries?series=capacity,jobs&range=${range}`),
    enabled: open,
    refetchInterval: 60_000,
  });
  const capacity = React.useMemo(() => pick(data, ["active", "busy", "responsive"]).map((s) => ({ series: s })), [data]);
  const jobs = React.useMemo(() => byPrefix(data, "jobs:").map((s) => ({ series: s, label: s.name.slice(5) })), [data]);
  return (
    <section className="space-y-2">
      <div className="flex items-center gap-2">
        <button type="button" onClick={() => setOpen(!open)} className="flex items-center gap-1 text-sm font-medium">
          {open ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />} Fleet over time
        </button>
        {open && <span className="text-xs text-muted-foreground">{sourceLabel(data)}</span>}
        {open && (
          <div className="ml-auto">
            <RangePicker value={range} onChange={setRange} retentionDays={cfg?.historyRetentionDays} />
          </div>
        )}
      </div>
      {open && (
        <>
          <ErrorBox error={error} />
          <div className="grid gap-3 lg:grid-cols-2">
            <Chart title="Workers active, busy and responsive" lines={capacity} format={workersFormat} />
            <Chart title="Jobs ended, by outcome" lines={jobs} format={countFormat} bars />
          </div>
        </>
      )}
    </section>
  );
}

function SummaryBar({ workers, filter, setFilter }: { workers: WorkerView[]; filter: WorkerFilter; setFilter: (f: WorkerFilter) => void }) {
  const byHealth: Record<string, number> = {};
  const routines: Record<string, number> = {};
  let busy = 0;
  let idle = 0;
  for (const w of workers) {
    byHealth[w.health] = (byHealth[w.health] ?? 0) + 1;
    if (w.activity?.job && !w.activity.stale) {
      busy++;
      routines[w.activity.job.routineKey] = (routines[w.activity.job.routineKey] ?? 0) + 1;
    } else if (w.responsive) {
      idle++;
    }
  }
  return (
    <div className="flex flex-wrap items-stretch gap-2">
      {healthOrder
        .filter((h) => byHealth[h])
        .map((h) => {
          const s = healthStyles[h as Health];
          const active = filter.health === h;
          return (
            <button
              key={h}
              type="button"
              onClick={() => setFilter({ ...filter, health: active ? undefined : h })}
              className={cn(
                "min-w-[96px] rounded-lg border px-3 py-2 text-left",
                active ? "border-foreground" : "border-border hover:border-foreground/30",
              )}
            >
              <div className="text-xl font-semibold tabular-nums" style={{ color: h === "NONE" ? undefined : s.color }}>
                {byHealth[h]}
              </div>
              <div className="text-xs text-muted-foreground">{h === "NONE" ? "Not running" : s.label}</div>
            </button>
          );
        })}
      <div className="min-w-[120px] rounded-lg border border-border px-3 py-2">
        <div className="text-xl font-semibold tabular-nums">
          {busy} <span className="text-sm font-normal text-muted-foreground">/ {idle} idle</span>
        </div>
        <div className="text-xs text-muted-foreground">Busy</div>
      </div>
      <div className="min-w-[160px] flex-1 rounded-lg border border-border px-3 py-2">
        <div className="text-sm font-medium">
          {Object.keys(routines).length === 0
            ? "—"
            : Object.entries(routines)
                .sort()
                .map(([r, n]) => `${r} ${n}`)
                .join(" · ")}
        </div>
        <div className="text-xs text-muted-foreground">Routines running now</div>
      </div>
    </div>
  );
}

const selectClass = "h-8 rounded-md border border-input bg-transparent px-2 text-sm";

function Filters({ workers, filter, setFilter }: { workers: WorkerView[]; filter: WorkerFilter; setFilter: (f: WorkerFilter) => void }) {
  const values = (fn: (w: WorkerView) => string | undefined) => [...new Set(workers.map(fn).filter(Boolean) as string[])].sort();
  const select = (key: keyof WorkerFilter, label: string, options: string[]) => (
    <select
      aria-label={label}
      className={selectClass}
      value={filter[key] ?? ""}
      onChange={(e) => setFilter({ ...filter, [key]: e.target.value || undefined })}
    >
      <option value="">{label}: all</option>
      {options.map((o) => (
        <option key={o} value={o}>
          {o}
        </option>
      ))}
    </select>
  );
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Input
        placeholder="Search id"
        className="h-8 w-48"
        value={filter.q ?? ""}
        onChange={(e) => setFilter({ ...filter, q: e.target.value || undefined })}
      />
      {select("host", "Host", values((w) => w.host))}
      {select("health", "Health", healthOrder.filter((h) => workers.some((w) => w.health === h)))}
      {select("lifecycle", "Lifecycle", values((w) => w.lifecycle))}
      {select("phase", "Phase", values((w) => w.activity?.phase))}
      {select("routine", "Routine", values((w) => w.activity?.job?.routineKey))}
      {Object.values(filter).some(Boolean) && (
        <Button variant="ghost" size="sm" onClick={() => setFilter({})}>
          Clear
        </Button>
      )}
    </div>
  );
}

export function FleetPage() {
  const search = useSearch({ from: "/" });
  const navigate = useNavigate({ from: "/" });
  const filter: WorkerFilter = search;
  const setFilter = (f: WorkerFilter) => navigate({ search: { ...f, avail: search.avail }, replace: true });
  const { data: workers = [], error, isLoading } = useWorkers();
  const { data: hosts = [] } = useHosts();
  const [creating, setCreating] = React.useState(false);
  const showAvail = search.avail !== "off";
  const { data: availability } = useQuery({
    queryKey: ["availability", "24h"],
    queryFn: () => api.get<{ range: string; workers: Record<string, Availability> }>("/availability?range=24h"),
    enabled: showAvail,
    refetchInterval: 60_000,
  });
  const shown = workers.filter((w) => matches(w, filter));
  return (
    <div className="space-y-6">
      <PageTitle
        right={
          <Button onClick={() => setCreating(true)}>
            <Plus className="h-4 w-4" /> New worker
          </Button>
        }
      >
        Fleet
      </PageTitle>
      <ErrorBox error={error} />
      <SummaryBar workers={workers} filter={filter} setFilter={setFilter} />
      <FleetChart />
      <Section title="Hosts">
        <div className="flex flex-wrap gap-2">
          {hosts.map((h) => (
            <HostCard key={h.name} h={h} />
          ))}
        </div>
      </Section>
      <Section
        title={`Workers (${shown.length}${shown.length !== workers.length ? ` of ${workers.length}` : ""})`}
        right={
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <Switch
              checked={showAvail}
              onCheckedChange={(v) => navigate({ search: { ...filter, avail: v ? undefined : "off" }, replace: true })}
            />
            Avail 24h
          </label>
        }
      >
        <Filters workers={workers} filter={filter} setFilter={setFilter} />
        {isLoading ? (
          <div className="p-8 text-center text-sm text-muted-foreground">Loading…</div>
        ) : (
          <WorkersTable workers={shown} availability={showAvail ? (availability?.workers ?? {}) : undefined} />
        )}
      </Section>
      <NewWorkerDialog open={creating} onOpenChange={setCreating} />
    </div>
  );
}
