# Deploying automata-worker-monitor

This guide installs the monitor and then uses it to create the **workers** that take jobs from the Automata core. The core (in the [automata](https://github.com/anovimous/automata) repository) never runs a tool itself, and its `docker compose` stack starts no worker: **until a worker is running, queued jobs are not executed**. Following this guide gives you a web UI where you create workers, watch them and put them down.

## What gets installed

| Part | How many | Where | What it does |
|---|---|---|---|
| **Workers** (`automata-worker` containers) | as many as you need | the worker hosts | Take jobs from the core's RabbitMQ queue and run the tools. Created by the monitor, from a template. |
| **Worker machine controller** (`worker-machine-controller/`) | one per worker host | each Docker host that runs workers | Talks to the local Docker daemon: creates, signals, stops and removes worker containers, reads their telemetry and stats. It is root-equivalent on its host: keep it on loopback (or a trusted network), with a strong token. |
| **Workers control plane** (`workers-control-plane/`) | one in total | any machine that can reach the worker machine controllers | Polls every worker machine controller every 5 s, keeps the state and the history in SQLite, and serves the web UI and its API on `127.0.0.1:9490`. |

```
browser ─► workers control plane ─► worker machine controller (host 1) ─► worker, worker, ...
                                 └► worker machine controller (host 2) ─► worker, worker, ...
workers ─► core: RabbitMQ (5672), RustFS (9000), API (8080)
```

The workers never depend on the monitor: they keep working when it is stopped, removed or broken. A "worker host" can also be the machine that runs the core, and one machine can hold the control plane and a worker machine controller together.

## Before you start

- **Docker and Docker Compose** on every machine of this guide. The Go and Node builds run inside Docker, so nothing else is needed.
- **Linux with cgroup v2** on the worker hosts. Workers run as `privileged` containers because they create a cgroup per job.
- **A running Automata core** (`docker compose up` in the `automata` repository), reachable from every worker host on RabbitMQ (`5672`), RustFS (`9000`) and the API (`8080`). The core's stack creates the queue and the buckets the workers use.
  - Have the `RABBITMQ_PASSWORD` and `RUSTFS_PASSWORD` from the core's `.env` at hand (step 3).
  - The core's compose file publishes these ports on all interfaces and the core has no authentication: from other machines, allow only the worker hosts (firewall or VPN).
- **This repository** on each worker host and on the control plane machine, since the compose files are in `deploy/`:

  ```bash
  git clone https://github.com/anovimous/automata-worker-monitor.git
  ```

Steps 1 and 2 build images. Build them once and copy them to the other hosts with `docker save <image> | ssh <host> docker load` (or push them to your registry).

## 1. Build the worker image

The monitor creates workers from an image that must exist on the worker host. On a machine with Docker, in the [automata-worker](https://github.com/anovimous/automata-worker) repository:

```bash
git clone https://github.com/anovimous/automata-worker.git
cd automata-worker
docker build -t automata-worker:0.10.0 --build-arg VERSION=0.10.0 --build-arg COMMIT=$(git rev-parse --short HEAD) .
```

The tag (`0.10.0` here) is the one you will write in the template (step 5); `latest` is refused. The image contains the orchestrator, both connectors, `ffuf` and `masser`.

## 2. Build the monitor images

In this repository:

```bash
make images VERSION=0.1.0
```

This gives `automata-worker-machine-controller:0.1.0` and `automata-workers-control-plane:0.1.0` (and `automata-fakeworker:dev`, which only the tests use).

## 3. Install a worker machine controller on each worker host

```bash
cd deploy/worker-machine-controller
cp worker-machine-controller.env.example worker-machine-controller.env
cp env/default.env.example env/default.env
chmod 600 worker-machine-controller.env env/*.env
```

Edit `worker-machine-controller.env`:

| Variable | Value |
|---|---|
| `WORKER_MACHINE_CONTROLLER_TOKEN` | A secret of at least 32 characters, e.g. `head -c 32 /dev/urandom \| od -An -tx1 \| tr -d ' \n'`. The control plane needs the same value in step 4. |
| `WORKER_MACHINE_CONTROLLER_HOST_NAME` | The name of this host in the monitor and in the worker ids (`w-<host>-xxxxxx`): lowercase letters, digits and `-`. |
| `WORKER_MACHINE_CONTROLLER_ALLOWED_IMAGES` | The images workers may be created from, as comma-separated glob patterns. Keep `automata-worker:*` unless your image has another name. |
| `WORKER_MACHINE_CONTROLLER_LISTEN_ADDR` | Keep `127.0.0.1:9480` when the control plane runs on this machine. Another address needs TLS (`WORKER_MACHINE_CONTROLLER_TLS_CERT` and `…_TLS_KEY`, in the `tls/` folder) or `WORKER_MACHINE_CONTROLLER_ALLOW_PLAINTEXT=true` on a trusted network. TLS is supported but has not been tested end to end. |

Edit `env/default.env`. It is the environment of the workers whose template names the env file `default`: how they reach the core. The example already has the right variables; replace the two `CHANGE_ME`:

- `RABBITMQ_URL`: `amqp://automata:<RABBITMQ_PASSWORD>@localhost:5672/`
- `RUSTFS_SECRET_KEY`: `<RUSTFS_PASSWORD>`

Both passwords come from the core's `.env`. The addresses (`localhost`) are for a worker host that is the same machine as the core. On another machine, replace `localhost` with the core machine's address in `RABBITMQ_URL`, `RUSTFS_ENDPOINT` and `CENTRAL_BASE_URL`. You can have one file per env file name (`env/<name>.env`); every host needs the files its templates name, and their values never leave the host: the monitor only sees the file names and the keys.

Start it:

```bash
MONITOR_VERSION=0.1.0 docker compose up -d
docker logs automata-worker-machine-controller
```

## 4. Install the workers control plane

On the machine that will serve the UI:

```bash
cd deploy/workers-control-plane
cp config.yml.example config.yml
cp workers-control-plane.env.example workers-control-plane.env
chmod 600 workers-control-plane.env
```

- `config.yml`, under `hosts`: one entry per worker machine controller, with its `name`, its URL (`workerMachineControllerUrl`) and `controlToken: "${HOST1_WORKER_MACHINE_CONTROLLER_TOKEN}"` (a `${NAME}` is read from the environment). `maxWorkers` caps the workers created on that host. The other keys have the defaults written in the example.
- `workers-control-plane.env`: the tokens, e.g. `HOST1_WORKER_MACHINE_CONTROLLER_TOKEN=<the token of step 3>`. With several hosts, use one variable per host and name each in its `controlToken`.

```bash
MONITOR_VERSION=0.1.0 docker compose up -d
curl -s http://127.0.0.1:9490/healthz
```

The compose file uses host networking, so the control plane reaches a worker machine controller of the same machine at `127.0.0.1:9480`. To use a bridge network instead: set `listen: 0.0.0.0:9490` in `config.yml`, publish `127.0.0.1:9490:9490`, add `extra_hosts: ["host.docker.internal:host-gateway"]` and make the worker machine controller listen on an address the bridge reaches.

There is **no login**: whoever reaches port 9490 can run every action. Open the UI from that machine (`http://127.0.0.1:9490`, or an SSH tunnel), or put a reverse proxy with TLS and authentication in front of it. The control plane refuses requests whose `Origin` is another site.

**Adding a host later**: install its worker machine controller (step 3), add it to `config.yml` and its token to `workers-control-plane.env`, restart the control plane (`docker compose up -d`).

## 5. Create the first template and the workers

Open the UI (`http://127.0.0.1:9490`) → **Templates → New template**. A template is what workers are created from:

| Field | What to put |
|---|---|
| **Image** | The worker image with its tag, e.g. `automata-worker:0.10.0` (step 1; `latest` is refused). |
| **Env file** | `default`, the file of step 3. |
| **Network** | `host` (the default, like the worker's own compose file): the workers use the machine's network and `default.env` reaches the core through the ports it publishes (`localhost:5672`, `localhost:9000`, `http://localhost:8080`, or the core machine's address from another host). The worker machine controller gives each worker its own telemetry port (19100–19199). Alternatives: the core's compose network (`automata_backend`; it must exist, i.e. the core's stack is up), where `default.env` uses the service names (`rabbitmq:5672`, `rustfs:9000`, `http://app:8080`); or a network of your own named `automata-…` (created when missing) with the extra host `host.docker.internal:host-gateway`. The network and the addresses in the env file go together. |
| **CPUs, memory, pids, wordlist cache size** | The limits of each worker (each worker has its own cache volume). |
| **Drain timeout** | `0` waits for the job however long it takes. With a timeout, the template can stop the worker after it, or remove it after the drain. |

The first start of the control plane seeds a template named `default` (`seedTemplates: true` in `config.yml`) with an example image tag. Edit it and set its image to the tag you built in step 1 (`automata-worker:0.10.0`), or make a new template.

Then **Fleet → New worker**: choose the host, the template and how many workers. Each worker takes one job at a time, so more workers run more jobs in parallel.

## 6. Check that it works

1. In **Fleet**, the new workers show `STARTING`, then `HEALTHY` within about a minute. If one stays `UNRESPONSIVE` or goes `DOWN`, open it: its reason codes, events and logs say why (a wrong address or password in `env/default.env` is the usual cause).
2. In the core's UI, queue a `fuzz` job. The job goes `QUEUED` → `RUNNING` → `FINISHED`, and its worker shows the job id and the routine key while it is busy.

## 7. Upgrading

- **The monitor**: build the new images (step 2), then on each host and on the control plane `MONITOR_VERSION=<new> docker compose up -d`. The workers keep running meanwhile; while a worker machine controller is down its workers show `UNKNOWN`, never `DOWN`. The database migrates itself on start.
- **The worker image**: build it with a new tag (step 1), change the image tag of the template (or make a new template), create new workers from it, then drain the old ones. A worker keeps the template it was created with.

## 8. Changing a worker machine controller's token

A worker machine controller accepts one token, so a change is a short gap, not a rollover:

1. Make a new token (step 3) and write it in the host's `worker-machine-controller.env` and in the control plane's `workers-control-plane.env`.
2. `docker compose up -d` in `worker-machine-controller/` on the host, then in `workers-control-plane/`.

Between the two restarts the control plane gets `401` from that host: its workers show `UNKNOWN` after 15 s and come back on the next poll. The workers themselves are not touched.

## 9. Backup and operations

- **Backup**: the SQLite file in the `monitor_data` volume (`/var/lib/automata-monitor/monitor.db`): stop the container and copy it, or copy it with `sqlite3 … ".backup"`. If it is lost, the templates, the events and the history are lost; the workers are found again on the next poll (from their labels) and come back as the monitor's.
- **History**: one row per worker and per host per minute, kept `history.retentionDays` (14), purged hourly. About 120 bytes per worker row: 20 workers × 14 days ≈ 50 MB. The size is shown at the bottom of the hosts page.
- **Disk**: each worker has a wordlist cache volume and a temp volume; the hosts page shows the Docker data root's usage. Removing a worker asks whether to delete its volumes.
- **Logs**: `docker logs automata-workers-control-plane`, `docker logs automata-worker-machine-controller` (JSON; credentials are redacted).

## 10. Measured load

`make load` (`test/e2e/load.sh`): 50 fakeworkers on one host, all `HEALTHY` during 5 minutes, CPU and memory of the two containers sampled every 5 s with `docker stats`. Measured on 2026-11-02 on a 12-CPU, 15 GiB host (Linux 7.0, Docker 29.6.1), images tagged dev:

| Container | CPU, % of one core (average / highest sample) | Memory (average / highest) |
|---|---|---|
| worker machine controller | 1.34 % / 4.41 % | 16.0 MiB / 17.5 MiB |
| workers control plane | 0.20 % / 1.36 % | 15.5 MiB / 17.4 MiB |

The budget of plan/02 §4 for the worker machine controller with 50 workers is under 2 % of one core and under 100 MB: met on average; single samples go higher for a moment (the volume sizes are read every 60 s).
