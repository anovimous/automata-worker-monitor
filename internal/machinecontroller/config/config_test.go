package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const token = "0123456789abcdef0123456789abcdef"

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func withToken(vars map[string]string) map[string]string {
	out := map[string]string{"WORKER_MACHINE_CONTROLLER_TOKEN": token}
	for k, v := range vars {
		out[k] = v
	}
	return out
}

func hostname(name string) func() (string, error) {
	return func() (string, error) { return name, nil }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(withToken(nil)), hostname("Host1"))
	if err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	want := Config{
		ListenAddr:        "127.0.0.1:9480",
		HostName:          "host1",
		Token:             token,
		EnvDir:            "/etc/automata-worker-machine-controller/env",
		PortRange:         PortRange{Min: 19100, Max: 19199},
		DiskPaths:         []string{"/host/var/lib/docker"},
		AllowedImages:     []string{"automata-worker:*"},
		ScrapeInterval:    5 * time.Second,
		DiscoveryInterval: 10 * time.Second,
		LogFormat:         "json",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("config = %+v\nwant     %+v", cfg, want)
	}
	if cfg.TLS() {
		t.Error("TLS() is true without a certificate")
	}
}

func TestLoadReadsEveryVar(t *testing.T) {
	cfg, err := Load(env(withToken(map[string]string{
		"WORKER_MACHINE_CONTROLLER_LISTEN_ADDR":                "10.0.0.5:9480",
		"WORKER_MACHINE_CONTROLLER_HOST_NAME":                  "gpu-2",
		"WORKER_MACHINE_CONTROLLER_TLS_CERT":                   "/tls/cert.pem",
		"WORKER_MACHINE_CONTROLLER_TLS_KEY":                    "/tls/key.pem",
		"WORKER_MACHINE_CONTROLLER_DOCKER_HOST":                "unix:///run/docker.sock",
		"WORKER_MACHINE_CONTROLLER_ENV_DIR":                    "/etc/env",
		"WORKER_MACHINE_CONTROLLER_HOST_PORT_RANGE":            "20000-20009",
		"WORKER_MACHINE_CONTROLLER_DISK_PATHS":                 "/host/var/lib/docker, /host/data",
		"WORKER_MACHINE_CONTROLLER_ALLOWED_IMAGES":             "automata-worker:*,registry.local/automata-worker:*",
		"WORKER_MACHINE_CONTROLLER_SCRAPE_INTERVAL_SECONDS":    "3",
		"WORKER_MACHINE_CONTROLLER_DISCOVERY_INTERVAL_SECONDS": "20",
		"LOG_FORMAT": "text",
	})), hostname("unused"))
	if err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if cfg.ListenAddr != "10.0.0.5:9480" || cfg.HostName != "gpu-2" || !cfg.TLS() || cfg.DockerHost != "unix:///run/docker.sock" ||
		cfg.EnvDir != "/etc/env" || cfg.PortRange != (PortRange{20000, 20009}) || len(cfg.DiskPaths) != 2 ||
		len(cfg.AllowedImages) != 2 || cfg.ScrapeInterval != 3*time.Second || cfg.DiscoveryInterval != 20*time.Second || cfg.LogFormat != "text" {
		t.Errorf("config = %+v", cfg)
	}
}

func TestLoadRefusesWrongValues(t *testing.T) {
	tests := map[string]map[string]string{
		"short token":            {"WORKER_MACHINE_CONTROLLER_TOKEN": "short"},
		"bad host name":          {"WORKER_MACHINE_CONTROLLER_HOST_NAME": "Host_1"},
		"listen not host:port":   {"WORKER_MACHINE_CONTROLLER_LISTEN_ADDR": "9480"},
		"cert without key":       {"WORKER_MACHINE_CONTROLLER_TLS_CERT": "/c.pem"},
		"plaintext remote":       {"WORKER_MACHINE_CONTROLLER_LISTEN_ADDR": "10.0.0.5:9480"},
		"plaintext flag garbage": {"WORKER_MACHINE_CONTROLLER_ALLOW_PLAINTEXT": "yes"},
		"relative env dir":       {"WORKER_MACHINE_CONTROLLER_ENV_DIR": "env"},
		"port range reversed":    {"WORKER_MACHINE_CONTROLLER_HOST_PORT_RANGE": "19199-19100"},
		"port range too low":     {"WORKER_MACHINE_CONTROLLER_HOST_PORT_RANGE": "80-90"},
		"port range garbage":     {"WORKER_MACHINE_CONTROLLER_HOST_PORT_RANGE": "19100"},
		"relative disk path":     {"WORKER_MACHINE_CONTROLLER_DISK_PATHS": "var/lib/docker"},
		"bad image pattern":      {"WORKER_MACHINE_CONTROLLER_ALLOWED_IMAGES": "automata-worker:["},
		"no image":               {"WORKER_MACHINE_CONTROLLER_ALLOWED_IMAGES": " , "},
		"zero scrape interval":   {"WORKER_MACHINE_CONTROLLER_SCRAPE_INTERVAL_SECONDS": "0"},
		"bad discovery interval": {"WORKER_MACHINE_CONTROLLER_DISCOVERY_INTERVAL_SECONDS": "ten"},
		"bad log format":         {"LOG_FORMAT": "xml"},
	}
	for name, vars := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(env(withToken(vars)), hostname("host1")); err == nil {
				t.Errorf("Load(%v) returned no error", vars)
			}
		})
	}
}

func TestLoadNeedsAToken(t *testing.T) {
	_, err := Load(env(nil), hostname("host1"))
	if err == nil || !strings.Contains(err.Error(), "WORKER_MACHINE_CONTROLLER_TOKEN") {
		t.Errorf("err = %v, want one naming the token", err)
	}
}

func TestLoadNeedsAHostName(t *testing.T) {
	if _, err := Load(env(withToken(nil)), func() (string, error) { return "", errors.New("no hostname") }); err == nil {
		t.Error("Load returned no error without a host name")
	}
}

func TestPlaintextIsAllowedOnLoopbackOrWhenAsked(t *testing.T) {
	for _, vars := range []map[string]string{
		{"WORKER_MACHINE_CONTROLLER_LISTEN_ADDR": "localhost:9480"},
		{"WORKER_MACHINE_CONTROLLER_LISTEN_ADDR": "[::1]:9480"},
		{"WORKER_MACHINE_CONTROLLER_LISTEN_ADDR": "10.0.0.5:9480", "WORKER_MACHINE_CONTROLLER_ALLOW_PLAINTEXT": "true"},
	} {
		if _, err := Load(env(withToken(vars)), hostname("host1")); err != nil {
			t.Errorf("Load(%v): %v", vars, err)
		}
	}
}

func TestImageAllowed(t *testing.T) {
	cfg := Config{AllowedImages: []string{"automata-worker:*", "registry.local/automata-worker:0.*"}}
	tests := map[string]bool{
		"automata-worker:0.4.0":                 true,
		"automata-worker:dev":                   true,
		"registry.local/automata-worker:0.4.0":  true,
		"registry.local/automata-worker:1.0":    false,
		"automata-fakeworker:dev":               false,
		"ubuntu:24.04":                          false,
		"automata-worker":                       false,
		"evil/automata-worker:0.4.0":            false,
		"automata-worker:0.4.0@sha256:abcdef01": true,
	}
	for image, want := range tests {
		if got := cfg.ImageAllowed(image); got != want {
			t.Errorf("ImageAllowed(%q) = %v, want %v", image, got, want)
		}
	}
}
