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

## Deploy

This is how you get workers that take jobs from the [Automata core](https://github.com/anovimous/automata): the core
starts no worker, and queued jobs are not executed until one is running. You need Docker and Docker Compose, Linux with
cgroup v2 on the worker hosts, and a running core that the worker hosts can reach (RabbitMQ `5672`, RustFS `9000`, API
`8080`). In short:

1. **Build the worker image** (`automata-worker:<tag>`) from the [automata-worker](https://github.com/anovimous/automata-worker)
   repository, on every worker host or once and copied.
2. **Build the monitor images** here: `make images VERSION=0.1.0`.
3. **Install a worker machine controller on each worker host**: `deploy/worker-machine-controller`, with a token, the
   host name and `env/default.env`, the credentials the workers use to reach the core.
4. **Install the workers control plane** once: `deploy/workers-control-plane`, with the address and token of each worker
   machine controller. Its UI is at `http://127.0.0.1:9490` (there is no login: keep it on loopback or behind a proxy).
5. **Create a template and the workers** in the UI (**Templates → New template**, then **Fleet → New worker**), and check
   that they become `HEALTHY`.

The details of every step, the settings, upgrades, token changes and backup are in [deploy/README.md](deploy/README.md).

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

The worker side (telemetry, signals, graceful shutdown) lives in the `automata-worker` repository; its contract with
the monitor is `docs/telemetry-contract.md` in that repository.
