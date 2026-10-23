package main

import (
	"context"
	"image"
	"time"
)

const (
	firstFrameTimeout = 5 * time.Second
	maxRenderFailures = 2
	maxRenderRestarts = 2
	renderRetryDelay  = 2 * time.Second
)

// renderHealth belongs to one selected tab/navigation. A successful CDP start
// is not a successful render: only an actual image marks the renderer ready.
// Idle pages need not emit frames; consecutive failed PNG probes (not silence)
// trigger recovery. Restarts have a total budget, even across brief successes.
// Only a deliberate navigation/tab change resets that budget.
type renderHealth struct {
	started   time.Time
	retryAt   time.Time
	ready     bool
	failures  int
	restarts  int
	exhausted bool
}

func newRenderHealth(now time.Time) renderHealth { return renderHealth{started: now} }

func (h *renderHealth) frame() (becameReady bool) {
	becameReady = !h.ready || h.failures != 0 || h.exhausted
	h.ready, h.exhausted, h.failures = true, false, 0
	return becameReady
}

func (h *renderHealth) failed() {
	h.failures++
	if h.failures >= maxRenderFailures && h.restarts >= maxRenderRestarts {
		h.exhausted = true
	}
}

func (h *renderHealth) transportFailed() {
	h.failures = max(h.failures, maxRenderFailures)
	if h.restarts >= maxRenderRestarts {
		h.exhausted = true
	}
}

func (h *renderHealth) recoveryDue(now time.Time) bool {
	return !h.exhausted && h.failures >= maxRenderFailures &&
		h.restarts < maxRenderRestarts && !now.Before(h.retryAt)
}

func (h *renderHealth) restarting(now time.Time) {
	h.restarts++
	h.failures, h.ready = 0, false
	h.started, h.retryAt = now, now.Add(renderRetryDelay)
}

func (h *renderHealth) waitingTooLong(now time.Time) bool {
	return !h.ready && !now.Before(h.started.Add(firstFrameTimeout))
}

const renderUnavailable = "Page image unavailable; automatic recovery stopped. Reload or switch tabs to retry."

// renderNotice owns only its own status message, so a subsequent successful
// frame cannot erase an unrelated navigation, download or screenshot error.
type renderNotice struct{ text string }

func (n *renderNotice) clear(status *string) {
	if n.text != "" && *status == n.text {
		*status = ""
	}
	n.text = ""
}

func (n *renderNotice) set(status *string, text string) {
	n.text, *status = text, text
}

type stillResult struct {
	image    image.Image
	err      error
	forFrame time.Time
	started  time.Time
}

// Only one PNG probe runs at a time, off the UI goroutine. Cancelling on tab,
// viewport or navigation changes abandons its channel: even a late result from
// an old target cannot paint over the newly selected page. A buffered result
// lets that old goroutine finish without waiting for a receiver.
type stillProbe struct {
	result <-chan stillResult
	cancel context.CancelFunc
}

func (p *stillProbe) start(ctx context.Context, forFrame time.Time, capture func(context.Context) (image.Image, error)) {
	if p.result != nil {
		return
	}
	ctx, p.cancel = context.WithCancel(ctx)
	ch := make(chan stillResult, 1)
	p.result = ch
	started := time.Now()
	go func() {
		img, err := capture(ctx)
		ch <- stillResult{image: img, err: err, forFrame: forFrame, started: started}
	}()
}

func (p *stillProbe) stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.result, p.cancel = nil, nil
}
