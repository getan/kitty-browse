package term

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"
)

// Geometry maps terminal cells to browser CSS pixels.
type Geometry struct {
	Cols, Rows      int
	CellWidth       int
	CellHeight      int
	ContentTopRow   int // 1-based terminal row where the page starts
	ContentWidthPx  int
	ContentHeightPx int
}

// CellToPixel maps a 1-based terminal cell to the page viewport. Returns false
// when the point is outside the content area.
func (g Geometry) CellToPixel(col, row int) (x, y float64, ok bool) {
	if col < 1 || row < g.ContentTopRow || g.CellWidth <= 0 || g.CellHeight <= 0 {
		return 0, 0, false
	}
	x = float64(col-1) * float64(g.CellWidth)
	y = float64(row-g.ContentTopRow) * float64(g.CellHeight)
	if x < 0 || y < 0 || x >= float64(g.ContentWidthPx) || y >= float64(g.ContentHeightPx) {
		return 0, 0, false
	}
	return x, y, true
}

// CellPixels is the result of the CSI 14t/16t query.
type CellPixels struct {
	WidthPx  int
	HeightPx int
	CellW    int
	CellH    int
}

// QueryCellPixels asks the terminal for its text-area and cell size. It is
// best-effort: a terminal that does not answer gets the 8x16 fallback.
func QueryCellPixels(in *os.File, out interface{ Write([]byte) (int, error) }, rows, cols int) CellPixels {
	fallback := CellPixels{WidthPx: cols * 8, HeightPx: rows * 16, CellW: 8, CellH: 16}
	if in == nil || out == nil || rows <= 0 || cols <= 0 {
		return fallback
	}
	_, _ = out.Write([]byte("\x1b[14t\x1b[16t"))
	if err := in.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		return fallback
	}
	buf := make([]byte, 256)
	n, err := in.Read(buf)
	_ = in.SetReadDeadline(time.Time{})
	if err != nil || n == 0 {
		return fallback
	}
	p, ok := ParseCellPixels(string(buf[:n]))
	if !ok {
		return fallback
	}
	if p.CellW <= 0 || p.CellH <= 0 {
		p.CellW, p.CellH = 8, 16
	}
	return p
}

var csiPixelRE = regexp.MustCompile(`\x1b\[(?:4|6);(\d+);(\d+)t`)

// ParseCellPixels parses one or both of CSI 4;height;width t (text area) and
// CSI 6;height;width t (cell size).
func ParseCellPixels(s string) (CellPixels, bool) {
	matches := csiPixelRE.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return CellPixels{}, false
	}
	var p CellPixels
	for _, m := range matches {
		h, _ := strconv.Atoi(m[1])
		w, _ := strconv.Atoi(m[2])
		if len(m[0]) > 2 && m[0][2] == '4' {
			p.WidthPx, p.HeightPx = w, h
		} else {
			p.CellW, p.CellH = w, h
		}
	}
	if p.WidthPx <= 0 || p.HeightPx <= 0 {
		return CellPixels{}, false
	}
	return p, true
}

// GeometryFor builds the mapping for a terminal grid and optional pixel
// measurements.
func GeometryFor(rows, cols int, p CellPixels) Geometry {
	if p.CellW <= 0 {
		p.CellW = 8
	}
	if p.CellH <= 0 {
		p.CellH = 16
	}
	if p.WidthPx <= 0 {
		p.WidthPx = cols * p.CellW
	}
	if p.HeightPx <= 0 {
		p.HeightPx = rows * p.CellH
	}
	contentRows := rows - 1
	if contentRows < 1 {
		contentRows = 1
	}
	return Geometry{
		Cols: cols, Rows: rows, CellWidth: p.CellW, CellHeight: p.CellH,
		ContentTopRow: 2, ContentWidthPx: p.WidthPx,
		ContentHeightPx: contentRows * p.CellH,
	}
}

func (g Geometry) String() string {
	return fmt.Sprintf("%dx%d cells, %dx%d px", g.Cols, g.Rows, g.ContentWidthPx, g.ContentHeightPx)
}
