package fleet

import (
	"context"
	log "log/slog"
	"time"
)

// Poll asks every host for its snapshot every interval until ctx is done, one goroutine per host. A host's polls
// never overlap: a slow one delays the next, it does not pile up.
func (f *Fleet) Poll(ctx context.Context) {
	done := make(chan struct{})
	for _, h := range f.cfg.Hosts {
		go func() {
			defer func() { done <- struct{}{} }()
			ticker := time.NewTicker(f.cfg.PollInterval())
			defer ticker.Stop()
			for {
				f.PollHost(ctx, h.Name)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	for range f.cfg.Hosts {
		<-done
	}
}

// PollHost polls one host once.
func (f *Fleet) PollHost(ctx context.Context, hostName string) {
	client, ok := f.clients[hostName]
	if !ok {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, f.cfg.ControllerTimeout())
	defer cancel()
	snap, err := client.Snapshot(callCtx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		f.HostDown(ctx, hostName, err)
		return
	}
	f.ApplySnapshot(ctx, hostName, snap)
}

// Purge applies the retention of the events and of the removed workers.
func (f *Fleet) Purge(ctx context.Context) {
	events, workers, err := f.store.Purge(ctx, f.now(), f.cfg.Retention.EventsDays, f.cfg.Retention.RemovedWorkersDays)
	if err != nil {
		log.Error("the retention purge failed", "error", err)
		return
	}
	if events+workers > 0 {
		log.Info("purged old rows", "events", events, "removedWorkers", workers)
	}
}

// PurgeEvery runs Purge at once and then every interval until ctx is done.
func (f *Fleet) PurgeEvery(ctx context.Context, interval time.Duration) {
	for {
		f.Purge(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
