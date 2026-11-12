import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
import { api, ApiError } from "@/api";
import { ActionButtons } from "@/components/ConfirmAction";
import { WorkerStatus } from "@/components/badges";
import { ErrorBox } from "@/components/Shell";
import { WorkerCharts } from "@/components/WorkerCharts";
import { WorkerContainer } from "@/components/WorkerContainer";
import { WorkerEvents } from "@/components/WorkerEvents";
import { WorkerLogs } from "@/components/WorkerLogs";
import { WorkerOverview } from "@/components/WorkerOverview";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/primitives";
import { duration } from "@/format";
import { useWorker } from "@/live";
import type { Range, WorkerDetailResponse } from "@/types";

export const workerTabs = ["overview", "charts", "events", "logs", "container"] as const;
export type WorkerTab = (typeof workerTabs)[number];

export function WorkerPage() {
  const { id } = useParams({ from: "/workers/$id" });
  const search = useSearch({ from: "/workers/$id" });
  const navigate = useNavigate({ from: "/workers/$id" });
  const tab: WorkerTab = search.tab ?? "overview";
  const range: Range = search.range ?? "1h";
  const live = useWorker(id);
  const detail = useQuery({
    queryKey: ["worker-detail", id],
    queryFn: () => api.get<WorkerDetailResponse>(`/workers/${encodeURIComponent(id)}`),
    refetchInterval: tab === "container" ? 15_000 : false,
    retry: (n, e) => !(e instanceof ApiError && e.status === 404) && n < 2,
  });
  const w = live ?? detail.data?.worker;
  const gone = !w && detail.error instanceof ApiError && detail.error.status === 404;

  return (
    <div className="space-y-4">
      <Link to="/" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
        <ArrowLeft className="h-3 w-3" /> Fleet
      </Link>
      <div className="flex flex-wrap items-start gap-4">
        <div className="space-y-1">
          <h1 className="font-mono text-lg font-semibold">{id}</h1>
          {w ? (
            <div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
              <WorkerStatus w={w} />
              <span>
                on{" "}
                <Link to="/hosts/$name" params={{ name: w.host }} className="text-foreground hover:underline">
                  {w.host}
                </Link>
              </span>
              <span className="font-mono text-xs">{w.image}</span>
              {w.version && <span>{/^\d/.test(w.version) ? "v" + w.version : w.version}</span>}
              <span>up {duration(w.uptimeSeconds)}</span>
            </div>
          ) : gone ? (
            <p className="text-sm text-muted-foreground">This worker is removed; its history and events stay until the retention purge.</p>
          ) : null}
          {w?.healthMessage && <p className="text-sm">{w.healthMessage}</p>}
        </div>
        {w && (
          <div className="ml-auto">
            <ActionButtons worker={w} template={detail.data?.templateSnapshot} />
          </div>
        )}
      </div>
      {!gone && <ErrorBox error={detail.error} />}
      <Tabs value={tab} onValueChange={(t) => navigate({ search: { ...search, tab: t as WorkerTab }, replace: true })}>
        <TabsList>
          <TabsTrigger value="overview" disabled={!w}>
            Overview
          </TabsTrigger>
          <TabsTrigger value="charts">Charts</TabsTrigger>
          <TabsTrigger value="events">Events</TabsTrigger>
          <TabsTrigger value="logs" disabled={!w}>
            Logs
          </TabsTrigger>
          <TabsTrigger value="container" disabled={!detail.data}>
            Container
          </TabsTrigger>
        </TabsList>
        <div className="pt-4">
          <TabsContent value="overview">{w && <WorkerOverview w={w} />}</TabsContent>
          <TabsContent value="charts">
            <WorkerCharts id={id} range={range} setRange={(r) => navigate({ search: { ...search, range: r }, replace: true })} />
          </TabsContent>
          <TabsContent value="events">
            <WorkerEvents id={id} />
          </TabsContent>
          <TabsContent value="logs">{tab === "logs" && w && <WorkerLogs id={id} />}</TabsContent>
          <TabsContent value="container">{detail.data && <WorkerContainer detail={detail.data} />}</TabsContent>
        </div>
      </Tabs>
    </div>
  );
}
