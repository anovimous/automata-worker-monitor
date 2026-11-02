-- The history of the workers and of the hosts: one row per minute (plan/09-history.md §1.2). An average is weighted by
-- the polls that had its value: resource_polls for the resources, rate_polls for the I/O rates (a first poll has no
-- rate yet). The per-worker network and job columns of the plan are left out: R3 and R5 are delayed.

CREATE TABLE worker_samples (
  worker_id        TEXT    NOT NULL,
  minute           INTEGER NOT NULL,   -- unix seconds, floored to 60
  polls            INTEGER NOT NULL,   -- how many polls fed this row (12 at 5 s)
  resource_polls   INTEGER NOT NULL,
  rate_polls       INTEGER NOT NULL,
  cpu_cores_avg    REAL, cpu_cores_max REAL, cpu_limit REAL,
  mem_ws_avg       INTEGER, mem_ws_max INTEGER, mem_limit INTEGER,
  pids_max         INTEGER,
  blk_read_bps     REAL, blk_write_bps REAL,
  health_worst     TEXT    NOT NULL,   -- DOWN > UNRESPONSIVE > STUCK > DEGRADED > UNKNOWN > STARTING > HEALTHY > NONE
  responsive_polls INTEGER NOT NULL,   -- polls with health HEALTHY or DEGRADED
  busy_polls       INTEGER NOT NULL,   -- polls with a job (PREPARING … FINISHING)
  phase_last       TEXT    NOT NULL DEFAULT '',
  job_id_last      INTEGER,            -- the job at the end of the minute, NULL when idle
  routine_key_last TEXT,
  PRIMARY KEY (worker_id, minute)
) WITHOUT ROWID;
CREATE INDEX worker_samples_minute ON worker_samples(minute);

CREATE TABLE host_samples (
  host            TEXT    NOT NULL,
  minute          INTEGER NOT NULL,
  polls           INTEGER NOT NULL,
  reachable_polls INTEGER NOT NULL,
  cpu_ratio_avg   REAL, cpu_ratio_max REAL,
  mem_used_avg    INTEGER, mem_total INTEGER,
  load1_avg       REAL,
  disk_json       TEXT    NOT NULL DEFAULT '[]',  -- [{path, usedRatioMax}]
  net_rx_bps      REAL, net_tx_bps REAL,
  PRIMARY KEY (host, minute)
) WITHOUT ROWID;
CREATE INDEX host_samples_minute ON host_samples(minute);
