package main

import (
	"context"
	log "log/slog"
	"os"
	"syscall"
)

// lifecycle is the part of *fake the signals act on.
type lifecycle interface {
	setDraining(bool)
	shutdown() (first bool)
}

// handleSignals applies the signals of the telemetry contract until ctx is done: SIGUSR1 drains, SIGUSR2 undrains,
// SIGTERM/SIGINT shut down, and a second one exits with 1 at once.
func handleSignals(ctx context.Context, sigs <-chan os.Signal, l lifecycle, exit func(code int)) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig := <-sigs:
			switch sig {
			case syscall.SIGUSR1:
				l.setDraining(true)
			case syscall.SIGUSR2:
				l.setDraining(false)
			case syscall.SIGTERM, syscall.SIGINT:
				if !l.shutdown() {
					log.Warn("second shutdown signal, exiting now")
					exit(1)
				}
			}
		}
	}
}
