// Package render turns a Chromium screencast into terminal image frames. It is
// intentionally smaller than termcast: the image occupies rows 2..N so the
// address bar can stay on row 1.
package render

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"iter"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
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
	Encoder rasterm.Encoder
}

// Stream is a running CDP screencast. Frames contains the latest decoded
// viewport; the consumer can drop frames it cannot draw yet.
type Stream struct {
	ctx    context.Context
	cancel context.CancelFunc
	enc    rasterm.Encoder
	frames chan image.Image

	wg      sync.WaitGroup
	mu      sync.Mutex
	err     error
	stopped bool
}

// Start begins a screencast and starts Chromium if the context has none.
func Start(ctx context.Context, cfg Config) (*Stream, error) {
	if cfg.FPS <= 0 {
		cfg.FPS = 8
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
	if cfg.Encoder == nil {
		cfg.Encoder = rasterm.NewDefaultEncoder(
			rasterm.KittyEncoder{NoNewline: true},
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
		MaxWidth:      int64(cfg.Width),
		MaxHeight:     int64(cfg.Height),
		EveryNthFrame: 1,
	}); err != nil {
		cancel()
		return nil, fmt.Errorf("start screencast: %w", err)
	}

	s := &Stream{ctx: ctx, cancel: cancel, enc: cfg.Encoder, frames: make(chan image.Image, 1)}
	s.wg.Add(1)
	go s.read(rctx, frames)
	return s, nil
}

// Frames returns latest-frame notifications. It is never closed; stop by
// cancelling the context and calling Stop.
func (s *Stream) Frames() <-chan image.Image {
	if s == nil {
		return nil
	}
	return s.frames
}

// Draw clears the browser area and emits one image. The caller supplies the
// writer that is positioned at row 2.
func (s *Stream) Draw(w io.Writer, img image.Image) error {
	if s == nil || img == nil {
		return nil
	}
	// Reusing one image id is not enough on all terminals: delete the previous
	// placement first, then display the new frame. Text on row 1 is untouched.
	if _, err := io.WriteString(w, "\x1b_Ga=d,d=A,q=2;\x1b\\"); err != nil {
		return err
	}
	return s.enc.Encode(w, img)
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

func (s *Stream) read(ctx context.Context, frames iter.Seq2[page.EventScreencastFrame, error]) {
	defer s.wg.Done()
	for ev, err := range frames {
		if err != nil {
			if ctx.Err() == nil {
				s.fail(fmt.Errorf("screencast: %w", err))
			}
			return
		}
		if _, err := chromedp.Call(ctx, page.ScreencastFrameAck, page.ScreencastFrameAckParams{
			SessionID: ev.SessionID,
		}); err != nil {
			if ctx.Err() == nil {
				s.fail(fmt.Errorf("ack frame: %w", err))
			}
			return
		}
		img, err := jpeg.Decode(bytes.NewReader(ev.Data))
		if err != nil {
			s.fail(fmt.Errorf("decode frame: %w", err))
			continue
		}
		s.publish(ctx, img)
	}
}

func (s *Stream) publish(ctx context.Context, img image.Image) {
	for {
		select {
		case s.frames <- img:
			return
		case <-ctx.Done():
			return
		default:
			select {
			case <-s.frames:
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
