import type { HostView, WorkerView } from "@/types";

export function worker(over: Partial<WorkerView> = {}): WorkerView {
  return {
    id: "w-host1-aaaaaa",
    host: "host1",
    managed: true,
    template: "default",
    image: "automata-worker:0.4.0",
    version: "0.4.0",
    containerId: "c1",
    lifecycle: "ACTIVE",
    lifecycleReason: "",
    lifecycleSince: "2026-11-02T10:00:00Z",
    drainDeadline: null,
    health: "HEALTHY",
    healthReasons: [],
    healthMessage: "",
    healthSince: "2026-11-02T10:00:00Z",
    responsive: true,
    lastSeenAt: "2026-11-02T10:00:00Z",
    uptimeSeconds: 60,
    restartCount: 0,
    lastExitCode: null,
    oomKills: 0,
    activity: null,
    resources: null,
    wordlistCache: null,
    volumes: null,
    workDirBytes: null,
    counters: null,
    limits: { cpus: 2, memoryBytes: 4 << 30, pids: 512 },
    createdAt: "2026-11-02T10:00:00Z",
    allowedActions: ["drain", "stop", "kill", "restart"],
    ...over,
  };
}

export function host(over: Partial<HostView> = {}): HostView {
  return {
    name: "host1",
    url: "http://127.0.0.1:9480",
    reachable: true,
    lastSeenAt: "2026-11-02T10:00:00Z",
    cordoned: false,
    cordonedReason: "",
    workerMachineControllerVersion: "0.1.0",
    metrics: null,
    workers: { total: 2, running: 2, responsive: 2, busy: 1 },
    maxWorkers: 10,
    capacity: { cpusReserved: 4, memoryBytesReserved: 8 * 1024 ** 3, cpus: 8, memoryBytes: 32 * 1024 ** 3 },
    ...over,
  };
}
