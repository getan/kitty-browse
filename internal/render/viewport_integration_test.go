package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/getan/kitty-browse/internal/browser"
)

// Exercise the actual multi-tab startup path and the pixel dimensions observed
// in terminal panes. Reading DOM text is not proof that the compositor produces
// pixels: require a painted frame AND a PNG capture of that same target.
func TestChromiumTabFramesAcrossViewportSizes(t *testing.T) {
	executable := os.Getenv("KITTY_BROWSE_CHROME")
	if executable == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run Chromium integration tests")
	}
	path, err := filepath.Abs("testdata/page.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{640, 480}, {1078, 384}, {1166, 624}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
			opts = append(opts, chromedp.ExecPath(executable))
			alloc, closeAlloc := chromedp.NewExecAllocator(ctx, opts...)
			defer closeAlloc()
			tabs, err := browser.OpenTabs(alloc, "file://"+filepath.ToSlash(path), 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer tabs.Close()
			sess := tabs.Active()
			if err := sess.SetViewport(size[0], size[1]); err != nil {
				t.Fatal(err)
			}
			stream, err := Start(sess.Context(), Config{
				Quality: 85, Width: size[0], Height: size[1], Encoder: fakeEncoder{available: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Stop()
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
		painted:
			for {
				select {
				case img := <-stream.Frames():
					if img.Bounds().Dx() != size[0] || img.Bounds().Dy() != size[1] {
						continue // a frame already in flight before the resize
					}
					// The fixture has a blue background. Reject an empty initial
					// frame even if its dimensions happen to match the viewport.
					r, g, b, _ := img.At(0, 0).RGBA()
					if b > r && b > g {
						t.Logf("painted screencast: %v", img.Bounds())
						break painted
					}
				case <-timer.C:
					t.Fatalf("no painted frame: err=%v stats=%+v", stream.Err(), stream.Stats())
				}
			}
			img, err := stream.Still(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if img.Bounds().Dx() != size[0] || img.Bounds().Dy() != size[1] {
				t.Fatalf("PNG bounds=%v, want %dx%d", img.Bounds(), size[0], size[1])
			}
			t.Logf("PNG still: %v", img.Bounds())
		})
	}
}

// Recovery restarts image transport on the same target, not the page/browser.
// In particular, unsaved JS state must survive both allowed recovery attempts.
func TestChromiumStreamRestartPreservesDocument(t *testing.T) {
	executable := os.Getenv("KITTY_BROWSE_CHROME")
	if executable == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run Chromium integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(executable))
	alloc, closeAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer closeAlloc()
	path, err := filepath.Abs("testdata/page.html")
	if err != nil {
		t.Fatal(err)
	}
	tabs, err := browser.OpenTabs(alloc, "file://"+filepath.ToSlash(path), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tabs.Close()
	sess := tabs.Active()
	if err := sess.SetViewport(640, 480); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Run(sess.Context(), chromedp.Evaluate[any](`window.unsavedRenderTest = "keep me"`)); err != nil {
		t.Fatal(err)
	}
	id := sess.TargetID()
	for attempt := 0; attempt < 3; attempt++ { // initial stream + two recoveries
		s, err := Start(sess.Context(), Config{Quality: 85, Width: 640, Height: 480, Encoder: fakeEncoder{available: true}})
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer s.Stop() // must precede the next Start on this target
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			for {
				select {
				case img := <-s.Frames():
					if isTestFrame(img) {
						return
					}
				case <-timer.C:
					t.Fatalf("restart %d did not produce a painted frame: %+v", attempt, s.Stats())
				}
			}
		}()
		value, err := chromedp.Run(sess.Context(), chromedp.Evaluate[string](`window.unsavedRenderTest`))
		if err != nil || value != "keep me" || sess.TargetID() != id {
			t.Fatalf("recovery changed the document: target=%s state=%q err=%v", sess.TargetID(), value, err)
		}
	}
}
