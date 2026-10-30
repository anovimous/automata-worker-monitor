package main

import (
	"context"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// recordingLifecycle records the calls of handleSignals.
type recordingLifecycle struct {
	mu        sync.Mutex
	draining  []bool
	shutdowns int
}

func (r *recordingLifecycle) setDraining(d bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.draining = append(r.draining, d)
}

func (r *recordingLifecycle) shutdown() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shutdowns++
	return r.shutdowns == 1
}

// sendAndWait sends the signals and stops handleSignals once they were all handled.
func sendAndWait(t *testing.T, l lifecycle, exit func(int), sigs ...os.Signal) {
	t.Helper()
	ch := make(chan os.Signal)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		handleSignals(ctx, ch, l, exit)
		close(done)
	}()
	for _, s := range sigs {
		ch <- s // unbuffered: returns once handleSignals took it
	}
	// one more send makes sure the last signal was fully handled
	ch <- syscall.SIGHUP
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleSignals did not return after the context was canceled")
	}
}

func TestDrainAndUndrainSignals(t *testing.T) {
	l := &recordingLifecycle{}
	sendAndWait(t, l, func(int) { t.Error("exit called") }, syscall.SIGUSR1, syscall.SIGUSR1, syscall.SIGUSR2)
	if len(l.draining) != 3 || !l.draining[0] || !l.draining[1] || l.draining[2] {
		t.Errorf("setDraining calls = %v, want [true true false]", l.draining)
	}
}

func TestFirstShutdownSignalDoesNotExit(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGINT} {
		l := &recordingLifecycle{}
		sendAndWait(t, l, func(int) { t.Errorf("exit called after one %s", sig) }, sig)
		if l.shutdowns != 1 {
			t.Errorf("%s: shutdowns = %d, want 1", sig, l.shutdowns)
		}
	}
}

func TestSecondShutdownSignalExitsWithOne(t *testing.T) {
	l := &recordingLifecycle{}
	var codes []int
	sendAndWait(t, l, func(code int) { codes = append(codes, code) }, syscall.SIGTERM, syscall.SIGINT)
	if len(codes) != 1 || codes[0] != 1 {
		t.Errorf("exit codes = %v, want [1]", codes)
	}
}

func TestOtherSignalsAreIgnored(t *testing.T) {
	l := &recordingLifecycle{}
	sendAndWait(t, l, func(int) { t.Error("exit called") }, syscall.SIGHUP, syscall.SIGWINCH)
	if len(l.draining) != 0 || l.shutdowns != 0 {
		t.Errorf("calls = %v, %d shutdowns, want none", l.draining, l.shutdowns)
	}
}
