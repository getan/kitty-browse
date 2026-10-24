package browsepane

import (
	"context"
	"sync"

	"github.com/getan/kitty-browse/internal/term"
)

type inputQueueStats struct {
	Received  uint64
	Coalesced uint64
	Dropped   uint64
	Pending   int
}

type inputQueue struct {
	mu    sync.Mutex
	items []term.Event
	cap   int
	ready chan struct{}
	space chan struct{}
	stats inputQueueStats
}

func newInputQueue(capacity int) *inputQueue {
	if capacity < 1 {
		capacity = 1
	}
	return &inputQueue{
		items: make([]term.Event, 0, capacity), cap: capacity,
		ready: make(chan struct{}, 1), space: make(chan struct{}),
	}
}

func (q *inputQueue) Push(ctx context.Context, ev term.Event) bool {
	q.mu.Lock()
	q.stats.Received++
	q.mu.Unlock()
	for {
		if ctx.Err() != nil {
			q.mu.Lock()
			q.stats.Dropped++
			q.mu.Unlock()
			return false
		}
		q.mu.Lock()
		if wheel(ev) && len(q.items) != 0 {
			i := len(q.items) - 1
			old := q.items[i]
			if wheel(old) && wheelRegion(old) == wheelRegion(ev) {
				if sameSign(old.Mouse.Wheel, ev.Mouse.Wheel) {
					ev.Mouse.Wheel += old.Mouse.Wheel
					q.stats.Coalesced++
				} else {
					q.stats.Dropped++
				}
				q.items[i] = ev
				q.signalReady()
				q.mu.Unlock()
				return true
			}
		}
		if len(q.items) < q.cap {
			q.items = append(q.items, ev)
			q.signalReady()
			q.mu.Unlock()
			return true
		}
		space := q.space
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			q.mu.Lock()
			q.stats.Dropped++
			q.mu.Unlock()
			return false
		case <-space:
		}
	}
}

func (q *inputQueue) Ready() <-chan struct{} { return q.ready }

func (q *inputQueue) Pop() (term.Event, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return term.Event{}, false
	}
	ev := q.items[0]
	copy(q.items, q.items[1:])
	q.items[len(q.items)-1] = term.Event{}
	q.items = q.items[:len(q.items)-1]
	close(q.space)
	q.space = make(chan struct{})
	if len(q.items) != 0 {
		q.signalReady()
	} else {
		select {
		case <-q.ready:
		default:
		}
	}
	return ev, true
}

func (q *inputQueue) Stats() inputQueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.stats
	s.Pending = len(q.items)
	return s
}

func (q *inputQueue) signalReady() {
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

func wheel(ev term.Event) bool {
	return ev.Kind == term.KeyMouse && ev.Mouse.Wheel != 0 && !ev.Mouse.Motion
}

func wheelRegion(ev term.Event) bool { return ev.Mouse.Row <= term.ChromeRows }

func sameSign(a, b int) bool { return a < 0 == (b < 0) }
