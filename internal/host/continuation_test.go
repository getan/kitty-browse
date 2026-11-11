package host

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func continuationFor(b *Bridge) ContinueRequest {
	s := b.Interaction()
	return ContinueRequest{BrowserID: b.ID, InstanceEpoch: s.InstanceEpoch, PauseGeneration: s.PauseGeneration, RequestID: rand.Text(), ExpiresAt: time.Now().Add(time.Minute)}
}

func TestCheckedContinuationGenerationAndReceipts(t *testing.T) {
	b := NewBridge("view-a")
	b.Pause("manual")
	req := continuationFor(b)
	if err := b.ContinueChecked(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if b.Interaction().Paused || b.ContinuationOutcome(req) != "resumed" {
		t.Fatal("successful continuation lost its receipt")
	}
	b.Pause("captcha")
	if err := b.ContinueChecked(context.Background(), req); err != nil || !b.Interaction().Paused {
		t.Fatal("receipt replay cleared a later pause")
	}
	req.RequestID = rand.Text()
	if err := b.ContinueChecked(context.Background(), req); err == nil || !b.Interaction().Paused {
		t.Fatal("old pause generation was accepted")
	}
	if b.ContinuationOutcome(req) != "not_consumed" {
		t.Fatal("rejected continuation was not acknowledged")
	}
	req = continuationFor(b)
	req.BrowserID = "view-b"
	if err := b.ContinueChecked(context.Background(), req); err == nil || b.ContinuationOutcome(req) != "stale" {
		t.Fatal("decision crossed view identity")
	}
}

func TestCheckedContinuationBusyCancellationAndExpiry(t *testing.T) {
	b := NewBridge("view-a")
	finish, err := b.beginInteraction("fill", true)
	if err != nil {
		t.Fatal(err)
	}
	b.Pause("manual")
	req := continuationFor(b)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.ContinueChecked(ctx, req); err == nil || !b.Interaction().Paused || b.ContinuationOutcome(req) != "not_consumed" {
		t.Fatal("cancelled continuation modified the pause")
	}
	finish()
	if err := b.ContinueChecked(context.Background(), req); err == nil {
		t.Fatal("completed failure was replayed with the same request ID")
	}
	req = continuationFor(b)
	req.ExpiresAt = time.Now().Add(-time.Second)
	if err := b.ContinueChecked(context.Background(), req); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired decision: %v", err)
	}
	for i := range 100 {
		req.RequestID = rand.Text()
		_ = b.ContinueChecked(context.Background(), req)
		if len(b.interaction.receipts) > 64 {
			t.Fatalf("unbounded receipts after %d calls", i)
		}
	}
}

func TestCheckedContinuationWaitsWithoutReplayingInput(t *testing.T) {
	b := NewBridge("view-a")
	finish, _ := b.beginInteraction("submit", true)
	b.Pause("manual")
	req := continuationFor(b)
	done := make(chan error, 1)
	go func() { done <- b.ContinueChecked(context.Background(), req) }()
	// Receipt existence is an explicit in-flight barrier; do not sleep/race.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for b.ContinuationOutcome(req) != "pending" {
		select {
		case <-deadline.C:
			t.Fatal("continuation did not start")
		case <-tick.C:
		}
	}
	if !b.Interaction().Paused {
		t.Fatal("resumed before mutation drained")
	}
	finish()
	if err := <-done; err != nil || b.Interaction().Paused || b.Interaction().Busy {
		t.Fatalf("stable point recovery: %v %+v", err, b.Interaction())
	}
	if _, err := b.beginInteractionAt("submit", true, req.PauseGeneration-1); err == nil {
		t.Fatal("queued pre-pause mutation was replayed")
	}
}
