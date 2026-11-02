# Deploying automata-worker-monitor

Two parts, each a compose file in this folder:

- **`worker-machine-controller/`** — one per Docker host that runs workers. It talks to the local Docker daemon,
  reads the workers' telemetry and creates, signals, stops and removes worker containers. It is root-equivalent on its
  host: keep it on loopback (or a trusted network), with a strong token.
- **`workers-control-plane/`** — one in total. It polls every worker machine controller every 5 s, keeps the state
  and the history in SQLite and serves the web UI and its API on `127.0.0.1:9490`.

The workers never depend on the monitor: they keep working when it is stopped, removed or broken.

## 1. Build the images

On a machine with Docker and the repository (the Go and Node builds run inside Docker):

```bash
make images VERSION=0.1.0
```

This gives `automata-worker-machine-controller:0.1.0` and `automata-workers-control-plane:0.1.0` (and
`automata-fakeworker:dev`, for tests). For another host, `docker save … | ssh host docker load` or push them to your
registry.

## 2. Install a worker machine controller on a host

```bash
cd deploy/worker-machine-controller
cp worker-machine-controller.env.example worker-machine-controller.env
cp env/default.env.example env/default.env
chmod 600 worker-machine-controller.env env/*.env
```

- `worker-machine-controller.env`:
  - `WORKER_MACHINE_CONTROLLER_TOKEN`: at least 32 characters, e.g. `head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'`.
  - `WORKER_MACHINE_CONTROLLER_HOST_NAME`: the host's name in the monitor and in the worker ids (`w-<host>-xxxxxx`).
  - `WORKER_MACHINE_CONTROLLER_ALLOWED_IMAGES`: the images workers may be created from (glob patterns,
    comma-separated), e.g. `automata-worker:*`.
  - `WORKER_MACHINE_CONTROLLER_LISTEN_ADDR`: keep `127.0.0.1:9480` when the workers control plane runs on the same
    machine. Another address needs TLS (`…_TLS_CERT`, `…_TLS_KEY`) or `…_ALLOW_PLAINTEXT=true` on a trusted network.
    TLS is supported but has not been tested end to end.
- `env/default.env`: the environment of the workers whose template names the env file `default` (RabbitMQ, RustFS,
  core URLs and credentials). One file per env file name; every host needs the files its templates name. The
  values never leave the host: the monitor only sees the file names and the keys.

```bash
MONITOR_VERSION=0.1.0 docker compose up -d
docker logs automata-worker-machine-controller
```

## 3. Add the host to the workers control plane

```bash
cd deploy/workers-control-plane
cp config.yml.example config.yml
cp workers-control-plane.env.example workers-control-plane.env
chmod 600 workers-control-plane.env
```

- `config.yml` → `hosts`: one entry per worker machine controller, with its URL and `controlToken:
  "${HOST1_WORKER_MACHINE_CONTROLLER_TOKEN}"` (a `${NAME}` is read from the environment); `maxWorkers` caps the
  workers created on that host. The other keys have the defaults written in the example.
- `workers-control-plane.env`: the tokens, e.g. `HOST1_WORKER_MACHINE_CONTROLLER_TOKEN=<the token of step 2>`.

```bash
MONITOR_VERSION=0.1.0 docker compose up -d
curl -s http://127.0.0.1:9490/healthz
```

The compose file uses host networking, so the workers control plane reaches a worker machine controller of the same
machine at `127.0.0.1:9480`. To use a bridge network instead: set `listen: 0.0.0.0:9490`, publish
`127.0.0.1:9490:9490`, add `extra_hosts: ["host.docker.internal:host-gateway"]` and make the worker machine
controller listen on an address the bridge reaches.

There is **no login**: whoever reaches port 9490 can run every action. Open the UI from that machine
(`http://127.0.0.1:9490`, or an SSH tunnel), or put a reverse proxy with TLS and authentication in front of it. The
control plane refuses requests whose `Origin` is another site.

A new host later: install its worker machine controller (step 2), add it to `config.yml`, restart the workers control
plane.

## 4. The first template

Open the UI → **Templates → New template**:

- **Image**: the worker image with its tag (e.g. `automata-worker:0.4.0`; `latest` is refused).
- **Env file**: `default` (the file of step 2).
- **Network**: on the machine of the core's compose stack, the core's network (`automata_backend`) so the env file
  can use the service names (`rabbitmq`, `rustfs`, `app`); elsewhere a network of your own, or `host`.
- **CPUs, memory, pids, wordlist cache size**: the limits of each worker (each worker has its own cache volume).
- **Drain timeout**: 0 waits for the job however long it takes; with a timeout the template can stop the worker
  after it, or remove it after the drain.

The first start seeds a template named `default` for `automata-worker:0.4.0` (`seedTemplates: true`); edit it
instead if that suits you. Then **Fleet → New worker**: host, template, count.

## 5. Upgrading

- **The monitor**: build the new images, then on each host and on the control plane
  `MONITOR_VERSION=<new> docker compose up -d`. The workers keep running meanwhile; while a worker machine controller
  is down its workers show `UNKNOWN`, never `DOWN`. The database migrates itself on start.
- **The worker image**: change the image tag of the template (or make a new template), create new workers from it,
  then drain the old ones. A worker keeps the template it was created with.

## 6. Changing a worker machine controller's token

A worker machine controller accepts one token, so a change is a short gap, not a rollover:

1. Make a new token (step 2) and write it in the host's `worker-machine-controller.env` and in the control plane's
   `workers-control-plane.env`.
2. `docker compose up -d` in `worker-machine-controller/` on the host, then in `workers-control-plane/`.

Between the two restarts the control plane gets `401` from that host: its workers show `UNKNOWN` after 15 s and come
back on the next poll. The workers themselves are not touched.

## 7. Backup and operations

- **Backup**: the SQLite file in the `monitor_data` volume (`/var/lib/automata-monitor/monitor.db`): stop the
  container and copy it, or copy it with `sqlite3 … ".backup"`. If it is lost, the templates, the events and the
  history are lost; the workers are found again on the next poll (from their labels) and come back as the monitor's.
- **History**: one row per worker and per host per minute, kept `history.retentionDays` (14), purged hourly.
  About 120 bytes per worker row: 20 workers × 14 days ≈ 50 MB. The size is shown at the bottom of the hosts page.
- **Disk**: each worker has a wordlist cache volume and a temp volume; the hosts page shows the Docker data root's
  usage. Removing a worker asks whether to delete its volumes.
- **Logs**: `docker logs automata-workers-control-plane`, `docker logs automata-worker-machine-controller` (JSON;
  credentials are redacted).

## 8. Measured load

`make load` (`test/e2e/load.sh`): 50 fakeworkers on one host, all `HEALTHY` during 5 minutes, CPU and memory of the
two containers sampled every 5 s with `docker stats`. Measured on 2026-11-02 on a 12-CPU, 15 GiB host (Linux 7.0,
Docker 29.6.1), images tagged dev:

| Container | CPU, % of one core (average / highest sample) | Memory (average / highest) |
|---|---|---|
| worker machine controller | 1.34 % / 4.41 % | 16.0 MiB / 17.5 MiB |
| workers control plane | 0.20 % / 1.36 % | 15.5 MiB / 17.4 MiB |

The budget of plan/02 §4 for the worker machine controller with 50 workers is under 2 % of one core and under
100 MB: met on average; single samples go higher for a moment (the volume sizes are read every 60 s).
