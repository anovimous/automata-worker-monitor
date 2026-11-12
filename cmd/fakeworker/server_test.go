package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/contracttest"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

// stubSource answers fixed values.
type stubSource struct {
	status workerapi.Status
	ok     bool
	age    time.Duration
	phase  workerapi.Phase
}

func (s stubSource) snapshot() workerapi.Status { return s.status }

func (s stubSource) health() (bool, time.Duration, workerapi.Phase) { return s.ok, s.age, s.phase }

func serve(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestStatusServesTheSnapshot(t *testing.T) {
	src := stubSource{status: workerapi.Status{SchemaVersion: 1, WorkerID: "w-host1-k3f9qa", Phase: workerapi.PhaseIdle,
		Counters: workerapi.Counters{JobsByOutcome: map[string]int64{}}}}
	rec := serve(t, handler(src, 0), http.MethodGet, "/status")

	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET /status = %d %s, want 200 application/json", rec.Code, rec.Header().Get("Content-Type"))
	}
	want, err := json.Marshal(src.status)
	if err != nil {
		t.Fatal(err)
	}
	if same, err := contracttest.SameJSON(rec.Body.Bytes(), want); err != nil || !same {
		t.Errorf("body = %s, want %s", rec.Body, want)
	}
}

func TestHealthzIsOkWithinTheBudget(t *testing.T) {
	rec := serve(t, handler(stubSource{ok: true}, 0), http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("GET /healthz = %d %q, want 200 ok", rec.Code, rec.Body)
	}
}

func TestHealthzIsStuckPastTheBudget(t *testing.T) {
	src := stubSource{ok: false, age: 1602 * time.Second, phase: workerapi.PhasePreparing}
	rec := serve(t, handler(src, 0), http.MethodGet, "/healthz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /healthz = %d, want 503", rec.Code)
	}
	want := `{"reason":"STUCK","phase":"PREPARING","progressAgeSeconds":1602}`
	if same, err := contracttest.SameJSON(rec.Body.Bytes(), []byte(want)); err != nil || !same {
		t.Errorf("body = %s, want %s", rec.Body, want)
	}
}

func TestEveryResponseIsNotCached(t *testing.T) {
	h := handler(stubSource{ok: true}, 0)
	cases := []struct {
		method, path string
		code         int
	}{
		{http.MethodGet, "/status", 200},
		{http.MethodHead, "/status", 200},
		{http.MethodHead, "/healthz", 200},
		{http.MethodPost, "/status", 405},
		{http.MethodDelete, "/healthz", 405},
		{http.MethodGet, "/metrics", 404},
		{http.MethodGet, "/", 404},
	}
	for _, c := range cases {
		rec := serve(t, h, c.method, c.path)
		if rec.Code != c.code {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s %s Cache-Control = %q, want no-store", c.method, c.path, cc)
		}
	}
}

func TestSlowStatusWaitsBeforeAnswering(t *testing.T) {
	start := time.Now()
	rec := serve(t, handler(stubSource{}, 50*time.Millisecond), http.MethodGet, "/status")
	if took := time.Since(start); rec.Code != 200 || took < 50*time.Millisecond {
		t.Errorf("GET /status = %d after %s, want 200 after ≥ 50ms", rec.Code, took)
	}
}

func TestSlowStatusStopsWhenTheClientGoesAway(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	start := time.Now()
	handler(stubSource{}, time.Minute).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil).WithContext(ctx))
	if took := time.Since(start); took > time.Second || rec.Body.Len() != 0 {
		t.Errorf("handler took %s and wrote %q, want it to stop at once without a body", took, rec.Body)
	}
}

func TestServerStartsAndShutsDown(t *testing.T) {
	srv := newServer("127.0.0.1:0", stubSource{ok: true}, 0)
	// bind on a free port picked by the test, since Start does not report the address
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.addr = ln.Addr().String()
	ln.Close()

	if err := srv.Start(); err != nil {
		t.Fatalf("Start returned an error: %v", err)
	}
	resp, err := http.Get("http://" + srv.addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Errorf("GET /healthz = %d %q, want 200 ok", resp.StatusCode, body)
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned an error: %v", err)
	}
	if _, err := http.Get("http://" + srv.addr + "/healthz"); err == nil {
		t.Error("the server still answers after Shutdown")
	}
}

func TestServerStartFailsOnABusyAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := newServer(ln.Addr().String(), stubSource{}, 0).Start(); err == nil {
		t.Error("Start returned no error for an address in use")
	}
}

func healthzServer(t *testing.T, code int) (port string) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("healthcheck asked for %s", r.URL.Path)
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(ts.Close)
	_, port, _ = net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	return port
}

func TestHealthcheck(t *testing.T) {
	client := &http.Client{Timeout: time.Second}
	okPort := healthzServer(t, http.StatusOK)
	stuckPort := healthzServer(t, http.StatusServiceUnavailable)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := ln.Addr().String()
	ln.Close()

	cases := []struct {
		name, addr string
		want       int
	}{
		{"200", "127.0.0.1:" + okPort, 0},
		{"503", "127.0.0.1:" + stuckPort, 1},
		{"connection refused", closedAddr, 1},
		{"off", "off", 0},
		{"wildcard host", "0.0.0.0:" + okPort, 0},
		{"empty host", ":" + okPort, 0},
		{"invalid address", "nope", 1},
	}
	for _, c := range cases {
		if got := healthcheck(c.addr, client); got != c.want {
			t.Errorf("%s: healthcheck(%q) = %d, want %d", c.name, c.addr, got, c.want)
		}
	}
}
