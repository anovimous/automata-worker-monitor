package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func hostname(name string) func() (string, error) {
	return func() (string, error) { return name, nil }
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(env(nil), hostname("6f1c2b9a0d3e"))
	if err != nil {
		t.Fatalf("loadConfig returned an error: %v", err)
	}
	want := config{
		WorkerID:                 "6f1c2b9a0d3e",
		TelemetryAddr:            "127.0.0.1:9464",
		LogFormat:                "text",
		Scenario:                 scenarioNormal,
		JobDuration:              60 * time.Second,
		IdleDuration:             10 * time.Second,
		Routines:                 []string{"fuzz", "masser"},
		HeartbeatIntervalSeconds: 5,
		HeartbeatTimeoutSeconds:  60,
		WordlistCacheDir:         "/var/cache/automata/wordlists",
		WordlistCacheMaxBytes:    1610612736,
	}
	if !equalConfig(cfg, want) {
		t.Errorf("config = %+v\nwant     %+v", cfg, want)
	}
}

func equalConfig(a, b config) bool {
	return reflect.DeepEqual(a, b)
}

func TestLoadConfigReadsEveryVar(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"WORKER_ID":                         "w-host1-k3f9qa",
		"TELEMETRY_ADDR":                    "0.0.0.0:9464",
		"LOG_FORMAT":                        "json",
		"FAKE_SCENARIO":                     "cpu-hog",
		"FAKE_JOB_SECONDS":                  "5",
		"FAKE_IDLE_SECONDS":                 "2",
		"FAKE_ROUTINES":                     " fuzz , ,probe",
		"WORKER_HEARTBEAT_INTERVAL_SECONDS": "3",
		"WORKER_HEARTBEAT_TIMEOUT_SECONDS":  "30",
		"WORDLIST_CACHE_DIR":                "/cache",
		"WORDLIST_CACHE_MAX_BYTES":          "1000",
	}), hostname("unused"))
	if err != nil {
		t.Fatalf("loadConfig returned an error: %v", err)
	}
	want := config{
		WorkerID: "w-host1-k3f9qa", TelemetryAddr: "0.0.0.0:9464", LogFormat: "json", Scenario: scenarioCPUHog,
		JobDuration: 5 * time.Second, IdleDuration: 2 * time.Second, Routines: []string{"fuzz", "probe"},
		HeartbeatIntervalSeconds: 3, HeartbeatTimeoutSeconds: 30, WordlistCacheDir: "/cache", WordlistCacheMaxBytes: 1000,
	}
	if !equalConfig(cfg, want) {
		t.Errorf("config = %+v\nwant     %+v", cfg, want)
	}
}

func TestTelemetryOff(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"TELEMETRY_ADDR": "off"}), hostname("h"))
	if err != nil || cfg.TelemetryAddr != "" {
		t.Errorf("TelemetryAddr = %q, %v, want empty", cfg.TelemetryAddr, err)
	}
}

func TestLoadConfigRefusesWrongValues(t *testing.T) {
	cases := map[string]map[string]string{
		"worker id":        {"WORKER_ID": "-bad id"},
		"telemetry addr":   {"TELEMETRY_ADDR": "9464"},
		"telemetry port":   {"TELEMETRY_ADDR": "127.0.0.1:70000"},
		"telemetry name":   {"TELEMETRY_ADDR": "127.0.0.1:http"},
		"log format":       {"LOG_FORMAT": "xml"},
		"scenario":         {"FAKE_SCENARIO": "explode"},
		"job seconds":      {"FAKE_JOB_SECONDS": "0"},
		"idle seconds":     {"FAKE_IDLE_SECONDS": "ten"},
		"routines":         {"FAKE_ROUTINES": " , "},
		"heartbeat":        {"WORKER_HEARTBEAT_INTERVAL_SECONDS": "-1"},
		"heartbeat limit":  {"WORKER_HEARTBEAT_TIMEOUT_SECONDS": "x"},
		"cache size":       {"WORDLIST_CACHE_MAX_BYTES": "0"},
		"cache size words": {"WORDLIST_CACHE_MAX_BYTES": "1 GB"},
	}
	for name, vars := range cases {
		if _, err := loadConfig(env(vars), hostname("h")); err == nil {
			t.Errorf("%s: loadConfig(%v) returned no error", name, vars)
		}
	}
}

func TestLoadConfigNeedsAHostnameWithoutWorkerID(t *testing.T) {
	_, err := loadConfig(env(nil), func() (string, error) { return "", errors.New("no hostname") })
	if err == nil {
		t.Error("loadConfig returned no error without WORKER_ID and hostname")
	}
}

func TestEveryScenarioIsAccepted(t *testing.T) {
	for _, s := range scenarios {
		if _, err := loadConfig(env(map[string]string{"FAKE_SCENARIO": string(s)}), hostname("h")); err != nil {
			t.Errorf("scenario %s: %v", s, err)
		}
	}
}

func TestNewLoggerJSONCarriesTheWorkerID(t *testing.T) {
	var buf bytes.Buffer
	newLogger("json", "w-host1-k3f9qa", &buf).Info("job started", "jobId", 4211)
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("not JSON: %q", buf.String())
	}
	if rec["workerId"] != "w-host1-k3f9qa" || rec["msg"] != "job started" {
		t.Errorf("record = %v", rec)
	}
}

func TestNewLoggerTextCarriesTheWorkerID(t *testing.T) {
	var buf bytes.Buffer
	newLogger("text", "w-host1-k3f9qa", &buf).Info("job started")
	if line := buf.String(); !strings.Contains(line, "workerId=w-host1-k3f9qa") || strings.HasPrefix(line, "{") {
		t.Errorf("line = %q, want text with workerId", line)
	}
}
