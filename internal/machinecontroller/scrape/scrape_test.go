package scrape

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/contracttest"
	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
)

func worker(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL
}

func answer(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}
}

func scrapeOnce(t *testing.T, s *Scraper, url string) controllerapi.Telemetry {
	t.Helper()
	s.ScrapeAll(context.Background(), []Target{{ContainerID: "c1", URL: url}})
	tel, ok := s.Get("c1")
	if !ok {
		t.Fatal("no telemetry after a scrape")
	}
	return tel
}

func newScraper() *Scraper {
	return New(&http.Client{}, time.Now)
}

func TestAGoodStatus(t *testing.T) {
	url := worker(t, answer(string(contracttest.Read(t, "worker-status.v1.running.json"))))
	tel := scrapeOnce(t, newScraper(), url)

	if !tel.OK || !tel.Configured || tel.URL != url || tel.ConsecutiveFailures != 0 || tel.LastError != "" || tel.LastErrorCode != "" {
		t.Errorf("telemetry = %+v", tel)
	}
	if tel.Status == nil || tel.Status.Job == nil || tel.Status.Job.JobID != 4211 || tel.FetchedAt == nil || tel.LatencyMs == nil {
		t.Errorf("status %+v fetched %v latency %v", tel.Status, tel.FetchedAt, tel.LatencyMs)
	}
}

func TestFailuresAreClassified(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String()
	ln.Close()

	tests := []struct {
		name, url, code string
	}{
		{"connection refused", closed, controllerapi.ScrapeUnreachable},
		{"500", worker(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }), controllerapi.ScrapeInvalid},
		{"bad json", worker(t, answer(`{"schemaVersion": 1, "pid": "x"`)), controllerapi.ScrapeInvalid},
		{"unknown schema", worker(t, answer(`{"schemaVersion": 2}`)), controllerapi.ScrapeUnsupportedSchema},
		{"too big", worker(t, answer(`{"schemaVersion":1,"x":"`+strings.Repeat("a", maxBody)+`"}`)), controllerapi.ScrapeInvalid},
		{"bad url", "http://bad host", controllerapi.ScrapeInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tel := scrapeOnce(t, newScraper(), tt.url)
			if tel.OK || tel.LastErrorCode != tt.code || tel.LastError == "" || tel.ConsecutiveFailures != 1 {
				t.Errorf("telemetry = %+v, want a failure %s", tel, tt.code)
			}
		})
	}
}

func TestASlowWorkerTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the 2 s timeout")
	}
	release := make(chan struct{})
	url := worker(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)

	start := time.Now()
	tel := scrapeOnce(t, newScraper(), url)
	if tel.LastErrorCode != controllerapi.ScrapeTimeout || time.Since(start) > Timeout+time.Second {
		t.Errorf("telemetry = %+v after %s, want a timeout after 2 s", tel, time.Since(start))
	}
}

func TestTheLastGoodStatusStaysAfterAFailure(t *testing.T) {
	var fail atomic.Bool
	good := string(contracttest.Read(t, "worker-status.v1.idle.json"))
	url := worker(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(good))
	})
	s := newScraper()
	first := scrapeOnce(t, s, url)

	fail.Store(true)
	scrapeOnce(t, s, url)
	tel := scrapeOnce(t, s, url)
	if tel.OK || tel.ConsecutiveFailures != 2 || tel.Status == nil || tel.FetchedAt == nil || !tel.FetchedAt.Equal(*first.FetchedAt) {
		t.Errorf("telemetry = %+v, want 2 failures and the first status kept", tel)
	}

	fail.Store(false)
	if tel := scrapeOnce(t, s, url); !tel.OK || tel.ConsecutiveFailures != 0 || tel.LastErrorCode != "" {
		t.Errorf("after a success: %+v", tel)
	}
}

func TestAStoppedWorkerIsForgotten(t *testing.T) {
	url := worker(t, answer(string(contracttest.Read(t, "worker-status.v1.idle.json"))))
	s := newScraper()
	scrapeOnce(t, s, url)

	s.ScrapeAll(context.Background(), nil)
	if _, ok := s.Get("c1"); ok {
		t.Error("the telemetry of a container no longer scraped is kept")
	}
}

func TestTheErrorIsRedacted(t *testing.T) {
	tel := scrapeOnce(t, newScraper(), "http://user:secret@127.0.0.1:1")
	if strings.Contains(tel.LastError, "secret") {
		t.Errorf("lastError = %q holds the password", tel.LastError)
	}
}

func TestManyWorkersAreScrapedAtOnce(t *testing.T) {
	var inFlight, peak atomic.Int32
	url := worker(t, func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		inFlight.Add(-1)
		w.Write([]byte(`{"schemaVersion":1}`))
	})
	var targets []Target
	for i := range 40 {
		targets = append(targets, Target{ContainerID: string(rune('a' + i)), URL: url})
	}

	newScraper().ScrapeAll(context.Background(), targets)
	if p := peak.Load(); p < 2 || p > concurrency {
		t.Errorf("peak concurrency = %d, want parallel scrapes but at most %d", p, concurrency)
	}
}
