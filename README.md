# automata-worker-monitor

Watches and controls the **workers** of Automata (the `automata-worker` orchestrator containers), from one web UI:

- **set up** new workers on a Docker host from a template (image, env file, limits, network);
- **put them down**: drain (finish the job, take no new one, exit), stop, kill, restart, start, remove;
- see **which workers are responsive**, and why not: `HEALTHY`, `DEGRADED`, `STUCK`, `UNRESPONSIVE`, `DOWN`,
  `STARTING`, `UNKNOWN`, with reason codes and an event timeline per worker;
- see their **resource consumption** per worker and per host (CPU, memory, pids, block I/O, disk, network), live and
  as history up to 14 days, with availability and utilisation;
- see **what each worker is busy with**: its phase, job id and routine key.

It is independent of the Automata core: it never calls the core's API, never reads its database and never touches its
queue, and the workers keep working with no monitor at all. There is no Prometheus, Grafana or alerting: problems are
seen in the UI.

```
browser ─► workers-control-plane      Go, SQLite, embedded React UI, REST + SSE, health, lifecycle, history
               │ every 5 s: snapshot, actions, logs (bearer token)
               ▼
           worker-machine-controller  one per Docker host: Docker API, stats, logs, host metrics
               │ local only: GET /status
               ▼
           worker containers          telemetry on TELEMETRY_ADDR: /healthz, /status
```

## Install

See [deploy/README.md](deploy/README.md): the images, a worker machine controller per host, the workers control plane,
the first template, upgrades.

## Try it with fakeworkers

The `fakeworker` implements the worker's telemetry contract with scripted scenarios (`normal`, `stuck`, `crash`,
`slow-status`, `no-status`, `cpu-hog`, `mem-hog`, `rabbit-down`, `core-errors`, `connector-silent`):

```bash
make images                 # the images, tagged dev
E2E_KEEP=1 make e2e         # runs the end-to-end test and leaves its stack up at http://127.0.0.1:19690
```

## Develop

Go 1.26, Node 20, Docker.

| Command | What it does |
|---|---|
| `make build` | the UI, then the binaries into `bin/` with the UI embedded (build tag `webui`) |
| `make build-noweb` | the binaries without the UI (no Node needed; the control plane serves a placeholder page) |
| `make test` | Go unit tests with the race detector |
| `make web-check` | UI lint and tests (`cd web && npm run dev` for the UI against a local control plane on 9490) |
| `make test-integration` | the worker machine controller against a real Docker daemon (needs `automata-fakeworker:dev`) |
| `make e2e` | the images, then the end-to-end scenarios in compose (`test/e2e/run.sh`) |
| `make load` | the images, then 50 fakeworkers and the monitor's CPU and memory (`test/e2e/load.sh`) |
| `make images` | one image per `Dockerfile.<binary>` |

| Path | |
|---|---|
| `cmd/worker-machine-controller`, `cmd/workers-control-plane`, `cmd/fakeworker` | the three binaries |
| `internal/machinecontroller/…` | the worker machine controller (Docker, discovery, stats, telemetry scrape, provisioning, logs, host metrics, API) |
| `internal/controlplane/…` | the workers control plane (config, SQLite store, health, lifecycle, fleet, history, SSE, API) |
| `internal/workerapi`, `internal/controllerapi`, `contracts/` | the contracts and their JSON samples |
| `web/` | the UI (React, Vite, TypeScript, Tailwind, TanStack, uPlot) |
| `deploy/` | the compose files |
| `test/e2e/` | the end-to-end test and the load check |
| `plan/` | the design: requirements, architecture, contracts, phases ([plan/README.md](plan/README.md)) |

The worker side (telemetry, signals, graceful shutdown) lives in the `automata-worker` repository; its contract with
the monitor is [plan/05-telemetry-contract.md](plan/05-telemetry-contract.md).
