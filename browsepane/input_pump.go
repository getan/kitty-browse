package browsepane

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/muesli/cancelreader"

	"github.com/getan/kitty-browse/internal/term"
)

// inputStopGrace bounds how long stop waits for a reader that reported it could
// not interrupt a read in flight. That only happens for a stdin at file
// descriptor 1024 or higher, where cancelreader falls back to a reader whose
// pending read cannot be woken; waiting forever would hang the host's exit.
const inputStopGrace = 250 * time.Millisecond

// inputPump is the one goroutine that reads terminal input for a pane run.
//
// It owns a cancelable child context and an interruptible reader, so a pane can
// always stop reading and wait for the goroutine to finish before it returns.
// That guarantee is the point: a pane that returned while its reader was still
// blocked in a read would leave a goroutine competing with the host application
// for the same stdin, and the next keystroke would vanish into a dead pane.
//
// The pump never closes the reader it was given: stdin belongs to the caller,
// which keeps reading it once the pane is gone.
type inputPump struct {
	events *inputQueue
	reader cancelreader.CancelReader
	cancel context.CancelFunc
	done   chan struct{}

	// disarm stops the parent-context hook, so a canceled parent cannot reach
	// into a pump that has already finished.
	disarm func() bool

	// once guards the one interrupt, so a second stop (a quit path plus the
	// deferred teardown) sees the same answer as the first.
	once        sync.Once
	interrupted bool
}

// startInput begins turning terminal bytes from in into events. in must be a
// file whose blocking reads can be interrupted; the pane's stdin always is, and
// anything else is rejected rather than read in a goroutine nobody can join.
func startInput(ctx context.Context, in io.Reader, events *inputQueue) (*inputPump, error) {
	source, ok := in.(cancelreader.File)
	if !ok {
		return nil, errors.New("kitty-browse: terminal input must be a file whose blocking read can be interrupted")
	}
	reader, err := cancelreader.NewReader(source)
	if err != nil {
		return nil, fmt.Errorf("kitty-browse: interruptible input: %w", err)
	}
	readCtx, cancel := context.WithCancel(ctx)
	pump := &inputPump{
		events: events,
		reader: reader,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	// A canceled parent stops the reading goroutine immediately, even while the
	// pane's own loop is busy somewhere else.
	pump.disarm = context.AfterFunc(ctx, func() { pump.interrupt() })
	go pump.read(readCtx)
	return pump, nil
}

func (p *inputPump) read(ctx context.Context) {
	defer close(p.done)
	defer p.reader.Close()
	defer func() { p.disarm() }()
	readInput(ctx, p.reader, p.events)
}

// interrupt cancels the child context and unblocks a pending read, and reports
// whether the reader could actually interrupt it. Reads after this return
// cancelreader.ErrCanceled without consuming input.
func (p *inputPump) interrupt() bool {
	p.once.Do(func() {
		p.cancel()
		p.interrupted = p.reader.Cancel()
	})
	return p.interrupted
}

// stop cancels the reader, unblocks any pending read and waits for the reading
// goroutine to exit. It is safe to call more than once and from any goroutine,
// and it never blocks once the reader is interruptible. The caller's stdin is
// left open and unread.
func (p *inputPump) stop() {
	if p == nil {
		return
	}
	if p.interrupt() {
		<-p.done
		return
	}
	select {
	case <-p.done:
	case <-time.After(inputStopGrace):
		debugf("input: pending read could not be interrupted; it will return on the next read")
	}
}

// readInput turns terminal bytes into events until the reader is canceled, the
// terminal goes away or ctx is done.
func readInput(ctx context.Context, in io.Reader, events *inputQueue) {
	r := bufio.NewReaderSize(in, 4096)
	for {
		ev, err := term.ReadEvent(r)
		if err != nil {
			if errors.Is(err, cancelreader.ErrCanceled) || ctx.Err() != nil {
				return // the pane stopped reading; not an input error
			}
			debugf("input error: %v", err)
			return
		}
		if ev.Kind == term.KeyText && ev.Rune == 0 {
			continue
		}
		if !events.Push(ctx, ev) {
			return
		}
	}
}
