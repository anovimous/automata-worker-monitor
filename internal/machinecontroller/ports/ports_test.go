package ports

import (
	"net"
	"testing"

	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/config"
)

func TestAllocateTakesTheLowestFreePort(t *testing.T) {
	busy := map[int]bool{19101: true}
	a := &Allocator{r: config.PortRange{Min: 19100, Max: 19104}, isFree: func(p int) bool { return !busy[p] }}

	tests := []struct {
		used map[int]bool
		want int
	}{
		{nil, 19100},
		{map[int]bool{19100: true}, 19102},
		{map[int]bool{19100: true, 19102: true, 19103: true}, 19104},
	}
	for _, tt := range tests {
		got, err := a.Allocate(tt.used)
		if err != nil || got != tt.want {
			t.Errorf("Allocate(%v) = %d, %v, want %d", tt.used, got, err, tt.want)
		}
	}
}

func TestAllocateFailsWhenTheRangeIsFull(t *testing.T) {
	a := &Allocator{r: config.PortRange{Min: 19100, Max: 19101}, isFree: func(int) bool { return true }}
	if _, err := a.Allocate(map[int]bool{19100: true, 19101: true}); err == nil {
		t.Error("Allocate returned a port from a full range")
	}
}

func TestListenableSeesABoundPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if listenable(port) {
		t.Errorf("port %d is bound but listenable says it is free", port)
	}
	ln.Close()
	if !listenable(port) {
		t.Errorf("port %d is free again but listenable says it is not", port)
	}
}

func TestNewChecksTheRealPorts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	got, err := New(config.PortRange{Min: port, Max: port}).Allocate(nil)
	if err == nil {
		t.Errorf("Allocate = %d, want an error for a range whose only port is bound", got)
	}
}
