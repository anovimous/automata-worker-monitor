// Package ports gives host-network workers their telemetry port: in host mode every worker of the host listens on
// the same network, so each needs a port of its own.
package ports

import (
	"fmt"
	"net"
	"strconv"

	"github.com/anovimous/automata-worker-monitor/internal/machinecontroller/config"
)

// Allocator picks the ports. The caller serializes the creates, so that two of them can not pick the same port before
// the first container exists.
type Allocator struct {
	r      config.PortRange
	isFree func(port int) bool
}

func New(r config.PortRange) *Allocator {
	return &Allocator{r: r, isFree: listenable}
}

// Allocate returns the lowest port of the range that no worker holds (used, from the labels of the host-network
// workers) and that nothing else listens on.
func (a *Allocator) Allocate(used map[int]bool) (int, error) {
	for p := a.r.Min; p <= a.r.Max; p++ {
		if !used[p] && a.isFree(p) {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no telemetry port is free in %d-%d", a.r.Min, a.r.Max)
}

// listenable tells whether the port can be bound on the loopback, where a host-network worker listens.
func listenable(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}
