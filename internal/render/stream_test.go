package render

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math"
	"strings"
	"testing"
	"time"
)

type fakeEncoder struct {
	available bool
	payload   string
}

func (f fakeEncoder) Available() bool { return f.available }
func (f fakeEncoder) Encode(w io.Writer, _ image.Image) error {
	_, err := io.WriteString(w, f.payload)
	return err
}

func TestDrawDoesNotDeleteVisiblePlacement(t *testing.T) {
	var out bytes.Buffer
	s := &Stream{enc: fakeEncoder{available: true, payload: "FRAME"}}
	if err := s.Draw(&out, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if got != "FRAME" {
		t.Fatalf("draw = %q", got)
	}
	if stats := s.Stats(); stats.Drawn != 1 || stats.WireBytes != 5 || stats.DrawTotal < 0 {
		t.Fatalf("draw stats = %+v", stats)
	}
}

func TestClearTargetsOnlyBrowserImage(t *testing.T) {
	var out bytes.Buffer
	if err := (&Stream{}).Clear(&out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "d=I,i=1") || strings.Contains(got, "d=A") {
		t.Fatalf("clear = %q", got)
	}
}

func TestDrawIgnoresNilImage(t *testing.T) {
	s := &Stream{enc: fakeEncoder{available: true, payload: "FRAME"}}
	if err := s.Draw(io.Discard, nil); err != nil {
		t.Fatal(err)
	}
}

func TestStillRespectsCancelledCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Stream{ctx: context.Background()}
	if _, err := s.Still(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestViewportRejectsScreenshotAndResizeSurfaces(t *testing.T) {
	s := &Stream{}
	s.SetViewportSize(856, 816)
	if !s.matchesViewport(856, 816) || s.matchesViewport(127, 816) || s.matchesViewport(856, 5492) {
		t.Fatal("full-page screenshot surfaces must not become pane frames")
	}
	s.SetViewportSize(640, 480)
	if s.matchesViewport(856, 816) || !s.matchesViewport(640, 480) {
		t.Fatal("resize must update the accepted viewport")
	}
}

func TestSetCellSizeUpdatesKittyPlacement(t *testing.T) {
	k := &kittyEncoder{Cols: 1, Rows: 1}
	s := &Stream{enc: k, kitty: k}
	s.SetCellSize(107, 50)
	var out bytes.Buffer
	if err := s.Draw(&out, image.NewRGBA(image.Rect(0, 0, 8, 18))); err != nil {
		t.Fatal(err)
	}
	header, _, ok := strings.Cut(strings.TrimPrefix(out.String(), "\x1b_G"), ";")
	if !ok || !strings.Contains(header, "c=107") || !strings.Contains(header, "r=50") {
		t.Fatalf("Kitty header = %q", header)
	}
}

func TestSetCellSizeLeavesCustomEncoderAlone(t *testing.T) {
	s := &Stream{enc: fakeEncoder{available: true, payload: "FRAME"}}
	s.SetCellSize(80, 24)
	var out bytes.Buffer
	if err := s.Draw(&out, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil || out.String() != "FRAME" {
		t.Fatalf("custom draw = %q, %v", out.String(), err)
	}
}

func TestNormalizeFPS(t *testing.T) {
	if got, err := normalizeFPS(0); err != nil || got != 24 {
		t.Fatalf("default fps = %v, %v", got, err)
	}
	if got, err := normalizeFPS(23.976); err != nil || got != 23.976 {
		t.Fatalf("fractional fps = %v, %v", got, err)
	}
	for _, fps := range []float64{-1, 0.01, math.NaN(), math.Inf(1), 241} {
		if _, err := normalizeFPS(fps); err == nil {
			t.Errorf("normalizeFPS(%v) unexpectedly succeeded", fps)
		}
	}
}

func jpegFrame(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, c)
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func decodeTestStream(t *testing.T, fps float64) (*Stream, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Stream{
		frames: make(chan image.Image, 1), raw: make(chan rawFrame, 1),
		fps: fps, width: 8, height: 8,
	}
	s.wg.Add(1)
	go s.decode(ctx)
	t.Cleanup(func() {
		cancel()
		s.wg.Wait()
	})
	return s, ctx, cancel
}

func receiveFrame(t *testing.T, frames <-chan image.Image) image.Image {
	t.Helper()
	select {
	case img := <-frames:
		return img
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for decoded frame")
		return nil
	}
}

func mostlyBlue(img image.Image) bool {
	r, g, b, _ := img.At(4, 4).RGBA()
	return b > 4*r && b > 4*g
}

func TestDecodeBurstCoalescesAndDeliversFinalFrame(t *testing.T) {
	burst := make([][]byte, 50)
	for i := range burst {
		c := color.RGBA{R: uint8(i + 1), G: uint8(200 - i), A: 255}
		if i == len(burst)-1 {
			c = color.RGBA{B: 255, A: 255}
		}
		burst[i] = jpegFrame(t, c)
	}
	s, ctx, _ := decodeTestStream(t, 4)
	s.offerRaw(ctx, rawFrame{data: jpegFrame(t, color.RGBA{R: 255, A: 255}), arrived: time.Now()})
	receiveFrame(t, s.frames) // Establish the cadence before the burst.

	for _, data := range burst {
		s.offerRaw(ctx, rawFrame{data: data, arrived: time.Now()})
	}
	if img := receiveFrame(t, s.frames); !mostlyBlue(img) {
		t.Fatal("the final frame in the burst was not delivered")
	}
	stats := s.Stats()
	if stats.Decoded != 2 || stats.Dropped < 49 || stats.Published != 2 {
		t.Fatalf("unexpected burst stats: %+v", stats)
	}
}

func TestSlowConsumerKeepsOnlyLatestDecodedFrame(t *testing.T) {
	s, ctx, _ := decodeTestStream(t, 100)
	frames := [][]byte{
		jpegFrame(t, color.RGBA{R: 255, A: 255}),
		jpegFrame(t, color.RGBA{G: 255, A: 255}),
		jpegFrame(t, color.RGBA{B: 255, A: 255}),
	}
	for i, data := range frames {
		s.offerRaw(ctx, rawFrame{data: data, arrived: time.Now()})
		deadline := time.Now().Add(time.Second)
		for s.Stats().Decoded < uint64(i+1) && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	if img := receiveFrame(t, s.frames); !mostlyBlue(img) {
		t.Fatal("slow consumer received a stale decoded frame")
	}
	if got := s.Stats(); got.Published != 3 || got.Dropped < 2 {
		t.Fatalf("unexpected slow-consumer stats: %+v", got)
	}
}

func TestDuplicateDoesNotRefreshLastFrame(t *testing.T) {
	s, ctx, _ := decodeTestStream(t, 100)
	data := jpegFrame(t, color.RGBA{R: 255, A: 255})
	firstArrival := time.Now().Add(-time.Second)
	s.offerRaw(ctx, rawFrame{data: data, arrived: firstArrival})
	receiveFrame(t, s.frames)
	s.offerRaw(ctx, rawFrame{data: data, arrived: time.Now()})
	time.Sleep(30 * time.Millisecond)
	if got := s.LastFrame(); !got.Equal(firstArrival) {
		t.Fatalf("duplicate refreshed LastFrame: got %v, want %v", got, firstArrival)
	}
	if got := s.Stats(); got.Decoded != 1 {
		t.Fatalf("duplicate was decoded: %+v", got)
	}
}

func TestClosedInputDeliversThrottledFinalFrameAndExits(t *testing.T) {
	ctx := context.Background()
	s := &Stream{
		frames: make(chan image.Image, 1), raw: make(chan rawFrame, 1),
		fps: 10, width: 8, height: 8,
	}
	s.wg.Add(1)
	go s.decode(ctx)
	s.raw <- rawFrame{data: jpegFrame(t, color.RGBA{R: 255, A: 255}), arrived: time.Now()}
	receiveFrame(t, s.frames)
	s.raw <- rawFrame{data: jpegFrame(t, color.RGBA{B: 255, A: 255}), arrived: time.Now()}
	close(s.raw)
	if img := receiveFrame(t, s.frames); !mostlyBlue(img) {
		t.Fatal("closed pipeline lost its final frame")
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("decoder did not exit after delivering final frame")
	}
}

func TestDecodeCancelExitsPromptly(t *testing.T) {
	s, ctx, cancel := decodeTestStream(t, 0.5)
	s.offerRaw(ctx, rawFrame{data: jpegFrame(t, color.RGBA{R: 255, A: 255}), arrived: time.Now()})
	receiveFrame(t, s.frames)
	s.offerRaw(ctx, rawFrame{data: jpegFrame(t, color.RGBA{B: 255, A: 255}), arrived: time.Now()})
	time.Sleep(10 * time.Millisecond) // The decoder is now in its two-second cadence wait.
	cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("cancel did not interrupt decoder promptly")
	}
}
