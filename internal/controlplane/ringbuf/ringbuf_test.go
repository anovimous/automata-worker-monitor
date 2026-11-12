package ringbuf

import (
	"slices"
	"sync"
	"testing"
)

func TestPointsAreOldestFirst(t *testing.T) {
	r := New[int](3)
	if got := r.Points(); len(got) != 0 || r.Len() != 0 {
		t.Errorf("empty ring = %v", got)
	}
	r.Add(1)
	r.Add(2)
	if got := r.Points(); !slices.Equal(got, []int{1, 2}) || r.Len() != 2 {
		t.Errorf("points = %v", got)
	}
	r.Add(3)
	r.Add(4)
	r.Add(5)
	if got := r.Points(); !slices.Equal(got, []int{3, 4, 5}) || r.Len() != 3 {
		t.Errorf("after wrapping = %v, want the last 3", got)
	}
}

func TestPointsIsACopy(t *testing.T) {
	r := New[int](2)
	r.Add(1)
	r.Points()[0] = 99
	if r.Points()[0] != 1 {
		t.Error("changing the result changed the ring")
	}
}

func TestConcurrentUse(t *testing.T) {
	r := New[int](Size)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			for j := range 1000 {
				r.Add(i*1000 + j)
				r.Points()
			}
		})
	}
	wg.Wait()
	if r.Len() != Size {
		t.Errorf("Len = %d, want %d", r.Len(), Size)
	}
}
