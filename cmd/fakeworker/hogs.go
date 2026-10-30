package main

import (
	"context"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

const (
	hogCheckInterval = 100 * time.Millisecond
	memHogChunk      = 16 << 20 // allocated every check, so 80 % of a 4 GB limit takes about 20 s
	memHogRatio      = 0.8
	// used when the container has no memory limit, so that a fakeworker never takes a host's memory
	memHogDefaultLimit = 1 << 30
)

// burnCPU keeps one core busy while running() is true, the cpu-hog scenario.
func burnCPU(ctx context.Context, running func() bool) {
	for ctx.Err() == nil {
		if !running() {
			time.Sleep(hogCheckInterval)
			continue
		}
		for end := time.Now().Add(hogCheckInterval); time.Now().Before(end); {
		}
	}
}

// hogMemory grows up to target bytes while running() is true and gives the memory back after, the mem-hog scenario.
func hogMemory(ctx context.Context, running func() bool, target int64) {
	var chunks [][]byte
	var held int64
	for ctx.Err() == nil {
		time.Sleep(hogCheckInterval)
		if running() {
			if held+memHogChunk <= target {
				chunks = append(chunks, touched(memHogChunk))
				held += memHogChunk
			}
			continue
		}
		if chunks != nil {
			chunks, held = nil, 0
			debug.FreeOSMemory()
		}
	}
}

// touched allocates n bytes and writes every page, so that they count in the container's memory.
func touched(n int) []byte {
	b := make([]byte, n)
	for i := 0; i < n; i += 4096 {
		b[i] = 1
	}
	return b
}

// memHogTarget is 80 % of the container's memory limit read from cgroup v2 memory.max ("max" or unreadable: a
// default).
func memHogTarget(memoryMax string) int64 {
	b, err := os.ReadFile(memoryMax)
	limit := int64(memHogDefaultLimit)
	if err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && n > 0 {
			limit = n
		}
	}
	return int64(float64(limit) * memHogRatio)
}
