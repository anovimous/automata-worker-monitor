package main

import (
	"fmt"
	"io"
	log "log/slog"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type scenario string

// The scenarios of plan/11 P0, chosen with FAKE_SCENARIO.
const (
	scenarioNormal          scenario = "normal"
	scenarioStuck           scenario = "stuck"            // stops touching progress after the first job starts
	scenarioSlowStatus      scenario = "slow-status"      // GET /status answers after 3 s
	scenarioNoStatus        scenario = "no-status"        // the telemetry server never listens
	scenarioCrash           scenario = "crash"            // exits with 1 after 30 s
	scenarioCPUHog          scenario = "cpu-hog"          // burns one core while a job runs
	scenarioMemHog          scenario = "mem-hog"          // allocates up to 80 % of the container limit while a job runs
	scenarioRabbitDown      scenario = "rabbit-down"      // rabbitmq.up = false, takes no job
	scenarioCoreErrors      scenario = "core-errors"      // every poll of the core fails
	scenarioConnectorSilent scenario = "connector-silent" // the connector sends no message after it connected
)

var scenarios = []scenario{scenarioNormal, scenarioStuck, scenarioSlowStatus, scenarioNoStatus, scenarioCrash,
	scenarioCPUHog, scenarioMemHog, scenarioRabbitDown, scenarioCoreErrors, scenarioConnectorSilent}

// the same rule as the worker's WORKER_ID
var workerIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

const defaultTelemetryAddr = "127.0.0.1:9464"

type config struct {
	WorkerID      string
	TelemetryAddr string // "" when off
	LogFormat     string
	Scenario      scenario
	JobDuration   time.Duration
	IdleDuration  time.Duration
	Routines      []string

	HeartbeatIntervalSeconds int
	HeartbeatTimeoutSeconds  int
	WordlistCacheDir         string
	WordlistCacheMaxBytes    int64
}

// loadConfig reads the env vars of the worker that the fakeworker honours (WORKER_ID, TELEMETRY_ADDR, LOG_FORMAT,
// the heartbeat and wordlist cache settings) and its own FAKE_* vars. Like the worker, a wrong value is an error.
func loadConfig(getenv func(string) string, hostname func() (string, error)) (config, error) {
	cfg := config{}

	cfg.WorkerID = getenv("WORKER_ID")
	if cfg.WorkerID == "" {
		h, err := hostname()
		if err != nil {
			return config{}, fmt.Errorf("WORKER_ID is not set and the hostname is unknown: %w", err)
		}
		cfg.WorkerID = h
	}
	if !workerIDPattern.MatchString(cfg.WorkerID) {
		return config{}, fmt.Errorf("WORKER_ID %q does not match %s", cfg.WorkerID, workerIDPattern)
	}

	addr, err := parseTelemetryAddr(getenv("TELEMETRY_ADDR"))
	if err != nil {
		return config{}, err
	}
	cfg.TelemetryAddr = addr

	cfg.LogFormat = getenv("LOG_FORMAT")
	if cfg.LogFormat == "" {
		cfg.LogFormat = "text"
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		return config{}, fmt.Errorf("LOG_FORMAT %q is neither text nor json", cfg.LogFormat)
	}

	cfg.Scenario = scenario(getenv("FAKE_SCENARIO"))
	if cfg.Scenario == "" {
		cfg.Scenario = scenarioNormal
	}
	known := false
	for _, s := range scenarios {
		known = known || s == cfg.Scenario
	}
	if !known {
		return config{}, fmt.Errorf("FAKE_SCENARIO %q is not one of %v", cfg.Scenario, scenarios)
	}

	if cfg.JobDuration, err = seconds(getenv, "FAKE_JOB_SECONDS", 60); err != nil {
		return config{}, err
	}
	if cfg.IdleDuration, err = seconds(getenv, "FAKE_IDLE_SECONDS", 10); err != nil {
		return config{}, err
	}

	routines := getenv("FAKE_ROUTINES")
	if routines == "" {
		routines = "fuzz,masser"
	}
	for _, r := range strings.Split(routines, ",") {
		if r = strings.TrimSpace(r); r != "" {
			cfg.Routines = append(cfg.Routines, r)
		}
	}
	if len(cfg.Routines) == 0 {
		return config{}, fmt.Errorf("FAKE_ROUTINES %q names no routine", routines)
	}

	if cfg.HeartbeatIntervalSeconds, err = positiveInt(getenv, "WORKER_HEARTBEAT_INTERVAL_SECONDS", 5); err != nil {
		return config{}, err
	}
	if cfg.HeartbeatTimeoutSeconds, err = positiveInt(getenv, "WORKER_HEARTBEAT_TIMEOUT_SECONDS", 60); err != nil {
		return config{}, err
	}

	cfg.WordlistCacheDir = getenv("WORDLIST_CACHE_DIR")
	if cfg.WordlistCacheDir == "" {
		cfg.WordlistCacheDir = "/var/cache/automata/wordlists"
	}
	cfg.WordlistCacheMaxBytes = 1610612736
	if v := getenv("WORDLIST_CACHE_MAX_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return config{}, fmt.Errorf("WORDLIST_CACHE_MAX_BYTES %q is not a number above 0", v)
		}
		cfg.WordlistCacheMaxBytes = n
	}

	return cfg, nil
}

// parseTelemetryAddr applies the worker's rule: empty is the default, "off" disables the server, anything else
// must be host:port with a port 1–65535.
func parseTelemetryAddr(v string) (string, error) {
	switch v {
	case "":
		return defaultTelemetryAddr, nil
	case "off":
		return "", nil
	}
	_, port, err := net.SplitHostPort(v)
	if err != nil {
		return "", fmt.Errorf("TELEMETRY_ADDR %q is not host:port: %w", v, err)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("TELEMETRY_ADDR %q has no port between 1 and 65535", v)
	}
	return v, nil
}

func seconds(getenv func(string) string, key string, def int) (time.Duration, error) {
	n, err := positiveInt(getenv, key, def)
	return time.Duration(n) * time.Second, err
}

func positiveInt(getenv func(string) string, key string, def int) (int, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s %q is not a number above 0", key, v)
	}
	return n, nil
}

// newLogger returns a text or JSON logger whose records all carry the worker id, as the worker does.
func newLogger(format, workerID string, w io.Writer) *log.Logger {
	var h log.Handler = log.NewTextHandler(w, nil)
	if format == "json" {
		h = log.NewJSONHandler(w, nil)
	}
	return log.New(h).With("workerId", workerID)
}
