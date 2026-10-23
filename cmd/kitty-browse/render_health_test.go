package main

import (
	"context"
	"image"
	"testing"
	"time"
)

func TestRenderHealthRequiresARealFrame(t *testing.T) {
	now := time.Unix(1, 0)
	h := newRenderHealth(now)
	if h.ready || h.waitingTooLong(now.Add(firstFrameTimeout-time.Millisecond)) {
		t.Fatal("transport startup must not claim a frame, nor time out early")
	}
	if !h.waitingTooLong(now.Add(firstFrameTimeout)) {
		t.Fatal("missing first frame must become a visible health failure")
	}
	if !h.frame() || h.waitingTooLong(now.Add(time.Hour)) || !h.ready {
		t.Fatal("actual image must mark ready; an idle page must stay healthy")
	}
}

func TestRenderHealthBoundsRecoveryAndStopsProbes(t *testing.T) {
	now := time.Unix(1, 0)
	h := newRenderHealth(now)
	for attempt := 0; attempt < maxRenderRestarts; attempt++ {
		h.failed()
		if h.recoveryDue(now) {
			t.Fatal("one failed PNG must not trigger a restart")
		}
		h.failed()
		if !h.recoveryDue(now) {
			t.Fatalf("recovery %d was not scheduled", attempt+1)
		}
		h.restarting(now)
		now = now.Add(renderRetryDelay)
	}
	h.failed()
	h.failed()
	if !h.exhausted || h.recoveryDue(now.Add(time.Hour)) || h.restarts != maxRenderRestarts {
		t.Fatalf("must stop after bounded recovery: %+v", h)
	}
	// Frames may arrive late even after giving up; they can clear the warning
	// without spending another retry or losing the document.
	if !h.frame() || h.exhausted || !h.ready {
		t.Fatal("a late successful image must restore health")
	}
}

func TestRenderHealthIntermittentSuccessCannotResetBudget(t *testing.T) {
	now := time.Unix(1, 0)
	h := newRenderHealth(now)
	for i := 0; i < maxRenderRestarts; i++ {
		h.transportFailed()
		if !h.recoveryDue(now) {
			t.Fatal("transport error must schedule recovery")
		}
		h.restarting(now)
		h.frame()
		now = now.Add(renderRetryDelay)
	}
	h.transportFailed()
	if !h.exhausted || h.recoveryDue(now) {
		t.Fatal("brief successes must not create an infinite restart loop")
	}
	h = newRenderHealth(now) // explicit navigation/tab change
	if h.exhausted || h.restarts != 0 {
		t.Fatal("explicit user retry must get a fresh budget")
	}
}

func TestRenderHealthRecoveryHasBackoff(t *testing.T) {
	now := time.Unix(1, 0)
	h := newRenderHealth(now)
	h.restarting(now)
	h.transportFailed() // starting the new stream itself failed
	if h.recoveryDue(now) || !h.recoveryDue(now.Add(renderRetryDelay)) {
		t.Fatal("a failed restart must wait, not spin on every UI tick")
	}
}

func TestRenderNoticeDoesNotEraseAnotherError(t *testing.T) {
	var n renderNotice
	status := ""
	n.set(&status, "waiting for a frame")
	n.clear(&status)
	if status != "" {
		t.Fatal("successful frame must clear the render warning")
	}
	n.set(&status, "waiting for a frame")
	status = "navigation failed"
	n.clear(&status)
	if status != "navigation failed" {
		t.Fatal("successful frame must not erase an unrelated error")
	}
}

func TestStillProbeDoesNotBlockUIOrStartOverlappingProbes(t *testing.T) {
	var p stillProbe
	defer p.stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := make(chan struct{})
	p.start(ctx, time.Time{}, func(ctx context.Context) (image.Image, error) {
		close(started)
		<-ctx.Done() // simulate a stalled CDP screenshot
		return nil, ctx.Err()
	})
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("probe did not start")
	}
	// start returned while the screenshot is still pending. A second UI tick
	// must not create another goroutine/request.
	original := p.result
	p.start(ctx, time.Time{}, func(context.Context) (image.Image, error) {
		t.Error("overlapping probe started")
		return nil, nil
	})
	if p.result != original {
		t.Fatal("an in-flight probe was replaced")
	}
	p.stop()
	if p.result != nil {
		t.Fatal("cancelled probe must be removed from UI select")
	}
	select {
	case result := <-original:
		if result.err != context.Canceled {
			t.Fatalf("probe ignored cancellation: %v", result.err)
		}
	case <-ctx.Done():
		t.Fatal("cancelled probe did not finish")
	}
}

func TestStillProbeOldTabResultCannotReplaceNewTab(t *testing.T) {
	var p stillProbe
	defer p.stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	oldImage := image.NewRGBA(image.Rect(0, 0, 1, 1))
	newImage := image.NewRGBA(image.Rect(0, 0, 2, 2))
	releaseOld := make(chan struct{})
	p.start(ctx, time.Time{}, func(context.Context) (image.Image, error) {
		<-releaseOld // simulate a late response despite cancellation
		return oldImage, nil
	})
	oldResult := p.result
	p.stop() // select a different tab
	p.start(ctx, time.Time{}, func(context.Context) (image.Image, error) { return newImage, nil })
	close(releaseOld)
	select {
	case result := <-p.result:
		if result.image != newImage {
			t.Fatal("previous tab painted over the new tab")
		}
	case <-ctx.Done():
		t.Fatal("new tab probe did not finish")
	}
	select {
	case <-oldResult: // it finishes without blocking, but UI never receives it
	case <-ctx.Done():
		t.Fatal("abandoned probe leaked its goroutine")
	}
}
