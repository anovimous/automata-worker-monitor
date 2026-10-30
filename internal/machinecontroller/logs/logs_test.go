package logs

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/docker"
)

func collect(t *testing.T, f *docker.Fake, id string) []controllerapi.LogLine {
	t.Helper()
	var lines []controllerapi.LogLine
	err := Stream(context.Background(), f, id, Options{Tail: 500}, func(l controllerapi.LogLine) error {
		lines = append(lines, l)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream returned an error: %v", err)
	}
	return lines
}

func TestStreamSplitsRedactsAndTimestamps(t *testing.T) {
	f := docker.NewFake()
	f.AddContainer(docker.ContainerInfo{ID: "c1"}, "Up")
	f.SetLogs("c1",
		"2026-10-29T10:42:13.000000001Z {\"msg\":\"job started\",\"jobId\":4211}\n"+
			"2026-10-29T10:42:14Z ffuf -H 'Authorization: Bearer abc' -u http://t/FUZZ\n",
		"2026-10-29T10:42:15Z dial amqp://automata:secret@rabbitmq:5672/ failed\n")

	lines := collect(t, f, "c1")
	sort.Slice(lines, func(i, j int) bool { return lines[i].TS.Before(lines[j].TS) })
	if len(lines) != 3 {
		t.Fatalf("lines = %+v, want 3", lines)
	}
	if lines[0].Stream != "stdout" || lines[0].Line != `{"msg":"job started","jobId":4211}` || lines[0].TS.Nanosecond() != 1 {
		t.Errorf("first line = %+v", lines[0])
	}
	if lines[1].Line != "ffuf -H 'Authorization: ***' -u http://t/FUZZ" {
		t.Errorf("second line = %q, want the header masked", lines[1].Line)
	}
	if lines[2].Stream != "stderr" || lines[2].Line != "dial amqp://***@rabbitmq:5672/ failed" {
		t.Errorf("third line = %+v", lines[2])
	}
}

func TestALineWithoutATimestampIsKept(t *testing.T) {
	f := docker.NewFake()
	f.AddContainer(docker.ContainerInfo{ID: "c1"}, "Up")
	f.SetLogs("c1", "no timestamp here\nlast line without a newline", "")

	lines := collect(t, f, "c1")
	if len(lines) != 2 || lines[0].Line != "no timestamp here" || !lines[0].TS.IsZero() || lines[1].Line != "last line without a newline" {
		t.Errorf("lines = %+v", lines)
	}
}

func TestALongLineIsCut(t *testing.T) {
	f := docker.NewFake()
	f.AddContainer(docker.ContainerInfo{ID: "c1"}, "Up")
	f.SetLogs("c1", strings.Repeat("a", 3*maxLine)+"\nnext\n", "")

	lines := collect(t, f, "c1")
	if len(lines) != 2 || len(lines[0].Line) != maxLine || lines[1].Line != "next" {
		t.Errorf("got %d lines, first %d bytes; want the long line cut at %d and the next kept", len(lines), len(lines[0].Line), maxLine)
	}
}

func TestStreamStopsOnAnEmitError(t *testing.T) {
	f := docker.NewFake()
	f.AddContainer(docker.ContainerInfo{ID: "c1"}, "Up")
	f.SetLogs("c1", "a\nb\nc\n", "")
	gone := errors.New("client gone")

	calls := 0
	err := Stream(context.Background(), f, "c1", Options{}, func(controllerapi.LogLine) error {
		calls++
		return gone
	})
	if !errors.Is(err, gone) || calls != 1 {
		t.Errorf("err = %v after %d calls, want the emit error after one", err, calls)
	}
}

func TestStreamOfAMissingContainer(t *testing.T) {
	err := Stream(context.Background(), docker.NewFake(), "nope", Options{}, func(controllerapi.LogLine) error { return nil })
	if !errors.Is(err, docker.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestStreamEndsWithItsContext(t *testing.T) {
	f := docker.NewFake()
	f.AddContainer(docker.ContainerInfo{ID: "c1"}, "Up")
	f.SetLogs("c1", strings.Repeat("line\n", 1000), "")
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- Stream(ctx, f, "c1", Options{}, func(controllerapi.LogLine) error {
			cancel()
			time.Sleep(time.Millisecond)
			return nil
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not end with its context")
	}
}

func TestTail(t *testing.T) {
	for in, want := range map[int]int{0: 500, -3: 500, 1: 1, 500: 500, 5000: 5000, 9999: 5000} {
		if got := Tail(in); got != want {
			t.Errorf("Tail(%d) = %d, want %d", in, got, want)
		}
	}
}
