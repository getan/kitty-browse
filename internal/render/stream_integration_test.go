package render

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/getan/kitty-browse/internal/browser"
)

// Opt in with KITTY_BROWSE_CHROME=/absolute/path/to/Chromium. This exercises
// real CDP transport without a PTY, graphics-capable terminal or external site.
func TestChromiumFrames(t *testing.T) {
	executable := os.Getenv("KITTY_BROWSE_CHROME")
	if executable == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run the Chromium integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(executable))
	alloc, closeAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer closeAlloc()
	path, err := filepath.Abs("testdata/page.html")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := browser.Open(alloc, "file://"+filepath.ToSlash(path), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.SetViewport(640, 480); err != nil {
		t.Fatal(err)
	}
	s, err := Start(sess.Context(), Config{
		Quality: 85, Width: 640, Height: 480, Encoder: fakeEncoder{available: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
waitFrame:
	for {
		select {
		case img := <-s.Frames():
			// macOS can deliver one in-flight frame from before the viewport
			// resize, or before the compositor has painted the document.
			if !isTestFrame(img) {
				t.Logf("waiting for painted 640x480 viewport, received %v", img.Bounds())
				continue
			}
			t.Logf("screencast: %v", img.Bounds())
			break waitFrame
		case <-deadline.C:
			t.Fatalf("no painted screencast frame: %v", s.Err())
		}
	}
	// The caller controls cancellation, not which CDP target is captured.
	img, err := s.Still(ctx)
	if err != nil {
		t.Fatalf("still with non-chromedp caller context: %v", err)
	}
	checkTestFrame(t, img)
	t.Logf("PNG still: %v", img.Bounds())
	if _, err := sess.Screenshot(ctx, true); err != nil {
		t.Fatalf("full-page screenshot while streaming: %v", err)
	}
	settled := time.NewTimer(300 * time.Millisecond)
	defer settled.Stop()
checkCaptureFrames:
	for {
		select {
		case img := <-s.Frames():
			checkTestFrame(t, img)
		case <-settled.C:
			break checkCaptureFrames
		}
	}
	if title, err := sess.Title(sess.Context()); err != nil || title != "kitty-browse frame test" {
		t.Fatalf("session did not survive initial navigation: title=%q err=%v", title, err)
	}
}

func checkTestFrame(t *testing.T, img image.Image) {
	t.Helper()
	if !isTestFrame(img) {
		if img == nil {
			t.Fatal("nil frame")
		}
		t.Fatalf("frame %v does not contain the rendered 640x480 test page", img.Bounds())
	}
}

func isTestFrame(img image.Image) bool {
	if img == nil || img.Bounds().Dx() != 640 || img.Bounds().Dy() != 480 {
		return false
	}
	// Find the orange swatch, not a coordinate that moves when fonts wrap.
	for y := 0; y < 480; y += 4 {
		for x := 0; x < 640; x += 4 {
			r, g, b, _ := img.At(x, y).RGBA()
			if r>>8 > 220 && g>>8 > 140 && g>>8 < 200 && b>>8 < 90 {
				return true
			}
		}
	}
	return false
}
