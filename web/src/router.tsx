import { createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { Shell } from "@/components/Shell";
import { strings } from "@/filter";
import { FleetPage } from "@/routes/fleet";
import { HostPage } from "@/routes/host";
import { HostsPage } from "@/routes/hosts";
import { TemplatesPage } from "@/routes/templates";
import { WorkerPage, workerTabs, type WorkerTab } from "@/routes/worker";
import { ranges } from "@/charts";
import type { Range } from "@/types";

function rangeOf(v: unknown): Range | undefined {
  return ranges.some((r) => r.value === v) ? (v as Range) : undefined;
}

const root = createRootRoute({ component: Shell, notFoundComponent: () => <p className="text-sm text-muted-foreground">No such page.</p> });

const fleet = createRoute({
  getParentRoute: () => root,
  path: "/",
  component: FleetPage,
  validateSearch: (s: Record<string, unknown>) => strings(s, ["host", "health", "lifecycle", "phase", "routine", "q", "avail"]),
});

const worker = createRoute({
  getParentRoute: () => root,
  path: "/workers/$id",
  component: WorkerPage,
  validateSearch: (s: Record<string, unknown>): { tab?: WorkerTab; range?: Range } => ({
    tab: workerTabs.includes(s.tab as WorkerTab) ? (s.tab as WorkerTab) : undefined,
    range: rangeOf(s.range),
  }),
});

const hosts = createRoute({ getParentRoute: () => root, path: "/hosts", component: HostsPage });

const host = createRoute({
  getParentRoute: () => root,
  path: "/hosts/$name",
  component: HostPage,
  validateSearch: (s: Record<string, unknown>): { range?: Range } => ({ range: rangeOf(s.range) }),
});

const templates = createRoute({ getParentRoute: () => root, path: "/templates", component: TemplatesPage });

export const router = createRouter({ routeTree: root.addChildren([fleet, worker, hosts, host, templates]) });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
