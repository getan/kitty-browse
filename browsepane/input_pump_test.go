package browsepane

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/getan/kitty-browse/internal/term"
)

// inputPumpJoinTimeout bounds every wait in these tests, so a reader that cannot
// be interrupted fails the test instead of hanging the suite.
const inputPumpJoinTimeout = 5 * time.Second

// inputPipe is a stdin stand-in: a real pipe is an *os.File, so the pump takes
// the same interruptible code path a terminal does, without needing a PTY.
func inputPipe(t *testing.T) (read, write *os.File) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = read.Close()
		_ = write.Close()
	})
	return read, write
}

// waitForEvents waits until the queue holds something to pop.
func waitForEvents(t *testing.T, events *inputQueue) {
	t.Helper()
	if events.Stats().Pending != 0 {
		return
	}
	select {
	case <-events.Ready():
	case <-time.After(inputPumpJoinTimeout):
		t.Fatal("the pane never reported reading the key it was given")
	}
}

// readOwnEnd reads from the caller's own end of stdin, in a goroutine so that a
// steal by a leftover reader fails the test instead of blocking it.
func readOwnEnd(t *testing.T, stdin *os.File) string {
	t.Helper()
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		buf := make([]byte, 1)
		n, err := stdin.Read(buf)
		done <- result{text: string(buf[:n]), err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("the pane disturbed the caller's stdin: %v", got.err)
		}
		return got.text
	case <-time.After(inputPumpJoinTimeout):
		t.Fatal("the caller's own read never returned: something else is still reading stdin")
		return ""
	}
}

func writeByte(t *testing.T, w *os.File, b byte) {
	t.Helper()
	if _, err := w.Write([]byte{b}); err != nil {
		t.Fatalf("write %q: %v", string(b), err)
	}
}

// stopWithin fails the test when stop cannot finish, so the regression this
// guards (a read nobody can interrupt) is reported in seconds instead of hanging
// the suite until the go test timeout.
func stopWithin(t *testing.T, pump *inputPump, within time.Duration) {
	t.Helper()
	stopped := make(chan struct{})
	go func() {
		pump.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(within):
		t.Fatalf("stop did not finish within %v: the blocking read was not interrupted", within)
	}
}

// A pane that quits, or whose control socket fails to start, while its reader is
// blocked in a read must interrupt that read and wait for the goroutine. This is
// the regression: canceling the run context alone never unblocked term.ReadEvent.
func TestInputPumpCancelUnblocksBlockingReadAndJoins(t *testing.T) {
	stdin, keyboard := inputPipe(t)
	events := newInputQueue(8)
	pump, err := startInput(context.Background(), stdin, events)
	if err != nil {
		t.Fatalf("startInput: %v", err)
	}
	// Nothing is written, so the reading goroutine parks inside ReadEvent.
	time.Sleep(50 * time.Millisecond)
	stopWithin(t, pump, inputPumpJoinTimeout)
	if got := events.Stats(); got.Received != 0 || got.Pending != 0 {
		t.Fatalf("a keyless run reported input: %+v", got)
	}
	// The caller keeps its own stdin, still open and still unread.
	writeByte(t, keyboard, 'q')
	if got := readOwnEnd(t, stdin); got != "q" {
		t.Fatalf("caller read %q after the pane returned, want %q", got, "q")
	}
}

// The same guarantee holds when the parent context is canceled instead of the
// pump: the parked read has to come back and the goroutine has to be joinable.
func TestInputPumpStopsWhenParentContextIsCanceled(t *testing.T) {
	stdin, keyboard := inputPipe(t)
	events := newInputQueue(8)
	ctx, cancel := context.WithCancel(context.Background())
	pump, err := startInput(ctx, stdin, events)
	if err != nil {
		cancel()
		t.Fatalf("startInput: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	stopWithin(t, pump, inputPumpJoinTimeout)
	writeByte(t, keyboard, 'x')
	if got := readOwnEnd(t, stdin); got != "x" {
		t.Fatalf("caller read %q after cancellation, want %q", got, "x")
	}
}

// While the pane runs, keys still reach the event queue: the interruptible
// reader must not break ordinary input.
func TestInputPumpDeliversKeysWhileRunning(t *testing.T) {
	stdin, keyboard := inputPipe(t)
	events := newInputQueue(8)
	pump, err := startInput(context.Background(), stdin, events)
	if err != nil {
		t.Fatalf("startInput: %v", err)
	}
	writeByte(t, keyboard, 'q')
	waitForEvents(t, events)
	ev, ok := events.Pop()
	if !ok || ev.Kind != term.KeyText || ev.Rune != 'q' {
		t.Fatalf("event = %+v, %v; want the q key", ev, ok)
	}
	pump.stop()
}

// Repeated runs over the same stdin must each stop cleanly. A leftover reader
// from an earlier run would eat the key written after the last run and the
// caller's own read would never return.
func TestInputPumpRepeatedRunsLeaveNoReaderBehind(t *testing.T) {
	stdin, keyboard := inputPipe(t)
	events := newInputQueue(8)
	for run := 0; run < 5; run++ {
		pump, err := startInput(context.Background(), stdin, events)
		if err != nil {
			t.Fatalf("run %d: startInput: %v", run, err)
		}
		writeByte(t, keyboard, 'a')
		waitForEvents(t, events)
		if ev, ok := events.Pop(); !ok || ev.Rune != 'a' {
			t.Fatalf("run %d: event = %+v, %v; want the a key", run, ev, ok)
		}
		stopWithin(t, pump, inputPumpJoinTimeout)
		if got := events.Stats().Pending; got != 0 {
			t.Fatalf("run %d: %d events left queued after stop", run, got)
		}
	}
	writeByte(t, keyboard, 'z')
	if got := readOwnEnd(t, stdin); got != "z" {
		t.Fatalf("caller read %q after five runs, want %q", got, "z")
	}
}

// stop is what every pane exit path defers, so it has to tolerate being reached
// twice (a quit path plus the deferred teardown).
func TestInputPumpStopIsIdempotent(t *testing.T) {
	stdin, _ := inputPipe(t)
	pump, err := startInput(context.Background(), stdin, newInputQueue(1))
	if err != nil {
		t.Fatalf("startInput: %v", err)
	}
	pump.stop()
	pump.stop()
}

// A source whose blocking read cannot be interrupted would leave a goroutine
// nobody can join. The pane's stdin is always an *os.File, so refusing anything
// else only turns an impossible teardown into a startup error.
func TestInputPumpRejectsAnUninterruptibleSource(t *testing.T) {
	if _, err := startInput(context.Background(), strings.NewReader("q"), newInputQueue(1)); err == nil {
		t.Fatal("an uninterruptible reader was accepted")
	}
}
