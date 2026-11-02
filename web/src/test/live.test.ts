import { QueryClient, type InfiniteData } from "@tanstack/react-query";
import { applyEvent, keys, prependEvent, removeWorker, upsertWorker } from "@/live";
import type { HostView, WorkerEvent, WorkerView } from "@/types";
import { host, worker } from "./fixtures";

const event = (id: number, workerId = "w-host1-aaaaaa"): WorkerEvent => ({
  id,
  workerId,
  at: "2026-11-02T10:00:00Z",
  kind: "HEALTH_CHANGED",
  from: "HEALTHY",
  to: "STUCK",
  reasons: [],
  message: "",
});

describe("cache patching", () => {
  it("upserts and removes workers, kept by id", () => {
    const a = worker({ id: "w-b" });
    let list = upsertWorker(undefined, a);
    list = upsertWorker(list, worker({ id: "w-a" }));
    expect(list.map((w) => w.id)).toEqual(["w-a", "w-b"]);
    list = upsertWorker(list, worker({ id: "w-b", health: "DOWN" }));
    expect(list.find((w) => w.id === "w-b")?.health).toBe("DOWN");
    expect(list).toHaveLength(2);
    expect(removeWorker(list, "w-a")?.map((w) => w.id)).toEqual(["w-b"]);
  });

  it("adds an event to a loaded timeline once", () => {
    const data: InfiniteData<WorkerEvent[]> = { pages: [[event(2)], [event(1)]], pageParams: [0, 2] };
    const next = prependEvent(data, event(3))!;
    expect(next.pages[0].map((e) => e.id)).toEqual([3, 2]);
    expect(prependEvent(next, event(3))).toBe(next);
    expect(prependEvent(undefined, event(4))).toBeUndefined();
  });

  it("applies the server-sent events", () => {
    const qc = new QueryClient();
    applyEvent(qc, "snapshot", { workers: [worker()], hosts: [host()] });
    expect(qc.getQueryData<WorkerView[]>(keys.workers)).toHaveLength(1);

    applyEvent(qc, "worker.updated", worker({ id: "w-host1-bbbbbb" }));
    applyEvent(qc, "worker.updated", worker({ health: "STUCK" }));
    const workers = qc.getQueryData<WorkerView[]>(keys.workers)!;
    expect(workers.map((w) => [w.id, w.health])).toEqual([
      ["w-host1-aaaaaa", "STUCK"],
      ["w-host1-bbbbbb", "HEALTHY"],
    ]);

    applyEvent(qc, "worker.removed", { id: "w-host1-bbbbbb" });
    expect(qc.getQueryData<WorkerView[]>(keys.workers)).toHaveLength(1);

    applyEvent(qc, "host.updated", host({ reachable: false }));
    expect(qc.getQueryData<HostView[]>(keys.hosts)![0].reachable).toBe(false);

    // an event of a timeline that is not loaded is left out; a loaded one gets it
    applyEvent(qc, "worker.event", event(9));
    expect(qc.getQueryData(keys.events("w-host1-aaaaaa"))).toBeUndefined();
    qc.setQueryData<InfiniteData<WorkerEvent[]>>(keys.events("w-host1-aaaaaa"), { pages: [[]], pageParams: [0] });
    applyEvent(qc, "worker.event", event(10));
    expect(qc.getQueryData<InfiniteData<WorkerEvent[]>>(keys.events("w-host1-aaaaaa"))!.pages[0][0].id).toBe(10);
  });
});
