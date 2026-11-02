package sse

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublishReachesEveryClient(t *testing.T) {
	b := NewBroker()
	a, unsubA := b.Subscribe()
	c, unsubC := b.Subscribe()
	defer unsubA()
	defer unsubC()

	b.Publish(Event{Type: "worker.updated", Data: 1})
	for _, ch := range []<-chan Event{a, c} {
		if e := <-ch; e.Type != "worker.updated" {
			t.Errorf("event = %+v", e)
		}
	}
	if b.Clients() != 2 {
		t.Errorf("clients = %d", b.Clients())
	}
}

func TestASlowClientIsDropped(t *testing.T) {
	b := NewBroker()
	slow, _ := b.Subscribe()
	for range buffer + 1 {
		b.Publish(Event{Type: "x"})
	}
	if b.Clients() != 0 {
		t.Fatal("the slow client is still subscribed")
	}
	n := 0
	for range slow {
		n++
	}
	if n != buffer {
		t.Errorf("the slow client got %d events before being dropped, want %d", n, buffer)
	}
}

func TestUnsubscribeTwiceIsFine(t *testing.T) {
	b := NewBroker()
	_, unsub := b.Subscribe()
	unsub()
	unsub()
	if b.Clients() != 0 {
		t.Error("still subscribed")
	}
}

func TestServeSendsTheSnapshotThenTheEvents(t *testing.T) {
	b := NewBroker()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.Serve(w, r, func() any { return map[string]int{"workers": 3} })
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content type = %q", resp.Header.Get("Content-Type"))
	}

	sc := bufio.NewScanner(resp.Body)
	next := func() (string, string) {
		var typ, data string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "" && typ != "":
				return typ, data
			}
		}
		return "", ""
	}
	if typ, data := next(); typ != "snapshot" || data != `{"workers":3}` {
		t.Fatalf("first event = %s %s", typ, data)
	}
	for b.Clients() == 0 {
		time.Sleep(time.Millisecond)
	}
	b.Publish(Event{Type: "worker.removed", Data: map[string]string{"id": "w-1"}})
	if typ, data := next(); typ != "worker.removed" || data != `{"id":"w-1"}` {
		t.Errorf("second event = %s %s", typ, data)
	}
}

func TestServeSendsHeartbeats(t *testing.T) {
	old := Heartbeat
	Heartbeat = 20 * time.Millisecond
	defer func() { Heartbeat = old }()
	b := NewBroker()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.Serve(w, r, func() any { return nil })
	}))
	defer ts.Close()

	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() == ": heartbeat" {
			return
		}
	}
	t.Error("no heartbeat")
}
