// Package config reads the configuration of the worker machine controller from its env vars
// (plan/06-worker-machine-controller.md §1). A wrong value is an error, never a silent default.
package config

import (
	"fmt"
	"net"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// the token of the workers control plane must be long enough not to be guessed
const minTokenLength = 32

// the host name is part of the worker ids (w-<host>-<6 chars>)
var hostNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

type Config struct {
	ListenAddr     string
	HostName       string
	Token          string
	TLSCert        string
	TLSKey         string
	AllowPlaintext bool
	DockerHost     string // "" is the Docker default
	EnvDir         string
	PortRange      PortRange
	DiskPaths      []string
	AllowedImages  []string // glob patterns, path.Match syntax
	ScrapeInterval time.Duration
	// DiscoveryInterval is the full container list interval (Docker events fill the gaps)
	DiscoveryInterval time.Duration
	LogFormat         string
}

// PortRange is the telemetry ports of host-network workers, both ends included.
type PortRange struct {
	Min, Max int
}

// TLS tells whether the API is served over HTTPS.
func (c Config) TLS() bool {
	return c.TLSCert != ""
}

// Load reads the env vars; hostname gives the default host name.
func Load(getenv func(string) string, hostname func() (string, error)) (Config, error) {
	cfg := Config{
		ListenAddr: or(getenv("WORKER_MACHINE_CONTROLLER_LISTEN_ADDR"), "127.0.0.1:9480"),
		Token:      getenv("WORKER_MACHINE_CONTROLLER_TOKEN"),
		TLSCert:    getenv("WORKER_MACHINE_CONTROLLER_TLS_CERT"),
		TLSKey:     getenv("WORKER_MACHINE_CONTROLLER_TLS_KEY"),
		DockerHost: getenv("WORKER_MACHINE_CONTROLLER_DOCKER_HOST"),
		EnvDir:     or(getenv("WORKER_MACHINE_CONTROLLER_ENV_DIR"), "/etc/automata-worker-machine-controller/env"),
		LogFormat:  or(getenv("LOG_FORMAT"), "json"),
	}

	cfg.HostName = getenv("WORKER_MACHINE_CONTROLLER_HOST_NAME")
	if cfg.HostName == "" {
		h, err := hostname()
		if err != nil {
			return Config{}, fmt.Errorf("WORKER_MACHINE_CONTROLLER_HOST_NAME is not set and the hostname is unknown: %w", err)
		}
		cfg.HostName = strings.ToLower(h)
	}
	if !hostNamePattern.MatchString(cfg.HostName) {
		return Config{}, fmt.Errorf("WORKER_MACHINE_CONTROLLER_HOST_NAME must match %s, got %q", hostNamePattern, cfg.HostName)
	}

	if len(cfg.Token) < minTokenLength {
		return Config{}, fmt.Errorf("WORKER_MACHINE_CONTROLLER_TOKEN must have at least %d characters", minTokenLength)
	}

	host, _, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return Config{}, fmt.Errorf("WORKER_MACHINE_CONTROLLER_LISTEN_ADDR must be host:port, got %q: %w", cfg.ListenAddr, err)
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return Config{}, fmt.Errorf("WORKER_MACHINE_CONTROLLER_TLS_CERT and WORKER_MACHINE_CONTROLLER_TLS_KEY go together")
	}
	if cfg.AllowPlaintext, err = boolOf(getenv, "WORKER_MACHINE_CONTROLLER_ALLOW_PLAINTEXT"); err != nil {
		return Config{}, err
	}
	// the token would cross the network in clear
	if !cfg.TLS() && !isLoopback(host) && !cfg.AllowPlaintext {
		return Config{}, fmt.Errorf("WORKER_MACHINE_CONTROLLER_LISTEN_ADDR %q is not a loopback address: set WORKER_MACHINE_CONTROLLER_TLS_CERT and _KEY, or WORKER_MACHINE_CONTROLLER_ALLOW_PLAINTEXT=true", cfg.ListenAddr)
	}

	if !filepath.IsAbs(cfg.EnvDir) {
		return Config{}, fmt.Errorf("WORKER_MACHINE_CONTROLLER_ENV_DIR must be an absolute path, got %q", cfg.EnvDir)
	}

	if cfg.PortRange, err = parsePortRange(or(getenv("WORKER_MACHINE_CONTROLLER_HOST_PORT_RANGE"), "19100-19199")); err != nil {
		return Config{}, err
	}

	cfg.DiskPaths = list(or(getenv("WORKER_MACHINE_CONTROLLER_DISK_PATHS"), "/host/var/lib/docker"))
	for _, p := range cfg.DiskPaths {
		if !filepath.IsAbs(p) {
			return Config{}, fmt.Errorf("WORKER_MACHINE_CONTROLLER_DISK_PATHS must hold absolute paths, got %q", p)
		}
	}

	cfg.AllowedImages = list(or(getenv("WORKER_MACHINE_CONTROLLER_ALLOWED_IMAGES"), "automata-worker:*"))
	for _, pattern := range cfg.AllowedImages {
		if _, err := path.Match(pattern, ""); err != nil {
			return Config{}, fmt.Errorf("WORKER_MACHINE_CONTROLLER_ALLOWED_IMAGES has a bad pattern %q: %w", pattern, err)
		}
	}
	if len(cfg.AllowedImages) == 0 {
		return Config{}, fmt.Errorf("WORKER_MACHINE_CONTROLLER_ALLOWED_IMAGES names no image")
	}

	if cfg.ScrapeInterval, err = seconds(getenv, "WORKER_MACHINE_CONTROLLER_SCRAPE_INTERVAL_SECONDS", 5); err != nil {
		return Config{}, err
	}
	if cfg.DiscoveryInterval, err = seconds(getenv, "WORKER_MACHINE_CONTROLLER_DISCOVERY_INTERVAL_SECONDS", 10); err != nil {
		return Config{}, err
	}

	if cfg.LogFormat != "json" && cfg.LogFormat != "text" {
		return Config{}, fmt.Errorf("LOG_FORMAT must be text or json, got %q", cfg.LogFormat)
	}
	return cfg, nil
}

// ImageAllowed tells whether a worker may be created from image.
func (c Config) ImageAllowed(image string) bool {
	for _, pattern := range c.AllowedImages {
		if ok, _ := path.Match(pattern, image); ok {
			return true
		}
	}
	return false
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parsePortRange(v string) (PortRange, error) {
	lo, hi, ok := strings.Cut(v, "-")
	min, err1 := strconv.Atoi(strings.TrimSpace(lo))
	max, err2 := strconv.Atoi(strings.TrimSpace(hi))
	if !ok || err1 != nil || err2 != nil || min < 1024 || max > 65535 || min > max {
		return PortRange{}, fmt.Errorf("WORKER_MACHINE_CONTROLLER_HOST_PORT_RANGE must be min-max within 1024-65535, got %q", v)
	}
	return PortRange{Min: min, Max: max}, nil
}

func list(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func boolOf(getenv func(string) string, key string) (bool, error) {
	switch v := getenv(key); v {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be true or false, got %q", key, v)
	}
}

func seconds(getenv func(string) string, key string, def int) (time.Duration, error) {
	v := getenv(key)
	if v == "" {
		return time.Duration(def) * time.Second, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a number of seconds above 0, got %q", key, v)
	}
	return time.Duration(n) * time.Second, nil
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
