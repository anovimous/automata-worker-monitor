import { useInfiniteQuery } from "@tanstack/react-query";
import { api } from "@/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ErrorBox } from "@/components/Shell";
import { dateTime } from "@/format";
import { reasonText } from "@/health";
import { keys } from "@/live";
import type { WorkerEvent } from "@/types";

const pageSize = 50;

const kindVariant: Record<string, "default" | "destructive" | "warning" | "success" | "muted"> = {
  HEALTH_CHANGED: "warning",
  OOM_KILLED: "destructive",
  CONTAINER_RESTARTED: "destructive",
  PROVISION_FAILED: "destructive",
  DISAPPEARED: "destructive",
  DRAIN_TIMEOUT: "warning",
  JOB_STARTED: "muted",
  JOB_ENDED: "muted",
  ACTION: "default",
  ADOPTED: "default",
};

// eventText says what an event means in one line.
export function eventText(e: WorkerEvent): string {
  switch (e.kind) {
    case "HEALTH_CHANGED":
    case "LIFECYCLE_CHANGED":
      return `${e.from || "—"} → ${e.to}`;
    case "ACTION":
      return e.to ? `${e.to}` : e.message;
    case "JOB_STARTED":
      return `job #${e.to}${e.message ? ` (${e.message})` : ""} started`;
    case "JOB_ENDED":
      return `job #${e.from} ended: ${e.to}`;
    case "CONTAINER_RESTARTED":
      return `restart count ${e.from} → ${e.to}`;
    default:
      return e.to;
  }
}

// WorkerEvents is the Events tab: the timeline, newest first, a page at a time; new events come from the live stream.
export function WorkerEvents({ id }: { id: string }) {
  const q = useInfiniteQuery({
    queryKey: keys.events(id),
    queryFn: ({ pageParam }) =>
      api.get<WorkerEvent[]>(`/workers/${encodeURIComponent(id)}/events?limit=${pageSize}${pageParam ? `&before=${pageParam}` : ""}`),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.length < pageSize ? undefined : last[last.length - 1].id),
    staleTime: Infinity,
  });
  const events = q.data?.pages.flat() ?? [];
  return (
    <div className="space-y-3">
      <ErrorBox error={q.error} />
      {events.length === 0 && !q.isLoading && <p className="text-sm text-muted-foreground">No events.</p>}
      <ol className="space-y-0 border-l border-border">
        {events.map((e) => (
          <li key={e.id} className="relative space-y-1 py-2 pl-4">
            <span className="absolute -left-[4px] top-3.5 h-2 w-2 rounded-full bg-border" />
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <span className="w-44 shrink-0 text-xs tabular-nums text-muted-foreground">{dateTime(e.at)}</span>
              <Badge variant={kindVariant[e.kind] ?? "outline"}>{e.kind.replace(/_/g, " ")}</Badge>
              <span>{eventText(e)}</span>
            </div>
            {(e.message && e.kind !== "JOB_STARTED") || e.reasons.length > 0 ? (
              <div className="pl-[11.5rem] text-xs text-muted-foreground">
                {e.kind !== "JOB_STARTED" && e.message && <p>{e.message}</p>}
                {e.reasons.map((r) => (
                  <p key={r}>
                    <span className="font-mono">{r}</span>: {reasonText(r)}
                  </p>
                ))}
              </div>
            ) : null}
          </li>
        ))}
      </ol>
      {q.hasNextPage && (
        <Button variant="outline" size="sm" onClick={() => q.fetchNextPage()} disabled={q.isFetchingNextPage}>
          {q.isFetchingNextPage ? "Loading…" : "Older events"}
        </Button>
      )}
    </div>
  );
}
