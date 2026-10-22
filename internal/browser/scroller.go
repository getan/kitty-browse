package browser

import (
	"context"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

const (
	scrollFrameInterval = 16 * time.Millisecond
	scrollBusyWindow    = 125 * time.Millisecond
	scrollCallTimeout   = 400 * time.Millisecond
)

// ScrollFunc sends one accumulated wheel movement. Implementations must honor
// ctx cancellation; at most one call is made at a time.
type ScrollFunc func(ctx context.Context, x, y, deltaY float64) error

// ScrollStats is a race-safe snapshot of a Scroller.
type ScrollStats struct {
	Inputs        uint64
	CDPCalls      uint64
	Merged        uint64
	DroppedInputs uint64
	DroppedDeltaY float64
	PendingDeltaY float64
	InFlight      bool
	Stopped       bool
}

// Scroller coalesces high-frequency wheel input into one serial dispatch per
// display frame. It owns exactly one worker goroutine, irrespective of the
// number of calls to Push.
type Scroller struct {
	mu       sync.Mutex
	dispatch ScrollFunc
	ctx      context.Context
	cancel   context.CancelFunc

	x, y         float64
	pending      float64
	pendingCount uint64
	inflight     bool
	lastInput    time.Time
	stopped      bool
	err          error
	stats        ScrollStats
}

// NewScroller starts a frame-paced scroll dispatcher. A nil dispatch is
// permitted (and treated as a no-op), which is useful while a target changes.
func NewScroller(parent context.Context, dispatch ScrollFunc) *Scroller {
	ctx, cancel := context.WithCancel(parent)
	s := &Scroller{ctx: ctx, cancel: cancel, dispatch: dispatch}
	go s.run()
	return s
}

// Push records wheel input without waiting for the worker or CDP. Coordinates
// use latest-pointer semantics. Same-direction movement is accumulated in
// full. On a direction reversal, unsent movement in the old direction is
// discarded so stale momentum cannot run for seconds behind the user's hand.
func (s *Scroller) Push(x, y, deltaY float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stats.Inputs++
	if s.stopped || s.ctx.Err() != nil {
		s.stats.DroppedInputs++
		s.stats.DroppedDeltaY += deltaY
		return
	}
	s.lastInput = time.Now()
	s.x, s.y = x, y
	if deltaY == 0 {
		return
	}
	if s.pending != 0 && oppositeSigns(s.pending, deltaY) {
		s.stats.DroppedInputs += s.pendingCount
		s.stats.DroppedDeltaY += s.pending
		s.pending = 0
		s.pendingCount = 0
	}
	s.pending += deltaY
	s.pendingCount++
}

// Busy reports pending or in-flight work, and remains true briefly after the
// latest input. The grace period lets a UI avoid competing screenshot/title
// requests while a touchpad gesture is still arriving.
func (s *Scroller) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false
	}
	return s.pending != 0 || s.inflight || (!s.lastInput.IsZero() && time.Since(s.lastInput) < scrollBusyWindow)
}

// Err returns the first dispatch error, if any.
func (s *Scroller) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Stop cancels an in-flight dispatch and drops unsent movement. It is
// idempotent and does not wait for a misbehaving ScrollFunc to return.
func (s *Scroller) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.stats.DroppedInputs += s.pendingCount
	s.stats.DroppedDeltaY += s.pending
	s.pending = 0
	s.pendingCount = 0
	s.cancel()
	s.mu.Unlock()
}

// Stats returns a consistent snapshot. PendingDeltaY is signed; positive is
// downward. Merged counts input events combined behind another input in a
// completed CDP call. Movement discarded by reversal or Stop is reported
// separately.
func (s *Scroller) Stats() ScrollStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.stats
	out.PendingDeltaY = s.pending
	out.InFlight = s.inflight
	out.Stopped = s.stopped
	return out
}

func (s *Scroller) run() {
	ticker := time.NewTicker(scrollFrameInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			s.stopFromContext()
			return
		case <-ticker.C:
			x, y, delta, count, ok := s.takePending()
			if !ok {
				continue
			}
			var err error
			if s.dispatch != nil {
				err = s.dispatch(s.ctx, x, y, delta)
			}
			s.finishDispatch(count, err)
		}
	}
}

func (s *Scroller) takePending() (x, y, delta float64, count uint64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.ctx.Err() != nil || s.pending == 0 {
		return 0, 0, 0, 0, false
	}
	x, y, delta, count = s.x, s.y, s.pending, s.pendingCount
	s.pending, s.pendingCount = 0, 0
	s.inflight = true
	return x, y, delta, count, true
}

func (s *Scroller) finishDispatch(count uint64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight = false
	s.stats.CDPCalls++
	if count > 1 {
		s.stats.Merged += count - 1
	}
	if err != nil && s.err == nil {
		s.err = err
	}
}

func (s *Scroller) stopFromContext() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped {
		s.stopped = true
		s.stats.DroppedInputs += s.pendingCount
		s.stats.DroppedDeltaY += s.pending
		s.pending = 0
		s.pendingCount = 0
	}
}

func oppositeSigns(a, b float64) bool { return (a < 0 && b > 0) || (a > 0 && b < 0) }

// ScrollContext sends one wheel event through this Session's persistent target
// while respecting ctx cancellation. Each CDP operation is capped at 400ms.
func (s *Session) ScrollContext(ctx context.Context, x, y, deltaY float64) error {
	return s.run(ctx, scrollCallTimeout, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		_, err := cdp.Call(ctx, target, dispatchWheelEvent, wheelEventParams{
			Type: input.DispatchMouseEventTypeMouseWheel,
			X:    x, Y: y, DeltaX: 0, DeltaY: deltaY,
		})
		return err
	}))
}
