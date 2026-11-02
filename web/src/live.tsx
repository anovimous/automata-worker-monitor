// The live picture (plan/08 §1): one EventSource on /api/v1/events. The snapshot fills the caches of ['workers'] and
// ['hosts']; worker.updated, worker.removed and host.updated patch them; worker.event adds to ['events', id] when that
// timeline is loaded. Until the first snapshot, and when the stream is down, the queries fetch by themselves.
import * as React from "react";
import { useQuery, useQueryClient, type InfiniteData, type QueryClient } from "@tanstack/react-query";
import { api } from "@/api";
import type { HostView, WorkerEvent, WorkerView } from "@/types";

export const keys = {
  workers: ["workers"] as const,
  hosts: ["hosts"] as const,
  events: (id: string) => ["events", id] as const,
};

export function upsertWorker(list: WorkerView[] | undefined, w: WorkerView): WorkerView[] {
  if (!list) {
    return [w];
  }
  const i = list.findIndex((x) => x.id === w.id);
  if (i < 0) {
    return [...list, w].sort((a, b) => a.id.localeCompare(b.id));
  }
  const next = list.slice();
  next[i] = w;
  return next;
}

export function removeWorker(list: WorkerView[] | undefined, id: string): WorkerView[] | undefined {
  return list?.filter((w) => w.id !== id);
}

export function upsertHost(list: HostView[] | undefined, h: HostView): HostView[] {
  if (!list) {
    return [h];
  }
  const i = list.findIndex((x) => x.name === h.name);
  if (i < 0) {
    return [...list, h];
  }
  const next = list.slice();
  next[i] = h;
  return next;
}

// prependEvent adds an event to the first page of a loaded timeline (newest first), once.
export function prependEvent(
  data: InfiniteData<WorkerEvent[]> | undefined,
  e: WorkerEvent,
): InfiniteData<WorkerEvent[]> | undefined {
  if (!data || data.pages.length === 0) {
    return data;
  }
  if (data.pages.some((p) => p.some((x) => x.id === e.id))) {
    return data;
  }
  return { ...data, pages: [[e, ...data.pages[0]], ...data.pages.slice(1)] };
}

// applyEvent applies one server-sent event to the caches.
export function applyEvent(qc: QueryClient, type: string, data: unknown): void {
  switch (type) {
    case "snapshot": {
      const s = data as { workers: WorkerView[]; hosts: HostView[] };
      qc.setQueryData(keys.workers, s.workers);
      qc.setQueryData(keys.hosts, s.hosts);
      break;
    }
    case "worker.updated": {
      const w = data as WorkerView;
      qc.setQueryData<WorkerView[]>(keys.workers, (old) => upsertWorker(old, w));
      break;
    }
    case "worker.removed": {
      const { id } = data as { id: string };
      qc.setQueryData<WorkerView[]>(keys.workers, (old) => removeWorker(old, id));
      break;
    }
    case "host.updated": {
      const h = data as HostView;
      qc.setQueryData<HostView[]>(keys.hosts, (old) => upsertHost(old, h));
      break;
    }
    case "worker.event": {
      const e = data as WorkerEvent;
      qc.setQueryData<InfiniteData<WorkerEvent[]>>(keys.events(e.workerId), (old) => prependEvent(old, e));
      break;
    }
  }
}

export type LiveState = "connecting" | "live" | "reconnecting";

const LiveContext = React.createContext<LiveState>("connecting");

export const eventTypes = ["snapshot", "worker.updated", "worker.removed", "host.updated", "worker.event"];

export function LiveProvider({ children }: { children: React.ReactNode }) {
  const qc = useQueryClient();
  const [state, setState] = React.useState<LiveState>("connecting");
  React.useEffect(() => {
    const es = new EventSource("/api/v1/events");
    es.onopen = () => setState("live");
    // EventSource reconnects by itself; the next snapshot brings the picture back
    es.onerror = () => setState("reconnecting");
    for (const type of eventTypes) {
      es.addEventListener(type, (ev) => {
        try {
          applyEvent(qc, type, JSON.parse((ev as MessageEvent<string>).data));
          if (type === "snapshot") {
            setState("live");
          }
        } catch {
          // a broken event: the next one or the next snapshot repairs the picture
        }
      });
    }
    return () => es.close();
  }, [qc]);
  return <LiveContext.Provider value={state}>{children}</LiveContext.Provider>;
}

export function useLiveState(): LiveState {
  return React.useContext(LiveContext);
}

// fetchAllWorkers reads every page of GET /workers.
export async function fetchAllWorkers(): Promise<WorkerView[]> {
  const out: WorkerView[] = [];
  let cursor = "";
  for (;;) {
    const page = await api.get<{ items: WorkerView[]; nextCursor: string }>(
      "/workers?limit=500" + (cursor ? "&cursor=" + encodeURIComponent(cursor) : ""),
    );
    out.push(...page.items);
    if (!page.nextCursor) {
      return out;
    }
    cursor = page.nextCursor;
  }
}

export function useWorkers() {
  const live = useLiveState();
  return useQuery({
    queryKey: keys.workers,
    queryFn: fetchAllWorkers,
    staleTime: live === "live" ? Infinity : 0,
    refetchInterval: live === "live" ? false : 10_000,
  });
}

export function useHosts() {
  const live = useLiveState();
  return useQuery({
    queryKey: keys.hosts,
    queryFn: () => api.get<HostView[]>("/hosts"),
    staleTime: live === "live" ? Infinity : 0,
    refetchInterval: live === "live" ? false : 10_000,
  });
}

export function useWorker(id: string): WorkerView | undefined {
  const { data } = useWorkers();
  return data?.find((w) => w.id === id);
}
