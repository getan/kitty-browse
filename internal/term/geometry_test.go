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

func TestCellToPixelAimsAtCellCentre(t *testing.T) {
	g := GeometryFor(41, 100, CellPixels{WidthPx: 1200, HeightPx: 800, CellW: 8, CellH: 16})
	// The custom 1200x800 raster is stretched over 100x38 cells. Mapping must
	// follow that ratio rather than the reported 8x16 physical cell estimate.
	g.ContentHeightPx = 800
	x, y, ok := g.CellToPixel(1, 3)
	if !ok || x != 6 || y != 400.0/38 {
		t.Fatalf("first cell centre = %v,%v,%v", x, y, ok)
	}
	x, y, ok = g.CellToPixel(10, 5)
	if !ok || x != 114 || y != 2000.0/38 {
		t.Fatalf("10,5 = %v,%v,%v", x, y, ok)
	}
	// The last cell's centre stays inside the image instead of mapping to the
	// right/bottom edge.
	if x, y, ok = g.CellToPixel(g.Cols, g.ContentTopRow+g.ContentRows-1); !ok ||
		x != 1194 || y >= float64(g.ContentHeightPx) {
		t.Fatalf("last cell centre = %v,%v,%v", x, y, ok)
	}
	if _, _, ok := g.CellToPixel(1, 2); ok {
		t.Fatal("chrome rows must not map into the page")
	}
	if _, _, ok := g.CellToPixel(999, 3); ok {
		t.Fatal("outside content must not map")
	}
	if _, _, ok := g.CellToPixel(1, 41); ok {
		t.Fatal("footer row must not map into the page")
	}
	if g.ContentRows != 38 || g.ContentTopRow != 3 {
		t.Fatalf("content cells = row %d + %d", g.ContentTopRow, g.ContentRows)
	}
}

func TestGeometryUsesMeasuredEightByEighteenCells(t *testing.T) {
	g := GeometryFor(50, 100, CellPixels{WidthPx: 800, HeightPx: 900, CellW: 8, CellH: 18})
	if g.ContentRows != 47 || g.ContentWidthPx != 800 || g.ContentHeightPx != 846 {
		t.Fatalf("geometry = %+v", g)
	}
}

func TestTerminalCellPixelsFallsBackWithoutTerminal(t *testing.T) {
	p := TerminalCellPixels(nil, 50, 100)
	if p != (CellPixels{WidthPx: 800, HeightPx: 800, CellW: 8, CellH: 16}) {
		t.Fatalf("fallback = %+v", p)
	}
}
