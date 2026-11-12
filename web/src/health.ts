// How health and lifecycle are shown.
import {
  CircleCheck,
  CircleHelp,
  CircleX,
  Hourglass,
  Loader,
  TriangleAlert,
  Unplug,
  type LucideIcon,
} from "lucide-react";
import type { Health } from "@/types";

export interface HealthStyle {
  label: string;
  icon: LucideIcon | null;
  className: string; // badge colours
  color: string; // the health strip and the charts, as a CSS colour
  spin?: boolean;
}

export const healthStyles: Record<Health, HealthStyle> = {
  HEALTHY: { label: "Healthy", icon: CircleCheck, className: "border-success/30 bg-success/10 text-success", color: "hsl(142 65% 42%)" },
  DEGRADED: { label: "Degraded", icon: TriangleAlert, className: "border-warning/30 bg-warning/10 text-warning", color: "hsl(38 92% 50%)" },
  STUCK: { label: "Stuck", icon: Hourglass, className: "border-orange-500/30 bg-orange-500/10 text-orange-500", color: "hsl(25 95% 53%)" },
  UNRESPONSIVE: { label: "Unresponsive", icon: Unplug, className: "border-destructive/30 bg-destructive/10 text-destructive", color: "hsl(0 72% 60%)" },
  DOWN: { label: "Down", icon: CircleX, className: "border-transparent bg-destructive text-destructive-foreground", color: "hsl(0 72% 45%)" },
  STARTING: { label: "Starting", icon: Loader, className: "border-blue-500/30 bg-blue-500/10 text-blue-500", color: "hsl(217 91% 60%)", spin: true },
  UNKNOWN: { label: "Unknown", icon: CircleHelp, className: "border-border bg-muted text-muted-foreground", color: "hsl(240 4% 60%)" },
  NONE: { label: "—", icon: null, className: "", color: "transparent" },
};

// the order of the problems: the worst first, so that the fleet table lists them first
export const healthOrder: Health[] = ["DOWN", "UNRESPONSIVE", "STUCK", "DEGRADED", "UNKNOWN", "STARTING", "HEALTHY", "NONE"];

export function healthRank(h: Health): number {
  const i = healthOrder.indexOf(h);
  return i < 0 ? healthOrder.length : i;
}

// isProblem: a worker that is not fine, counted in the tab title.
export function isProblem(h: Health): boolean {
  return h !== "HEALTHY" && h !== "NONE" && h !== "STARTING";
}

// One sentence per reason code. The health message of the worker view carries the numbers; these explain
// the codes in the badge tooltip.
const reasonTexts: Record<string, string> = {
  WORKER_MACHINE_CONTROLLER_UNREACHABLE: "The worker machine controller of the host does not answer; the worker may be fine.",
  CONTAINER_MISSING: "The container of the worker is missing.",
  CONTAINER_RESTARTING: "Docker is restarting the container.",
  CONTAINER_EXITED: "The container has exited.",
  OOM_KILLED: "The container was killed for using too much memory.",
  NO_TELEMETRY: "The worker has no telemetry; only Docker is watched.",
  NO_PROGRESS: "The main loop of the worker has made no progress within the budget of its phase.",
  RABBITMQ_DOWN: "The worker lost its connection to RabbitMQ.",
  CORE_ERRORS: "The worker's calls to the core are failing.",
  OBJECT_STORAGE_ERRORS: "The worker's calls to the object storage are failing.",
  CONNECTOR_SILENT: "The connector of the running job has gone silent.",
  MEMORY_NEAR_LIMIT: "The container is near its memory limit.",
  RESTARTED_RECENTLY: "The container restarted recently.",
  SLOW_STATUS: "The worker answers its status slowly.",
  DUPLICATE_ID: "Another container carries the same worker id.",
  DOCKER_UNHEALTHY: "Docker says the container is unhealthy although its status is fine.",
  TIMEOUT: "Reading the status of the worker timed out.",
  CONNECTION_REFUSED: "The telemetry port of the worker refused the connection.",
};

export function reasonText(code: string): string {
  return reasonTexts[code] ?? "The status of the worker could not be read (" + code + ").";
}

const lifecycleReasons: Record<string, string> = {
  DRAINED: "drained",
  DRAIN_TIMEOUT: "drain timeout",
  EXITED_CLEANLY: "exited by itself",
  DISAPPEARED: "removed outside the monitor",
  PROVISION_TIMEOUT: "no good status in time",
};

export function lifecycleText(lifecycle: string, reason: string): string {
  const l = lifecycle.replace(/_/g, " ").toLowerCase();
  return reason ? `${l} (${lifecycleReasons[reason] ?? reason.toLowerCase()})` : l;
}
