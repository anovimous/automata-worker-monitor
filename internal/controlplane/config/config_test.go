package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const token = "0123456789abcdef0123456789abcdef"

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

const minimal = `
hosts:
  - name: host1
    workerMachineControllerUrl: http://127.0.0.1:9480
    controlToken: ${HOST1_TOKEN}
`

func TestParseMinimalUsesTheDefaults(t *testing.T) {
	cfg, err := Parse([]byte(minimal), env(map[string]string{"HOST1_TOKEN": token}))
	if err != nil {
		t.Fatalf("Parse returned an error: %v", err)
	}
	if cfg.Listen != "127.0.0.1:9490" || cfg.Database != "/var/lib/automata-monitor/monitor.db" || !cfg.Seed() {
		t.Errorf("config = %+v", cfg)
	}
	if h := cfg.Hosts[0]; h.ControlToken != token || h.MaxWorkers != 20 {
		t.Errorf("host = %+v, want the token from the env and 20 workers", h)
	}
	if cfg.PollInterval() != 5*time.Second || cfg.ControllerTimeout() != 4*time.Second {
		t.Errorf("polling = %+v", cfg.Polling)
	}
	if cfg.Health.UnresponsiveAfterFailures != 3 || cfg.Health.DegradedMemoryRatio != 0.9 || cfg.Health.ProvisionTimeoutSeconds != 120 {
		t.Errorf("health = %+v", cfg.Health)
	}
	if cfg.History.RetentionDays != 14 || cfg.Retention.EventsDays != 30 || cfg.Retention.RemovedWorkersDays != 30 {
		t.Errorf("retention = %+v %+v", cfg.History, cfg.Retention)
	}
	if !cfg.ListenIsLoopback() {
		t.Error("the default listen address is not loopback")
	}
}

func TestParseAFullFile(t *testing.T) {
	doc := `
listen: "0.0.0.0:9490"
tls: {cert: /tls/c.pem, key: /tls/k.pem}
database: /data/monitor.db
hosts:
  - name: host1
    workerMachineControllerUrl: https://10.0.0.5:9480
    controlToken: "` + token + `"
    caFile: /tls/ca.pem
    maxWorkers: 8
  - name: host2
    workerMachineControllerUrl: http://127.0.0.1:9481
    controlToken: "` + token + `"
history: {retentionDays: 7}
polling: {intervalSeconds: 10, workerMachineControllerTimeoutSeconds: 8}
health: {startingGraceSeconds: 90, degradedMemoryRatio: 0.8}
retention: {eventsDays: 60, removedWorkersDays: 10}
seedTemplates: false
`
	cfg, err := Parse([]byte(doc), env(nil))
	if err != nil {
		t.Fatalf("Parse returned an error: %v", err)
	}
	if cfg.ListenIsLoopback() || cfg.TLS.Cert != "/tls/c.pem" || cfg.Database != "/data/monitor.db" || cfg.Seed() {
		t.Errorf("config = %+v", cfg)
	}
	if h, ok := cfg.Host("host1"); !ok || h.MaxWorkers != 8 || h.CAFile != "/tls/ca.pem" {
		t.Errorf("host1 = %+v", h)
	}
	if _, ok := cfg.Host("host3"); ok {
		t.Error("Host found a host that is not configured")
	}
	if cfg.Health.StartingGraceSeconds != 90 || cfg.Health.DegradedMemoryRatio != 0.8 || cfg.Health.UnresponsiveAfterFailures != 3 {
		t.Errorf("health = %+v, want the two values set and the defaults for the others", cfg.Health)
	}
	if cfg.History.RetentionDays != 7 || cfg.Retention.EventsDays != 60 || cfg.PollInterval() != 10*time.Second {
		t.Errorf("config = %+v", cfg)
	}
}

func TestParseRefusesAMissingEnvVar(t *testing.T) {
	_, err := Parse([]byte(minimal), env(nil))
	if err == nil || !strings.Contains(err.Error(), "HOST1_TOKEN") {
		t.Errorf("err = %v, want one naming HOST1_TOKEN", err)
	}
}

func TestParseRefusesWrongValues(t *testing.T) {
	host := func(extra string) string {
		return "hosts:\n  - name: host1\n    workerMachineControllerUrl: http://127.0.0.1:9480\n    controlToken: \"" + token + "\"\n" + extra
	}
	tests := map[string]string{
		"unknown key":            host("sessionSecret: x\n"),
		"not yaml":               "hosts: [",
		"no host":                "listen: 127.0.0.1:9490\n",
		"bad listen":             "listen: 9490\n" + host(""),
		"cert without key":       "tls: {cert: /c.pem}\n" + host(""),
		"bad host name":          "hosts:\n  - name: Host_1\n    workerMachineControllerUrl: http://h:1\n    controlToken: \"" + token + "\"\n",
		"twice the same host":    host("") + "  - name: host1\n    workerMachineControllerUrl: http://h:1\n    controlToken: \"" + token + "\"\n",
		"bad url":                "hosts:\n  - name: host1\n    workerMachineControllerUrl: ftp://h\n    controlToken: \"" + token + "\"\n",
		"user info in url":       "hosts:\n  - name: host1\n    workerMachineControllerUrl: http://u:p@h:1\n    controlToken: \"" + token + "\"\n",
		"short token":            "hosts:\n  - name: host1\n    workerMachineControllerUrl: http://h:1\n    controlToken: short\n",
		"ca file with http":      "hosts:\n  - name: host1\n    workerMachineControllerUrl: http://h:1\n    controlToken: \"" + token + "\"\n    caFile: /ca.pem\n",
		"negative max workers":   "hosts:\n  - name: host1\n    workerMachineControllerUrl: http://h:1\n    controlToken: \"" + token + "\"\n    maxWorkers: -1\n",
		"timeout above interval": host("polling: {intervalSeconds: 5, workerMachineControllerTimeoutSeconds: 5}\n"),
		"zero threshold":         host("health: {slowStatusMs: 0}\n"),
		"memory ratio above 1":   host("health: {degradedMemoryRatio: 1.5}\n"),
		"zero retention":         host("history: {retentionDays: 0}\n"),
		"no database":            host("database: \"\"\n"),
	}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc), env(nil)); err == nil {
				t.Errorf("Parse(%q) returned no error", doc)
			}
		})
	}
}

func TestLoadReadsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	os.WriteFile(path, []byte(minimal), 0o600)
	if _, err := Load(path, env(map[string]string{"HOST1_TOKEN": token})); err != nil {
		t.Errorf("Load: %v", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yml"), env(nil)); err == nil {
		t.Error("Load of a missing file returned no error")
	}
}

func TestListenIsLoopback(t *testing.T) {
	for listen, want := range map[string]bool{"127.0.0.1:9490": true, "localhost:9490": true, "[::1]:9490": true,
		"0.0.0.0:9490": false, "10.0.0.5:9490": false, ":9490": false} {
		if got := (Config{Listen: listen}).ListenIsLoopback(); got != want {
			t.Errorf("ListenIsLoopback(%s) = %v, want %v", listen, got, want)
		}
	}
}
