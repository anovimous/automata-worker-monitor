# automata-worker-monitor — implementation plan

This directory holds the plan for **automata-worker-monitor**: an observability and control layer for the
workers of Automata (the `automata-worker` orchestrator containers). Nothing is implemented yet. The plan is
meant to be handed to an implementer (human or AI agent) who has **no other context**: everything needed about
Automata, the worker code and the decisions taken is written down here.

## What it is, in one paragraph

A separate system, independent of the Automata core, that lets an operator **start new workers**, **put workers
down** (drain gracefully, stop, kill, remove), see **which workers are responsive** (and why not), see their
**resource consumption** (CPU, memory, pids, disk I/O, network, restarts, OOM kills; per worker and per host), and
see **what each worker is doing right now** (idle, preparing, or running job `#4211` of routine `fuzz`). It is
made of a small HTTP telemetry surface added to the worker, a stateless **worker machine controller** on every Docker host, a
stateful **workers control plane** with a web UI and its own history in SQLite. There is no Prometheus, Grafana or alerting.
It never calls the core's API and never reads the core's database.

## Reading order

| File | What it contains |
|---|---|
| [plan/01-context.md](plan/01-context.md) | Self-contained description of Automata, the worker code (with file paths) and today's deployment, plus the facts found during the review that shape this design |
| [plan/02-requirements.md](plan/02-requirements.md) | Goals, must/should/could features, non-functional requirements, what is out of scope |
| [plan/03-architecture.md](plan/03-architecture.md) | Components, tech stack, data flows, key decisions with the alternatives that were rejected |
| [plan/04-worker-changes.md](plan/04-worker-changes.md) | Every change in the `automata-worker` repo, file by file, with tests |
| [plan/05-telemetry-contract.md](plan/05-telemetry-contract.md) | The contract between a worker and the monitor: status JSON, signals, env vars, container labels |
| [plan/06-worker-machine-controller.md](plan/06-worker-machine-controller.md) | Spec of `worker-machine-controller` (per host): Docker provisioning, discovery, stats, scraping, logs, API |
| [plan/07-workers-control-plane.md](plan/07-workers-control-plane.md) | Spec of `workers-control-plane`: data model, health engine, lifecycle state machine, REST/SSE API, auth |
| [plan/08-ui.md](plan/08-ui.md) | Screens and behaviour of the web UI |
| [plan/09-history.md](plan/09-history.md) | Resource and health history kept by the workers control plane (SQLite), charts and derived views |
| [plan/10-deployment-security.md](plan/10-deployment-security.md) | Compose files, configuration, secrets, threat model |
| [plan/11-phases.md](plan/11-phases.md) | Ordered implementation phases with tasks, acceptance criteria and test plan |
| [plan/12-risks-open-questions.md](plan/12-risks-open-questions.md) | Risks, limits, questions for the owner |
| [contracts/](contracts/) | JSON samples of the worker status and of the worker machine controller API, used by contract tests on both sides |

## Decisions in short

| Topic | Decision |
|---|---|
| Coupling to the core | None. The monitor never talks to the core (HTTP, DB or queue). Job id and routine key come from the worker's own status. Workers keep reporting to the core exactly as today. |
| Languages | Go 1.26 for the worker machine controller, workers control plane and worker changes (same as the worker). React + Vite + TypeScript + Tailwind + TanStack Query for the UI (same stack as `automata-frontend`), embedded in the workers control plane binary. |
| Topology | `worker-machine-controller` on every Docker host (talks to the local Docker Engine, scrapes the workers locally, stateless). One `workers-control-plane` (registry, health engine, lifecycle, API, UI, SQLite). |
| Worker telemetry | Pull. The orchestrator serves `GET /healthz` and `/status` (JSON, versioned) on `TELEMETRY_ADDR`. The worker does not know the monitor exists. |
| Worker control | Through Docker only: create/start, `SIGUSR1` = drain, `SIGUSR2` = undrain, `SIGTERM` (docker stop) = graceful abort, `SIGKILL` = kill, remove. No control endpoint on the worker. |
| Responsiveness | The worker reports its phase, the time it last made progress and the budget of the phase; the workers control plane combines that with container state and scrape success into one health value with reason codes. |
| Resources | Container level from the Docker stats API (worker machine controller), job/tool breakdown from the job's cgroups (worker), host level from `/proc` (worker machine controller). |
| History | Built into the workers control plane: 1 h at 5 s in memory, then 1-minute samples in SQLite (14 days by default). No Prometheus, Grafana or alerting (owner's decision); problems are seen in the UI. |
| State | SQLite in the workers control plane (templates, workers, events, audit). Docker labels are the source of truth for which containers exist. |

## Rules for whoever implements this

These come from the owner's working rules for the `automata-worker` repo and apply to the new repo as well.

- Do not commit until the owner confirms. Split the work into several commits when that reads better. Commit
  messages: `feat:`, `fix:`, `refactor:`, `build:`, `chore:`, `test:`, `docs:`, followed by a lowercase
  description (a scope is allowed, e.g. `feat(orchestrator): ...`). Do not add a co-author line. Leave pushing
  and merging to the owner.
- Write unit tests for every new function or method and for every one you modify.
- Write code that reads like the surrounding code: in the worker, short comments that explain *why*, `log/slog`
  imported as `log`, errors wrapped with `%w`, constructors named `New`.
- In the worker repo, a change to the contract with the core must also update `docs/worker-contract.md` in the
  core repo (`/home/haidar/dev/automata/code/automata`) in the same change. The telemetry contract (new) is
  documented in the worker repo itself (see [plan/05](plan/05-telemetry-contract.md)).
- Work phase by phase ([plan/11](plan/11-phases.md)). Each phase ends with its acceptance checks passing.
- Build only the features flagged `I` in [plan/02](plan/02-requirements.md) §3. Skip `D` and `R`, and ask the
  owner about a feature with no flag before starting a phase that contains it. The flag is the final decision: it
  overrides the priority and the other plan files, and only the owner changes it.
