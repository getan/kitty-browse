package main

import (
	"math"
	"testing"
	"time"

	"github.com/getan/kitty-browse/internal/term"
	"github.com/getan/kitty-browse/internal/ui"
)

func TestPanePixelsAndCustomResolution(t *testing.T) {
	p := mergeCellPixels(term.FallbackCellPixels(53, 107), term.CellPixels{CellW: 8, CellH: 18}, 53, 107)
	g := paneGeometry(53, 107, p, 0, 0)
	if g.ContentRows != 50 || g.ContentTopRow != 3 || g.ContentHeightPx != 900 || g.ContentWidthPx != 856 {
		t.Fatalf("measured pane = %+v", g)
	}
	g = paneGeometry(53, 107, p, 1280, 1000)
	x, y, ok := g.CellToPixel(107, 52)
	// Cell centres, not cell corners: the last cell lands at 1274,990 instead
	// of on the 1280,1000 image edge.
	if !ok || x >= 1280 || x < 1260 || y != 990 {
		t.Fatalf("stretched image cell centre = %v,%v,%v", x, y, ok)
	}
	if _, _, ok := g.CellToPixel(20, 53); ok {
		t.Fatal("footer must not click through to webpage")
	}
}

func TestFitViewportKeepsPaneAspect(t *testing.T) {
	width, height, ok := fitViewportSize(1105, 1110, 1250)
	if !ok || width != 1250 || height <= 1110 {
		t.Fatalf("fit = %d,%d,%v", width, height, ok)
	}
	if got, want := float64(width)/float64(height), 1105.0/1110.0; math.Abs(got-want) > 0.002 {
		t.Fatalf("aspect drifted: %dx%d", width, height)
	}
	if _, _, ok := fitViewportSize(1105, 1110, 1105); ok {
		t.Fatal("a page that fits the pane must not be widened")
	}
	if width, _, ok := fitViewportSize(1105, 1110, 5000); !ok || width != 1920 {
		t.Fatalf("cap = %d, %v", width, ok)
	}
}

func TestFractionalFrameCadenceAndScrollGain(t *testing.T) {
	fractionalFPS := 23.976
	if got, err := frameInterval(fractionalFPS); err != nil || got != time.Duration(float64(time.Second)/fractionalFPS) {
		t.Fatalf("fractional cadence = %v, %v", got, err)
	}
	for _, fps := range []float64{0, -1, math.NaN(), math.Inf(1), 241} {
		if _, err := frameInterval(fps); err == nil {
			t.Fatalf("invalid fps accepted: %v", fps)
		}
	}
	for _, lines := range []float64{0, -1, math.NaN(), math.Inf(1), 101} {
		if validScrollLines(lines) {
			t.Fatalf("invalid scroll gain accepted: %v", lines)
		}
	}
	g := paneGeometry(53, 107, term.CellPixels{CellW: 8, CellH: 18}, 0, 0)
	if got := scrollPixels(g, -10, 2); got != -360 {
		t.Fatalf("coalesced wheel gain = %v, want -360", got)
	}
	for _, kind := range []ui.ActionKind{ui.ActionNewTab, ui.ActionSelectTab, ui.ActionCloseTab, ui.ActionNavigate, ui.ActionReload} {
		if !interruptsScroll(kind) {
			t.Fatalf("target-changing action %v must discard old movement", kind)
		}
	}
}

func TestDebugURLRemovesCredentialsAndTokens(t *testing.T) {
	got := debugURL("https://user:password@example.com/callback?code=secret#token")
	if got != "https://example.com/callback" {
		t.Fatalf("debug URL leaked sensitive components: %q", got)
	}
}
