package browserhost

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestHubRejectsInvalidViewWithoutStartingChromium(t *testing.T) {
	var nilHub *Hub
	if _, err := nilHub.OpenView(context.Background(), ViewOptions{Socket: "x"}); err == nil {
		t.Fatal("nil hub accepted a view")
	}
	h := &Hub{}
	if _, err := h.OpenView(context.Background(), ViewOptions{}); err == nil {
		t.Fatal("view without a socket was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.OpenView(ctx, ViewOptions{Socket: "x"}); err == nil {
		t.Fatal("canceled view was accepted")
	}
	if _, err := h.OpenView(context.Background(), ViewOptions{Socket: "x"}); err == nil {
		t.Fatal("closed hub accepted a view")
	}
}

func TestHubCloseIsConcurrentAndIdempotent(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	h := &Hub{stop: func() { mu.Lock(); calls++; mu.Unlock() }}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = h.Close() }()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("hub cleanup ran %d times", calls)
	}
}

func TestHubCloseCancelsPendingViewsBeforeReleasingBrowser(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	h := &Hub{startingCtx: ctx, cancelStarting: cancel, stop: func() { close(stopped) }}
	h.starting.Add(1)
	go func() {
		defer h.starting.Done()
		<-ctx.Done()
	}()
	done := make(chan error, 1)
	go func() { done <- h.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("hub did not cancel an in-flight view")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("hub did not release the browser")
	}
}
