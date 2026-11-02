// Package ringbuf keeps the last points of a series in memory: the live history of the charts (1 h at 5 s). It is lost
// when the workers control plane restarts; the minute samples in SQLite are not (plan/09-history.md).
package ringbuf

import "sync"

// Size is 1 h of points at one point every 5 s.
const Size = 720

// Ring holds the last points added, the oldest dropped first. It is safe for concurrent use.
type Ring[T any] struct {
	mu   sync.Mutex
	buf  []T
	next int
	full bool
}

func New[T any](size int) *Ring[T] {
	return &Ring[T]{buf: make([]T, size)}
}

// Add appends a point.
func (r *Ring[T]) Add(p T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = p
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// Points returns the points held, oldest first.
func (r *Ring[T]) Points() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]T(nil), r.buf[:r.next]...)
	}
	out := make([]T, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	return append(out, r.buf[:r.next]...)
}

// Len is the number of points held.
func (r *Ring[T]) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return len(r.buf)
	}
	return r.next
}
