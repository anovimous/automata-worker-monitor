// Package config reads the configuration file of the workers control plane (plan/07-workers-control-plane.md §1). A
// value written ${NAME} is taken from the env var NAME, for the secrets. A wrong value stops the start.
package config

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// the token of a worker machine controller must be long enough not to be guessed
const minTokenLength = 32

var (
	hostNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	envRefPattern   = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

type Config struct {
	Listen        string    `yaml:"listen"`
	TLS           TLS       `yaml:"tls"`
	Database      string    `yaml:"database"`
	Hosts         []Host    `yaml:"hosts"`
	History       History   `yaml:"history"`
	Polling       Polling   `yaml:"polling"`
	Health        Health    `yaml:"health"`
	Retention     Retention `yaml:"retention"`
	SeedTemplates *bool     `yaml:"seedTemplates"`
}

type TLS struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// Host is a Docker host and its worker machine controller.
type Host struct {
	Name                       string `yaml:"name"`
	WorkerMachineControllerURL string `yaml:"workerMachineControllerUrl"`
	ControlToken               string `yaml:"controlToken"`
	CAFile                     string `yaml:"caFile"` // PEM of the CA of the controller's certificate, for https
	MaxWorkers                 int    `yaml:"maxWorkers"`
}

type History struct {
	RetentionDays int `yaml:"retentionDays"`
}

type Polling struct {
	IntervalSeconds                       int `yaml:"intervalSeconds"`
	WorkerMachineControllerTimeoutSeconds int `yaml:"workerMachineControllerTimeoutSeconds"`
}

// Health holds the thresholds of the health engine (plan/07 §4).
type Health struct {
	WorkerMachineControllerUnknownAfterSeconds int     `yaml:"workerMachineControllerUnknownAfterSeconds"`
	StartingGraceSeconds                       int     `yaml:"startingGraceSeconds"`
	UnresponsiveAfterFailures                  int     `yaml:"unresponsiveAfterFailures"`
	DegradedMemoryRatio                        float64 `yaml:"degradedMemoryRatio"`
	DegradedHeartbeatFactor                    int     `yaml:"degradedHeartbeatFactor"`
	CoreErrorRecentSeconds                     int     `yaml:"coreErrorRecentSeconds"`
	RestartWindowSeconds                       int     `yaml:"restartWindowSeconds"`
	SlowStatusMs                               int     `yaml:"slowStatusMs"`
	ProvisionTimeoutSeconds                    int     `yaml:"provisionTimeoutSeconds"`
}

type Retention struct {
	EventsDays         int `yaml:"eventsDays"`
	RemovedWorkersDays int `yaml:"removedWorkersDays"`
}

// Defaults are the values of a key left out of the file.
func Defaults() Config {
	seed := true
	return Config{
		Listen:   "127.0.0.1:9490",
		Database: "/var/lib/automata-monitor/monitor.db",
		History:  History{RetentionDays: 14},
		Polling:  Polling{IntervalSeconds: 5, WorkerMachineControllerTimeoutSeconds: 4},
		Health: Health{
			WorkerMachineControllerUnknownAfterSeconds: 15,
			StartingGraceSeconds:                       60,
			UnresponsiveAfterFailures:                  3,
			DegradedMemoryRatio:                        0.9,
			DegradedHeartbeatFactor:                    3,
			CoreErrorRecentSeconds:                     300,
			RestartWindowSeconds:                       900,
			SlowStatusMs:                               1000,
			ProvisionTimeoutSeconds:                    120,
		},
		Retention:     Retention{EventsDays: 30, RemovedWorkersDays: 30},
		SeedTemplates: &seed,
	}
}

// Load reads the file at path; getenv resolves the ${NAME} references.
func Load(path string, getenv func(string) string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("failed to read the configuration %s: %w", path, err)
	}
	return Parse(raw, getenv)
}

// Parse reads a configuration document.
func Parse(raw []byte, getenv func(string) string) (Config, error) {
	var missing []string
	expanded := envRefPattern.ReplaceAllFunc(raw, func(ref []byte) []byte {
		name := string(envRefPattern.FindSubmatch(ref)[1])
		v := getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return []byte(v)
	})
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("the configuration refers to env vars that are not set: %s", strings.Join(missing, ", "))
	}

	cfg := Defaults()
	dec := yaml.NewDecoder(bytes.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("the configuration is not valid YAML of the expected shape: %w", err)
	}
	for i := range cfg.Hosts {
		if cfg.Hosts[i].MaxWorkers == 0 {
			cfg.Hosts[i].MaxWorkers = 20
		}
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen must be host:port, got %q: %w", c.Listen, err)
	}
	if (c.TLS.Cert == "") != (c.TLS.Key == "") {
		return fmt.Errorf("tls.cert and tls.key go together")
	}
	if c.Database == "" {
		return fmt.Errorf("database is required")
	}
	if len(c.Hosts) == 0 {
		return fmt.Errorf("hosts names no host")
	}
	seen := map[string]bool{}
	for _, h := range c.Hosts {
		if !hostNamePattern.MatchString(h.Name) {
			return fmt.Errorf("hosts[].name must match %s, got %q", hostNamePattern, h.Name)
		}
		if seen[h.Name] {
			return fmt.Errorf("the host %s is named twice", h.Name)
		}
		seen[h.Name] = true
		u, err := url.Parse(h.WorkerMachineControllerURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return fmt.Errorf("host %s: workerMachineControllerUrl must be an http or https URL without user info", h.Name)
		}
		if len(h.ControlToken) < minTokenLength {
			return fmt.Errorf("host %s: controlToken must have at least %d characters", h.Name, minTokenLength)
		}
		if h.CAFile != "" && u.Scheme != "https" {
			return fmt.Errorf("host %s: caFile goes with an https URL", h.Name)
		}
		if h.MaxWorkers < 1 {
			return fmt.Errorf("host %s: maxWorkers must be at least 1", h.Name)
		}
	}
	if c.Polling.IntervalSeconds < 1 || c.Polling.WorkerMachineControllerTimeoutSeconds < 1 ||
		c.Polling.WorkerMachineControllerTimeoutSeconds >= c.Polling.IntervalSeconds {
		return fmt.Errorf("polling: intervalSeconds and workerMachineControllerTimeoutSeconds must be above 0, the timeout below the interval")
	}
	h := c.Health
	if h.WorkerMachineControllerUnknownAfterSeconds < 1 || h.StartingGraceSeconds < 1 || h.UnresponsiveAfterFailures < 1 ||
		h.DegradedHeartbeatFactor < 1 || h.CoreErrorRecentSeconds < 1 || h.RestartWindowSeconds < 1 || h.SlowStatusMs < 1 ||
		h.ProvisionTimeoutSeconds < 1 {
		return fmt.Errorf("health: every threshold must be above 0")
	}
	if h.DegradedMemoryRatio <= 0 || h.DegradedMemoryRatio > 1 {
		return fmt.Errorf("health.degradedMemoryRatio must be above 0 and at most 1")
	}
	if c.History.RetentionDays < 1 || c.Retention.EventsDays < 1 || c.Retention.RemovedWorkersDays < 1 {
		return fmt.Errorf("history.retentionDays, retention.eventsDays and retention.removedWorkersDays must be at least 1")
	}
	return nil
}

// ListenIsLoopback tells whether only this machine can reach the control plane. Without a login anyone who reaches
// it can run every action, so another address is allowed but warned about at every start.
func (c Config) ListenIsLoopback() bool {
	host, _, _ := net.SplitHostPort(c.Listen)
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Host returns the host of that name.
func (c Config) Host(name string) (Host, bool) {
	for _, h := range c.Hosts {
		if h.Name == name {
			return h, true
		}
	}
	return Host{}, false
}

// PollInterval and ControllerTimeout are the polling settings as durations.
func (c Config) PollInterval() time.Duration {
	return time.Duration(c.Polling.IntervalSeconds) * time.Second
}

func (c Config) ControllerTimeout() time.Duration {
	return time.Duration(c.Polling.WorkerMachineControllerTimeoutSeconds) * time.Second
}

// Seed tells whether the default template is inserted on the first start.
func (c Config) Seed() bool {
	return c.SeedTemplates == nil || *c.SeedTemplates
}
