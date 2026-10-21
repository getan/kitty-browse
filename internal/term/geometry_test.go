package term

import "testing"

func TestParseCellPixels(t *testing.T) {
	p, ok := ParseCellPixels("\x1b[4;800;1200t\x1b[6;16;8t")
	if !ok {
		t.Fatal("not parsed")
	}
	if p.WidthPx != 1200 || p.HeightPx != 800 || p.CellW != 8 || p.CellH != 16 {
		t.Fatalf("pixels = %+v", p)
	}
}

func TestCellToPixel(t *testing.T) {
	g := GeometryFor(41, 100, CellPixels{WidthPx: 1200, HeightPx: 800, CellW: 8, CellH: 16})
	x, y, ok := g.CellToPixel(1, 2)
	if !ok || x != 0 || y != 0 {
		t.Fatalf("top-left = %v,%v,%v", x, y, ok)
	}
	x, y, ok = g.CellToPixel(10, 5)
	if !ok || x != 72 || y != 48 {
		t.Fatalf("10,5 = %v,%v,%v", x, y, ok)
	}
	if _, _, ok := g.CellToPixel(1, 1); ok {
		t.Fatal("header row must not map into the page")
	}
	if _, _, ok := g.CellToPixel(999, 2); ok {
		t.Fatal("outside content must not map")
	}
	if g.ContentHeightPx != (41-2)*16 {
		t.Fatalf("content height = %d, want %d", g.ContentHeightPx, (41-2)*16)
	}
}
