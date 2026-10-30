package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMemHogTarget(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var defaultLimit int64 = memHogDefaultLimit
	defaultTarget := int64(float64(defaultLimit) * memHogRatio)
	cases := []struct {
		name, path string
		want       int64
	}{
		{"limit", write("limit", "4294967296\n"), 3435973836},
		{"no limit", write("max", "max\n"), defaultTarget},
		{"garbage", write("bad", "lots"), defaultTarget},
		{"missing file", filepath.Join(dir, "absent"), defaultTarget},
	}
	for _, c := range cases {
		if got := memHogTarget(c.path); got != c.want {
			t.Errorf("%s: memHogTarget = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestTouchedAllocatesEveryPage(t *testing.T) {
	b := touched(3*4096 + 10)
	if len(b) != 3*4096+10 {
		t.Fatalf("len = %d", len(b))
	}
	for i := 0; i < len(b); i += 4096 {
		if b[i] != 1 {
			t.Errorf("page at %d not written", i)
		}
	}
}

// The hogs run until their context ends, busy or not.
func TestHogsStopWithTheirContext(t *testing.T) {
	for name, hog := range map[string]func(context.Context, func() bool){
		"cpu":    burnCPU,
		"memory": func(ctx context.Context, running func() bool) { hogMemory(ctx, running, 2*memHogChunk) },
	} {
		for _, busy := range []bool{true, false} {
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				hog(ctx, func() bool { return busy })
				close(done)
			}()
			time.Sleep(3 * hogCheckInterval)
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Errorf("%s hog (busy %v) did not stop with its context", name, busy)
			}
		}
	}
}
