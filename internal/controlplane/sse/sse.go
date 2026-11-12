// Package sse pushes the changes of the fleet to the browsers as Server-Sent Events.
package sse

import (
	"encoding/json"
	"fmt"
	log "log/slog"
	"net/http"
	"sync"
	"time"
)

// buffer is how many events a client may fall behind; a client further behind is dropped, and its EventSource
// reconnects and gets a fresh snapshot
const buffer = 64

// Heartbeat is how often a comment is sent to keep the connection open through proxies.
var Heartbeat = 15 * time.Second

// Event is one message: its type (snapshot, worker.updated, ...) and its JSON data.
type Event struct {
	Type string
	Data any
}

type Broker struct {
	mu      sync.Mutex
	clients map[chan Event]struct{}
}

func NewBroker() *Broker {
	return &Broker{clients: map[chan Event]struct{}{}}
}

// Subscribe returns the channel of a new client; it is closed when the client is dropped or unsubscribes.
func (b *Broker) Subscribe() (events <-chan Event, unsubscribe func()) {
	ch := make(chan Event, buffer)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() { b.drop(ch) }
}

func (b *Broker) drop(ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.clients[ch]; ok {
		delete(b.clients, ch)
		close(ch)
	}
}

// Publish sends an event to every client, never blocking: a client whose buffer is full is dropped.
func (b *Broker) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- e:
		default:
			delete(b.clients, ch)
			close(ch)
		}
	}
}

// Clients is the number of connected clients.
func (b *Broker) Clients() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.clients)
}

// Serve streams the events to one browser until it goes away: first the snapshot, then the published events.
func (b *Broker) Serve(w http.ResponseWriter, r *http.Request, snapshot func() any) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	events, unsubscribe := b.Subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := write(w, Event{Type: "snapshot", Data: snapshot()}); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(Heartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-events:
			if !ok {
				// dropped for being too slow: ending the response makes the browser reconnect
				return
			}
			if err := write(w, e); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func write(w http.ResponseWriter, e Event) error {
	data, err := json.Marshal(e.Data)
	if err != nil {
		log.Warn("failed to encode an event", "type", e.Type, "error", err)
		return nil
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data)
	return err
}
