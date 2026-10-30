package main

import (
	"testing"
	"time"
)

func TestLoopReturnsTheExitCodeOfTheStep(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	ticks := make(chan time.Time)
	result := make(chan int)
	go func() { result <- loop(tf.fake, ticks) }()

	ticks <- t0
	tf.setDraining(true)
	for {
		select {
		case code := <-result:
			if code != 0 {
				t.Errorf("loop returned %d, want 0 after a drain", code)
			}
			return
		case ticks <- t0:
		case <-time.After(time.Second):
			t.Fatal("loop did not return after a drain")
		}
	}
}

func TestLoopReturnsZeroWhenTheTicksEnd(t *testing.T) {
	tf := newTestFake(t, scenarioNormal)
	ticks := make(chan time.Time)
	close(ticks)
	if code := loop(tf.fake, ticks); code != 0 {
		t.Errorf("loop returned %d, want 0", code)
	}
}
