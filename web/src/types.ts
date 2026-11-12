// The JSON of the workers control plane API, kept by hand in step with the Go types of
// internal/controlplane/fleet, internal/controlplane/store and internal/controllerapi.

export type Health = "HEALTHY" | "DEGRADED" | "STUCK" | "UNRESPONSIVE" | "DOWN" | "STARTING" | "UNKNOWN" | "NONE";

export type Lifecycle =
  | "PROVISIONING"
  | "ACTIVE"
  | "DRAINING"
  | "STOPPING"
  | "STOPPED"
  | "REMOVING"
  | "REMOVED"
  | "PROVISION_FAILED";

export type Action = "drain" | "undrain" | "stop" | "kill" | "restart" | "start" | "remove";

export interface JobInfo {
  jobId: number;
  routineKey: string;
  command: string;
  state: string;
  takenAt: string;
  startedAt: string | null;
}

export interface Activity {
  phase: string;
  phaseDetail: string;
  phaseSince: string;
  progressAgeSeconds: number;
  phaseBudgetSeconds: number;
  draining: boolean;
  shuttingDown: boolean;
  job: JobInfo | null;
  statusAt: string;
  stale: boolean;
}

export interface Resources {
  cpuCores: number;
  cpuLimit: number | null;
  memoryWorkingSetBytes: number;
  memoryLimitBytes: number | null;
  pids: number;
  pidsLimit: number | null;
  blockReadBytesPerSec: number | null;
  blockWriteBytesPerSec: number | null;
}

export interface Volume {
  name: string;
  mountPath: string;
  bytes: number | null;
}

export interface Limits {
  cpus: number | null;
  memoryBytes: number | null;
  pids: number | null;
}

export interface Counters {
  messagesTaken: number;
  messagesSkipped: number;
  jobsStarted: number;
  jobsByOutcome: Record<string, number>;
}

export interface WorkerView {
  id: string;
  host: string;
  managed: boolean;
  template: string;
  image: string;
  version: string;
  containerId: string;
  lifecycle: Lifecycle;
  lifecycleReason: string;
  lifecycleSince: string;
  drainDeadline: string | null;
  health: Health;
  healthReasons: string[];
  healthMessage: string;
  healthSince: string;
  responsive: boolean;
  lastSeenAt: string | null;
  uptimeSeconds: number | null;
  restartCount: number;
  lastExitCode: number | null;
  oomKills: number;
  activity: Activity | null;
  resources: Resources | null;
  wordlistCache: { bytes: number; maxBytes: number; files: number } | null;
  volumes: { sampledAt: string; items: Volume[] } | null;
  workDirBytes: number | null;
  counters: Counters | null;
  limits: Limits;
  createdAt: string;
  allowedActions: Action[];
}

export interface Disk {
  path: string;
  totalBytes: number;
  freeBytes: number;
}

export interface HostMetrics {
  sampledAt: string;
  cpus: number;
  cpuUsageRatio: number;
  load1: number;
  load5: number;
  load15: number;
  memoryTotalBytes: number;
  memoryAvailableBytes: number;
  swapUsedBytes: number;
  disks: Disk[];
  volumesBytes: number | null;
  networks: { interface: string; rxBytesPerSec: number; txBytesPerSec: number }[];
  uptimeSeconds: number;
  kernel: string;
  dockerVersion: string;
}

export interface HostView {
  name: string;
  url: string;
  reachable: boolean;
  lastSeenAt: string | null;
  cordoned: boolean;
  cordonedReason: string;
  workerMachineControllerVersion: string;
  metrics: HostMetrics | null;
  workers: { total: number; running: number; responsive: number; busy: number };
  maxWorkers: number;
  capacity: { cpusReserved: number; memoryBytesReserved: number; cpus: number; memoryBytes: number };
}

export interface Overview {
  workers: number;
  byHealth: Record<string, number>;
  byLifecycle: Record<string, number>;
  busy: number;
  idle: number;
  routines: Record<string, number>;
  hosts: { name: string; reachable: boolean; cordoned: boolean; workers: number }[];
}

export interface WorkerEvent {
  id: number;
  workerId: string;
  at: string;
  kind: string;
  from: string;
  to: string;
  reasons: string[];
  message: string;
}

export interface Template {
  id: number;
  name: string;
  description: string;
  image: string;
  pullPolicy: "always" | "if-missing" | "never";
  envFile: string;
  env: Record<string, string>;
  network: string;
  extraHosts: string[];
  cpus: number;
  memoryBytes: number;
  pidsLimit: number;
  wordlistCacheMaxBytes: number;
  tmpfsSocketsBytes: number;
  restartMaxRetries: number;
  stopTimeoutSeconds: number;
  drainTimeoutSeconds: number;
  escalateAfterDrainTimeout: boolean;
  removeAfterDrain: boolean;
  extraLabels: Record<string, string>;
  createdAt: string;
  updatedAt: string;
}

export type TemplateInput = Omit<Template, "id" | "createdAt" | "updatedAt">;

export interface ContainerDetails {
  imageId: string;
  created: string;
  envKeys: string[];
  mounts: { type: string; source: string; destination: string }[];
  restartPolicy: { name: string; maxRetries: number };
  stopTimeoutSeconds: number | null;
}

export interface WorkerDetailResponse {
  worker: WorkerView;
  templateSnapshot: Partial<Template> | null;
  // the worker machine controller's GET /v1/workers/{id}: the observation and the container details
  container: {
    container: {
      id: string;
      name: string;
      image: string;
      state: string;
      status: string;
      networkMode: string;
      ip: string;
      labels: Record<string, string>;
      limits: Limits;
    };
    telemetry: { configured: boolean; url: string; ok: boolean; lastError: string };
    details: ContainerDetails;
  } | null;
  containerError: string;
}

export type Range = "15m" | "1h" | "6h" | "24h" | "7d" | "14d";

export interface Series {
  name: string;
  unit: string;
  points: [number, number | string | null][];
}

export interface Timeseries {
  source: "buffer" | "samples";
  bucketSeconds: number;
  series: Series[];
}

export interface Availability {
  polls: number;
  availability: number | null;
  utilisation: number | null;
}

export interface UIConfig {
  pollIntervalSeconds: number;
  historyRetentionDays: number;
  databaseBytes: number;
}

export interface CreateResult {
  workers: { id: string; ok: boolean; error?: string }[];
  warnings: string[];
}

export interface LogLine {
  ts: string;
  stream: "stdout" | "stderr";
  line: string;
}
