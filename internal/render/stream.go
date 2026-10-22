// Package render turns a Chromium screencast into terminal image frames. It is
// intentionally smaller than termcast: the image occupies rows 2..N so the
// address bar can stay on row 1.
package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"iter"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/getan/kitty-browse/internal/captureguard"
	"github.com/kenshaw/rasterm"
)

// ErrNoGraphics means the terminal has no Kitty/iTerm2/Sixel encoder.
var ErrNoGraphics = errors.New("terminal has no graphics support")

// Config controls the screencast.
type Config struct {
	FPS     float64
	Quality int
	Width   int
	Height  int
	Cols    int
	Rows    int
	Encoder rasterm.Encoder
}

const (
	defaultFPS = 24
	minFPS     = 0.1
	maxFPS     = 240
)

// Stats is a snapshot of the screencast pipeline counters. Dropped includes
// raw frames superseded before decoding, filtered frames, and decoded frames
// superseded before the consumer received them.
type Stats struct {
	Received    uint64
	Acked       uint64
	Decoded     uint64
	Dropped     uint64
	Published   uint64
	DecodeTotal time.Duration
	DecodeLast  time.Duration
	Drawn       uint64
	WireBytes   uint64
	DrawTotal   time.Duration
	DrawLast    time.Duration
}

type streamStats struct {
	received, acked, decoded, dropped, published atomic.Uint64
	decodeTotal, decodeLast                      atomic.Int64
	drawn, wireBytes                             atomic.Uint64
	drawTotal, drawLast                          atomic.Int64
}

type rawFrame struct {
	data    []byte
	arrived time.Time
}

// Stream is a running CDP screencast. Frames contains the latest decoded
// viewport; the consumer can drop frames it cannot draw yet.
type Stream struct {
	ctx    context.Context
	cancel context.CancelFunc
	enc    rasterm.Encoder
	kitty  *kittyEncoder
	frames chan image.Image
	raw    chan rawFrame
	fps    float64
	stats  streamStats
	target string

	wg      sync.WaitGroup
	mu      sync.Mutex
	encMu   sync.Mutex // Never block capture/decode bookkeeping on a terminal write.
	err     error
	last    time.Time
	stopped bool
	width   int
	height  int
}

// Start begins a screencast and starts Chromium if the context has none.
func Start(ctx context.Context, cfg Config) (*Stream, error) {
	var err error
	if cfg.FPS, err = normalizeFPS(cfg.FPS); err != nil {
		return nil, err
	}
	if cfg.Quality < 0 || cfg.Quality > 100 {
		return nil, fmt.Errorf("quality must be 0..100")
	}
	if cfg.Width <= 0 {
		cfg.Width = 1280
	}
	if cfg.Height <= 0 {
		cfg.Height = 800
	}
	var kitty *kittyEncoder
	if cfg.Encoder == nil {
		kitty = &kittyEncoder{Cols: cfg.Cols, Rows: cfg.Rows}
		cfg.Encoder = rasterm.NewDefaultEncoder(
			kitty,
			rasterm.ITermEncoder{NoNewline: true},
			rasterm.SixelEncoder{NoNewline: true},
		)
	}
	if !cfg.Encoder.Available() {
		return nil, ErrNoGraphics
	}
	if err := chromedp.Do(ctx); err != nil {
		return nil, fmt.Errorf("start browser: %w", err)
	}

	rctx, cancel := context.WithCancel(ctx)
	frames := chromedp.Events(rctx, page.ScreencastFrame)
	quality := int64(cfg.Quality)
	if _, err := chromedp.Call(rctx, page.StartScreencast, page.StartScreencastParams{
		Format:        page.StartScreencastFormatJpeg,
		Quality:       &quality,
		EveryNthFrame: 1,
	}); err != nil {
		cancel()
		return nil, fmt.Errorf("start screencast: %w", err)
	}

	s := &Stream{
		ctx: ctx, cancel: cancel, enc: cfg.Encoder, kitty: kitty, frames: make(chan image.Image, 1),
		raw: make(chan rawFrame, 1), fps: cfg.FPS, width: cfg.Width, height: cfg.Height,
	}
	if c := chromedp.FromContext(ctx); c != nil && c.Target != nil {
		s.target = string(c.Target.TargetID)
	}
	s.wg.Add(2)
	go s.receive(rctx, frames)
	go s.decode(rctx)
	return s, nil
}

func normalizeFPS(fps float64) (float64, error) {
	if fps == 0 {
		return defaultFPS, nil
	}
	if fps < minFPS || math.IsNaN(fps) || math.IsInf(fps, 0) || fps > maxFPS {
		return 0, fmt.Errorf("fps must be between %.1f and %d", minFPS, maxFPS)
	}
	return fps, nil
}

// Frames returns latest-frame notifications. It is never closed; stop by
// cancelling the context and calling Stop.
func (s *Stream) Frames() <-chan image.Image {
	if s == nil {
		return nil
	}
	return s.frames
}

// SetViewportSize updates the expected pane viewport after a resize. Chromium
// can emit intermediate surfaces during resize and full-page screenshots;
// those must never temporarily replace the visible pane with a scaled sliver.
func (s *Stream) SetViewportSize(width, height int) {
	s.mu.Lock()
	s.width, s.height = width, height
	s.mu.Unlock()
}

// SetCellSize updates the terminal-cell extent of the image placement. It is
// independent of the raster dimensions, so custom-size screenshots still fill
// the pane exactly. Custom encoders and non-Kitty fallbacks are unaffected.
func (s *Stream) SetCellSize(cols, rows int) {
	if s == nil {
		return
	}
	s.encMu.Lock()
	if s.kitty != nil {
		s.kitty.Cols, s.kitty.Rows = cols, rows
	}
	s.encMu.Unlock()
}

func (s *Stream) matchesViewport(width, height int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return width == s.width && height == s.height
}

// Draw replaces the page image in place. Do not delete the old placement first:
// the host must keep displaying it while the next frame is being transmitted.
func (s *Stream) Draw(w io.Writer, img image.Image) error {
	if s == nil || img == nil {
		return nil
	}
	s.encMu.Lock()
	defer s.encMu.Unlock()
	started := time.Now()
	out := frameWriter{Writer: w}
	err := s.enc.Encode(&out, img)
	elapsed := time.Since(started)
	s.stats.drawLast.Store(int64(elapsed))
	s.stats.drawTotal.Add(int64(elapsed))
	s.stats.wireBytes.Add(uint64(out.bytes))
	if err == nil {
		s.stats.drawn.Add(1)
	}
	return err
}

type frameWriter struct {
	io.Writer
	bytes int
}

func (w *frameWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	w.bytes += n
	return n, err
}

// Clear removes only this browser's Kitty image, for mode changes and exit.
// Normal frame updates must never call Clear.
func (s *Stream) Clear(w io.Writer) error {
	_, err := io.WriteString(w, "\x1b_Ga=d,d=I,i=1,q=2;\x1b\\")
	return err
}

// Err returns the first transport/decode error.
func (s *Stream) Err() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// LastFrame reports when the last changed screencast frame arrived. Repeated
// frames caused by screenshot capture do not count as fresh page activity.
func (s *Stream) LastFrame() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// Stats returns a race-free snapshot of pipeline activity.
func (s *Stream) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	return Stats{
		Received: s.stats.received.Load(), Acked: s.stats.acked.Load(),
		Decoded: s.stats.decoded.Load(), Dropped: s.stats.dropped.Load(),
		Published:   s.stats.published.Load(),
		DecodeTotal: time.Duration(s.stats.decodeTotal.Load()),
		DecodeLast:  time.Duration(s.stats.decodeLast.Load()),
		Drawn:       s.stats.drawn.Load(), WireBytes: s.stats.wireBytes.Load(),
		DrawTotal: time.Duration(s.stats.drawTotal.Load()),
		DrawLast:  time.Duration(s.stats.drawLast.Load()),
	}
}

// Still captures a lossless PNG of the current viewport. It is used after the
// page has been idle, where JPEG ringing makes small text look soft.
func (s *Stream) Still(ctx context.Context) (image.Image, error) {
	if s == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Only the stream owns the CDP target. Callers may pass an ordinary
	// cancellation context (the UI does), which has no chromedp context value.
	callCtx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	endCapture := captureguard.Begin(s.target)
	defer cancel()
	defer endCapture()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	res, err := chromedp.Call(callCtx, page.CaptureScreenshot, page.CaptureScreenshotParams{
		Format: page.CaptureScreenshotFormatPng,
	})
	if err != nil {
		return nil, fmt.Errorf("still screenshot: %w", err)
	}
	img, err := png.Decode(bytes.NewReader(res.Data))
	if err != nil {
		return nil, fmt.Errorf("decode still: %w", err)
	}
	return img, nil
}

// Stop stops the screencast. It is safe to call more than once.
func (s *Stream) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
	if s.ctx.Err() == nil {
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		defer cancel()
		_, _ = chromedp.Call(ctx, page.StopScreencast, cdp.Empty{})
	}
}

func (s *Stream) receive(ctx context.Context, frames iter.Seq2[page.EventScreencastFrame, error]) {
	defer s.wg.Done()
	defer close(s.raw)
	for ev, err := range frames {
		if err != nil {
			if ctx.Err() == nil {
				s.fail(fmt.Errorf("screencast: %w", err))
			}
			return
		}
		s.stats.received.Add(1)
		if _, err := chromedp.Call(ctx, page.ScreencastFrameAck, page.ScreencastFrameAckParams{
			SessionID: ev.SessionID,
		}); err != nil {
			if ctx.Err() == nil {
				s.fail(fmt.Errorf("ack frame: %w", err))
			}
			return
		}
		s.stats.acked.Add(1)
		// ACK before doing any image work. offerRaw is non-blocking and retains
		// only the newest JPEG while the decoder is busy or rate limited.
		s.offerRaw(ctx, rawFrame{data: ev.Data, arrived: time.Now()})
	}
}

func (s *Stream) decode(ctx context.Context) {
	defer s.wg.Done()
	interval := time.Duration(float64(time.Second) / s.fps)
	var previous [sha256.Size]byte
	var havePrevious bool
	var next time.Time
	raw := s.raw
	for {
		if raw == nil {
			return
		}
		var frame rawFrame
		var ok bool
		select {
		case <-ctx.Done():
			return
		case frame, ok = <-raw:
		}
		if !ok {
			return
		}
		if wait := time.Until(next); wait > 0 {
			timer := time.NewTimer(wait)
		waiting:
			for {
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case newer, open := <-raw:
					if open {
						frame = newer
						s.stats.dropped.Add(1)
					} else {
						raw = nil
					}
				case <-timer.C:
					break waiting
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		started := time.Now()
		next = started.Add(interval)
		digest := sha256.Sum256(frame.data)
		if havePrevious && digest == previous {
			s.stats.dropped.Add(1)
			continue
		}
		if captureguard.Active(s.target) {
			s.stats.dropped.Add(1)
			continue
		}
		size, err := jpeg.DecodeConfig(bytes.NewReader(frame.data))
		if err != nil {
			s.fail(fmt.Errorf("decode frame header: %w", err))
			s.stats.dropped.Add(1)
			continue
		}
		if !s.matchesViewport(size.Width, size.Height) {
			s.stats.dropped.Add(1)
			continue
		}
		img, err := jpeg.Decode(bytes.NewReader(frame.data))
		if err != nil {
			s.fail(fmt.Errorf("decode frame: %w", err))
			s.stats.dropped.Add(1)
			continue
		}
		elapsed := time.Since(started)
		s.stats.decoded.Add(1)
		s.stats.decodeLast.Store(int64(elapsed))
		s.stats.decodeTotal.Add(int64(elapsed))
		previous, havePrevious = digest, true
		s.mu.Lock()
		s.last = frame.arrived
		s.mu.Unlock()
		s.publish(ctx, img)
		if raw == nil {
			return
		}
	}
}

func (s *Stream) offerRaw(ctx context.Context, frame rawFrame) {
	for {
		select {
		case s.raw <- frame:
			return
		case <-ctx.Done():
			return
		default:
			select {
			case <-s.raw:
				s.stats.dropped.Add(1)
			default:
			}
		}
	}
}

func (s *Stream) publish(ctx context.Context, img image.Image) {
	for {
		select {
		case s.frames <- img:
			s.stats.published.Add(1)
			return
		case <-ctx.Done():
			return
		default:
			select {
			case <-s.frames:
				s.stats.dropped.Add(1)
			default:
			}
		}
	}
}

func (s *Stream) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

// ImageID is a stable hex id for diagnostics (not used in the wire protocol
// yet). Keeping it here makes frame logs deterministic when debugging.
func ImageID(img image.Image) string {
	if img == nil {
		return ""
	}
	b := img.Bounds()
	return hex.EncodeToString([]byte(fmt.Sprintf("%dx%d", b.Dx(), b.Dy())))
}
