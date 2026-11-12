// Package scrape reads GET /status of the running workers, locally: the telemetry of a worker never has to be
// reachable from outside its host.
package scrape

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/redact"
	"github.com/anovimous/automata-worker-monitor/internal/workerapi"
)

const (
	// Timeout is the time a worker has to answer; a worker that takes longer counts as failed
	Timeout = 2 * time.Second
	// a status document is a few KiB; a bigger body is not one
	maxBody = 1 << 20
	// how many workers are scraped at the same time
	concurrency = 16
)

// Target is a worker to scrape.
type Target struct {
	ContainerID string
	URL         string
}

// Scraper keeps the last scrape of each worker.
type Scraper struct {
	client *http.Client
	now    func() time.Time

	mu    sync.Mutex
	state map[string]controllerapi.Telemetry // by container id
}

func New(client *http.Client, now func() time.Time) *Scraper {
	return &Scraper{client: client, now: now, state: map[string]controllerapi.Telemetry{}}
}

// ScrapeAll reads the status of every target, 16 at a time, and forgets the workers that are no longer targets (a
// container that stopped starts again with a clean slate).
func (s *Scraper) ScrapeAll(ctx context.Context, targets []Target) {
	keep := map[string]bool{}
	for _, t := range targets {
		keep[t.ContainerID] = true
	}
	s.mu.Lock()
	for id := range s.state {
		if !keep[id] {
			delete(s.state, id)
		}
	}
	s.mu.Unlock()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, t := range targets {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			s.scrape(ctx, t)
		})
	}
	wg.Wait()
}

// Get returns the last scrape of a worker; ok is false before the first one.
func (s *Scraper) Get(containerID string) (controllerapi.Telemetry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.state[containerID]
	return t, ok
}

func (s *Scraper) scrape(ctx context.Context, t Target) {
	start := s.now()
	status, code, err := s.fetch(ctx, t.URL)
	latency := s.now().Sub(start).Milliseconds()

	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.state[t.ContainerID]
	next := controllerapi.Telemetry{Configured: true, URL: t.URL}
	if err == nil {
		fetchedAt := s.now().UTC()
		next.OK, next.FetchedAt, next.LatencyMs, next.Status = true, &fetchedAt, &latency, status
	} else {
		// the last good document stays: it tells what the worker was doing before it stopped answering
		next.FetchedAt, next.LatencyMs, next.Status = prev.FetchedAt, prev.LatencyMs, prev.Status
		next.ConsecutiveFailures = prev.ConsecutiveFailures + 1
		next.LastError = redact.Line(err.Error())
		next.LastErrorCode = code
	}
	s.state[t.ContainerID] = next
}

// fetch reads and parses one status document; code classifies a failure.
func (s *Scraper) fetch(ctx context.Context, url string) (status *workerapi.Status, code string, err error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/status", nil)
	if err != nil {
		return nil, controllerapi.ScrapeInvalid, fmt.Errorf("bad telemetry URL: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, classify(err), err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, controllerapi.ScrapeInvalid, fmt.Errorf("GET /status answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, classify(err), fmt.Errorf("failed to read the status: %w", err)
	}
	if len(body) > maxBody {
		return nil, controllerapi.ScrapeInvalid, fmt.Errorf("the status is bigger than %d bytes", maxBody)
	}
	st, err := workerapi.Parse(body)
	if errors.Is(err, workerapi.ErrUnsupportedSchema) {
		return nil, controllerapi.ScrapeUnsupportedSchema, err
	}
	if err != nil {
		return nil, controllerapi.ScrapeInvalid, err
	}
	return &st, "", nil
}

func classify(err error) string {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return controllerapi.ScrapeTimeout
	}
	return controllerapi.ScrapeUnreachable
}
