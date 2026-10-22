package browser

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type scrollCall struct{ x, y, delta float64 }

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}

func TestScrollerBurstIsNonBlockingAndCoalesced(t *testing.T) {
	var mu sync.Mutex
	var calls []scrollCall
	dispatch := func(ctx context.Context, x, y, delta float64) error {
		select {
		case <-time.After(25 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		mu.Lock()
		calls = append(calls, scrollCall{x, y, delta})
		mu.Unlock()
		return nil
	}
	s := NewScroller(context.Background(), dispatch)
	defer s.Stop()

	start := time.Now()
	for i := 0; i < 1000; i++ {
		s.Push(float64(i), float64(i+1), 1)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("Push blocked on dispatch: %v", elapsed)
	}
	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) > 0 && !s.Stats().InFlight && s.Stats().PendingDeltaY == 0
	})

	mu.Lock()
	defer mu.Unlock()
	var total float64
	for _, call := range calls {
		total += call.delta
	}
	if total != 1000 {
		t.Fatalf("dispatched delta = %v, want 1000", total)
	}
	if len(calls) >= 20 {
		t.Fatalf("CDP calls = %d, burst was not sufficiently coalesced", len(calls))
	}
	if got := calls[len(calls)-1]; got.x != 999 || got.y != 1000 {
		t.Fatalf("coordinates = (%v,%v), want latest pointer", got.x, got.y)
	}
	stats := s.Stats()
	if stats.Inputs != 1000 || stats.CDPCalls != uint64(len(calls)) || stats.Merged != 999 {
		t.Fatalf("unexpected stats: %+v (calls %d)", stats, len(calls))
	}
}

func TestScrollerDirectionReversalDropsOnlyOldPendingTail(t *testing.T) {
	started := make(chan scrollCall, 4)
	release := make(chan struct{}, 4)
	s := NewScroller(context.Background(), func(ctx context.Context, x, y, delta float64) error {
		started <- scrollCall{x, y, delta}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	defer s.Stop()

	s.Push(1, 2, 10)
	var first scrollCall
	select {
	case first = <-started:
	case <-time.After(time.Second):
		t.Fatal("first dispatch did not start")
	}
	if first.delta != 10 {
		t.Fatalf("first delta = %v", first.delta)
	}
	// This old-direction tail is pending behind the slow dispatch. Reversal
	// replaces it rather than replaying stale down-scroll after the up-scroll.
	s.Push(3, 4, 30)
	s.Push(5, 6, -7)
	s.Push(7, 8, -8)
	release <- struct{}{}

	var second scrollCall
	select {
	case second = <-started:
	case <-time.After(time.Second):
		t.Fatal("reversed dispatch did not start")
	}
	if second.delta != -15 || second.x != 7 || second.y != 8 {
		t.Fatalf("reversed dispatch = %+v, want latest pointer and -15", second)
	}
	release <- struct{}{}
	waitFor(t, time.Second, func() bool { return !s.Stats().InFlight })
	stats := s.Stats()
	if stats.DroppedDeltaY != 30 || stats.DroppedInputs != 1 {
		t.Fatalf("reversal drop stats = %+v", stats)
	}
}

func TestScrollerDispatchesFinalTailAndBusyGrace(t *testing.T) {
	calls := make(chan float64, 2)
	s := NewScroller(context.Background(), func(_ context.Context, _, _, delta float64) error {
		calls <- delta
		return nil
	})
	defer s.Stop()
	s.Push(0, 0, 4)
	select {
	case got := <-calls:
		if got != 4 {
			t.Fatalf("tail delta = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("final tail was not dispatched")
	}
	if !s.Busy() {
		t.Fatal("Busy false inside post-input grace period")
	}
	waitFor(t, 500*time.Millisecond, func() bool { return !s.Busy() })
}

func TestScrollerStopDropsPendingAndCancelsInflight(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	s := NewScroller(context.Background(), func(ctx context.Context, _, _, _ float64) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	})
	s.Push(0, 0, 1)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("dispatch did not start")
	}
	s.Push(0, 0, 9)
	start := time.Now()
	s.Stop()
	s.Stop()
	if time.Since(start) > 20*time.Millisecond {
		t.Fatal("Stop waited for dispatch")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("in-flight dispatch was not canceled")
	}
	s.Push(0, 0, 100)
	time.Sleep(2 * scrollFrameInterval)
	stats := s.Stats()
	if !stats.Stopped || stats.CDPCalls != 1 || stats.PendingDeltaY != 0 || s.Busy() {
		t.Fatalf("unexpected stopped state: %+v busy=%v", stats, s.Busy())
	}
	if stats.DroppedDeltaY != 109 || stats.DroppedInputs != 2 {
		t.Fatalf("unexpected dropped input stats: %+v", stats)
	}
}

func TestScrollerRecordsFirstError(t *testing.T) {
	first := errors.New("first")
	var n int
	s := NewScroller(context.Background(), func(context.Context, float64, float64, float64) error {
		n++
		if n == 1 {
			return first
		}
		return errors.New("second")
	})
	defer s.Stop()
	s.Push(0, 0, 1)
	waitFor(t, time.Second, func() bool { return s.Err() != nil })
	s.Push(0, 0, 1)
	waitFor(t, time.Second, func() bool { return s.Stats().CDPCalls == 2 })
	if !errors.Is(s.Err(), first) {
		t.Fatalf("Err = %v, want first dispatch error", s.Err())
	}
}

func TestScrollerParentCancellationDropsPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := NewScroller(ctx, func(context.Context, float64, float64, float64) error {
		t.Fatal("dispatch called after parent cancellation")
		return nil
	})
	s.Push(0, 0, 12)
	cancel()
	waitFor(t, time.Second, func() bool { return s.Stats().Stopped })
	if got := s.Stats(); got.PendingDeltaY != 0 || got.DroppedDeltaY != 12 {
		t.Fatalf("unexpected canceled stats: %+v", got)
	}
}
