-- The registry of the workers control plane (plan/07-workers-control-plane.md §2). There is no login (O3 rejected)
-- and no audit (O2 rejected): no sessions, no audit_log, no actor on the events. Times are RFC 3339 UTC text.

CREATE TABLE host_state (
  name            TEXT PRIMARY KEY,           -- hosts[].name of the configuration
  cordoned        INTEGER NOT NULL DEFAULT 0, -- 1: no new workers here
  cordoned_reason TEXT NOT NULL DEFAULT '',
  last_seen_at    TEXT                        -- last successful snapshot
);

CREATE TABLE templates (
  id                           INTEGER PRIMARY KEY,
  name                         TEXT NOT NULL UNIQUE,
  description                  TEXT NOT NULL DEFAULT '',
  image                        TEXT NOT NULL,
  pull_policy                  TEXT NOT NULL CHECK (pull_policy IN ('always', 'if-missing', 'never')),
  env_file                     TEXT NOT NULL,           -- the name of an env file on the host, never its content
  env_json                     TEXT NOT NULL DEFAULT '{}',
  network                      TEXT NOT NULL,           -- host or a network name
  extra_hosts_json             TEXT NOT NULL DEFAULT '[]',
  cpus                         REAL NOT NULL CHECK (cpus > 0),
  memory_bytes                 INTEGER NOT NULL CHECK (memory_bytes >= 268435456),
  pids_limit                   INTEGER NOT NULL DEFAULT 512,
  wordlist_cache_max_bytes     INTEGER NOT NULL,
  tmpfs_sockets_bytes          INTEGER NOT NULL DEFAULT 16777216,
  restart_max_retries          INTEGER NOT NULL DEFAULT 5,
  stop_timeout_seconds         INTEGER NOT NULL DEFAULT 60 CHECK (stop_timeout_seconds BETWEEN 45 AND 600),
  drain_timeout_seconds        INTEGER NOT NULL DEFAULT 0,  -- 0: wait forever
  escalate_after_drain_timeout INTEGER NOT NULL DEFAULT 0,
  remove_after_drain           INTEGER NOT NULL DEFAULT 0,
  extra_labels_json            TEXT NOT NULL DEFAULT '{}',
  created_at                   TEXT NOT NULL,
  updated_at                   TEXT NOT NULL
);

CREATE TABLE workers (
  id                TEXT PRIMARY KEY,          -- the worker id, label automata.worker.id
  host              TEXT NOT NULL,
  template_id       INTEGER REFERENCES templates(id) ON DELETE SET NULL,
  template_snapshot TEXT NOT NULL DEFAULT '{}', -- the template as used at the creation
  managed           INTEGER NOT NULL,
  container_id      TEXT NOT NULL DEFAULT '',
  lifecycle         TEXT NOT NULL,
  lifecycle_reason  TEXT NOT NULL DEFAULT '',
  lifecycle_since   TEXT NOT NULL,
  drain_deadline    TEXT,                      -- set while DRAINING with a timeout
  health            TEXT NOT NULL DEFAULT 'UNKNOWN',
  health_reasons    TEXT NOT NULL DEFAULT '[]',
  health_since      TEXT NOT NULL,
  last_seen_at      TEXT,                      -- last successful scrape of the telemetry
  last_status_json  TEXT,                      -- the last status document, shown after a restart
  version           TEXT NOT NULL DEFAULT '',
  created_at        TEXT NOT NULL,
  removed_at        TEXT                       -- soft delete, purged after the retention
);
CREATE INDEX workers_host ON workers(host) WHERE removed_at IS NULL;

CREATE TABLE worker_events (
  id         INTEGER PRIMARY KEY,
  worker_id  TEXT NOT NULL,
  at         TEXT NOT NULL,
  kind       TEXT NOT NULL,   -- HEALTH_CHANGED, LIFECYCLE_CHANGED, ACTION, CONTAINER_RESTARTED, OOM_KILLED,
                              -- JOB_STARTED, JOB_ENDED, ADOPTED, DISAPPEARED, PROVISION_FAILED, DRAIN_TIMEOUT
  from_value TEXT NOT NULL DEFAULT '',
  to_value   TEXT NOT NULL DEFAULT '',
  reasons    TEXT NOT NULL DEFAULT '[]',
  message    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX worker_events_worker_at ON worker_events(worker_id, at DESC);
CREATE INDEX worker_events_kind_at ON worker_events(kind, at);
