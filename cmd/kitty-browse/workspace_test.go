package main

import (
	"testing"

	"github.com/getan/kitty-browse/internal/term"
)

func TestPanePixelsAndCustomResolution(t *testing.T) {
	p := mergeCellPixels(term.FallbackCellPixels(53, 107), term.CellPixels{CellW: 8, CellH: 18}, 53, 107)
	g := paneGeometry(53, 107, p, 0, 0)
	if g.ContentRows != 50 || g.ContentTopRow != 3 || g.ContentHeightPx != 900 || g.ContentWidthPx != 856 {
		t.Fatalf("measured pane = %+v", g)
	}
	g = paneGeometry(53, 107, p, 1280, 1000)
	x, y, ok := g.CellToPixel(107, 52)
	if !ok || x >= 1280 || x < 1260 || y != 980 {
		t.Fatalf("stretched image mapping = %v,%v,%v", x, y, ok)
	}
	if _, _, ok := g.CellToPixel(20, 53); ok {
		t.Fatal("footer must not click through to webpage")
	}
}
