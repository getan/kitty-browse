package browsepane

import (
	"context"
	"testing"
	"time"

	"github.com/getan/kitty-browse/internal/term"
)

func wheelEvent(n, col, row int) term.Event {
	return term.Event{Kind: term.KeyMouse, Mouse: term.MouseEvent{Wheel: n, Col: col, Row: row}}
}

func TestInputQueueCoalescesWheelBurst(t *testing.T) {
	q := newInputQueue(2)
	for i := 0; i < 10000; i++ {
		if !q.Push(context.Background(), wheelEvent(2, i, term.ChromeRows+1)) {
			t.Fatal("push failed")
		}
	}
	s := q.Stats()
	if s.Received != 10000 || s.Coalesced != 9999 || s.Pending != 1 {
		t.Fatalf("stats = %+v", s)
	}
	ev, ok := q.Pop()
	if !ok || ev.Mouse.Wheel != 20000 || ev.Mouse.Col != 9999 {
		t.Fatalf("event = %+v, %v", ev, ok)
	}
}

func TestInputQueueReverseReplacesTail(t *testing.T) {
	q := newInputQueue(1)
	q.Push(context.Background(), wheelEvent(9, 1, 3))
	q.Push(context.Background(), wheelEvent(-2, 8, 3))
	ev, _ := q.Pop()
	if ev.Mouse.Wheel != -2 || ev.Mouse.Col != 8 || q.Stats().Dropped != 1 {
		t.Fatalf("event=%+v stats=%+v", ev, q.Stats())
	}
}

func TestInputQueueBarriersAndKeyOrder(t *testing.T) {
	q := newInputQueue(8)
	w1, w2 := wheelEvent(1, 1, 3), wheelEvent(2, 2, 3)
	click := term.Event{Kind: term.KeyMouse, Mouse: term.MouseEvent{Press: true, Row: 3}}
	key := term.Event{Kind: term.KeyText, Rune: 'x'}
	for _, ev := range []term.Event{w1, click, w2, key} {
		q.Push(context.Background(), ev)
	}
	for i, want := range []term.Event{w1, click, w2, key} {
		got, ok := q.Pop()
		if !ok || got != want {
			t.Fatalf("pop %d = %+v, %v; want %+v", i, got, ok, want)
		}
	}
}

func TestInputQueueFullCancellationAndWakeup(t *testing.T) {
	q := newInputQueue(1)
	q.Push(context.Background(), term.Event{Kind: term.KeyText, Rune: 'a'})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if q.Push(ctx, term.Event{Kind: term.KeyText, Rune: 'b'}) {
		t.Fatal("canceled push succeeded")
	}
	done := make(chan bool, 1)
	go func() { done <- q.Push(context.Background(), term.Event{Kind: term.KeyText, Rune: 'c'}) }()
	select {
	case <-done:
		t.Fatal("full queue did not block")
	case <-time.After(10 * time.Millisecond):
	}
	q.Pop()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("woken push failed")
		}
	case <-time.After(time.Second):
		t.Fatal("pop did not wake push")
	}
}

func TestInputQueueSeparatesChromeAndPageWheels(t *testing.T) {
	q := newInputQueue(4)
	q.Push(context.Background(), wheelEvent(1, 1, term.ChromeRows))
	q.Push(context.Background(), wheelEvent(1, 2, term.ChromeRows+1))
	if s := q.Stats(); s.Pending != 2 || s.Coalesced != 0 {
		t.Fatalf("stats = %+v", s)
	}
	first, _ := q.Pop()
	second, _ := q.Pop()
	if first.Mouse.Row != term.ChromeRows || second.Mouse.Row != term.ChromeRows+1 {
		t.Fatalf("wrong order: %+v %+v", first, second)
	}
}

func TestInputQueueReadyDrainsOneAtATime(t *testing.T) {
	q := newInputQueue(3)
	q.Push(context.Background(), term.Event{Kind: term.KeyText, Rune: 'a'})
	q.Push(context.Background(), term.Event{Kind: term.KeyText, Rune: 'b'})
	<-q.Ready()
	q.Pop()
	select {
	case <-q.Ready():
	case <-time.After(time.Second):
		t.Fatal("remaining event was not announced")
	}
}
