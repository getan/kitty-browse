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
	ContentRows     int // number of terminal rows occupied by the page image
	ContentWidthPx  int
	ContentHeightPx int
}

// CellToPixel maps a 1-based terminal cell to the page viewport. Returns false
// when the point is outside the content area.
func (g Geometry) CellToPixel(col, row int) (x, y float64, ok bool) {
	if col < 1 || col > g.Cols || row < g.ContentTopRow ||
		row >= g.ContentTopRow+g.ContentRows || g.Cols <= 0 || g.ContentRows <= 0 ||
		g.ContentWidthPx <= 0 || g.ContentHeightPx <= 0 {
		return 0, 0, false
	}
	// The image may have been rendered at a custom size and stretched into its
	// cell placement. Map against the actual image extent, not an estimated cell.
	// A terminal mouse report identifies a cell, not a point inside it. Aim at
	// the cell centre: mapping to the top-left corner biases every click up and
	// left by up to a full cell (one text line), which misses small links and
	// list rows while large buttons still respond.
	x = (float64(col) - 0.5) * float64(g.ContentWidthPx) / float64(g.Cols)
	y = (float64(row-g.ContentTopRow) + 0.5) * float64(g.ContentHeightPx) / float64(g.ContentRows)
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
	fallback := FallbackCellPixels(rows, cols)
	if in == nil || out == nil || rows <= 0 || cols <= 0 {
		return fallback
	}
	_, _ = out.Write([]byte("\x1b[14t\x1b[16t"))
	if err := in.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		return fallback
	}
	buf := make([]byte, 256)
	var data []byte
	deadline := time.Now().Add(350 * time.Millisecond)
	for {
		_ = in.SetReadDeadline(deadline)
		n, err := in.Read(buf)
		if n > 0 {
			data = append(data, buf[:n]...)
			deadline = time.Now().Add(60 * time.Millisecond)
			continue
		}
		if err != nil {
			break
		}
	}
	_ = in.SetReadDeadline(time.Time{})
	if len(data) == 0 {
		return fallback
	}
	p, ok := ParseCellPixels(string(data))
	if !ok {
		return fallback
	}
	if p.CellW <= 0 || p.CellH <= 0 {
		p.CellW, p.CellH = 8, 16
	}
	return p
}

// FallbackCellPixels is the 8x16 estimate used when the terminal is not
// queried or does not answer.
func FallbackCellPixels(rows, cols int) CellPixels {
	return CellPixels{WidthPx: cols * 8, HeightPx: rows * 16, CellW: 8, CellH: 16}
}

// TerminalCellPixels obtains the kernel-reported terminal pixel extent when it
// is available. Pseudo terminals commonly leave it zero, in which case the
// conservative fallback is returned without reading stdin.
func TerminalCellPixels(in *os.File, rows, cols int) CellPixels {
	if in != nil {
		if p, ok := ioctlCellPixels(in, rows, cols); ok {
			return p
		}
	}
	return FallbackCellPixels(rows, cols)
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
	contentRows := rows - ChromeRows - 1
	if contentRows < 0 {
		contentRows = 0
	}
	contentHeight := 0
	if rows > 0 {
		contentHeight = p.HeightPx * contentRows / rows
	}
	return Geometry{
		Cols: cols, Rows: rows, CellWidth: p.CellW, CellHeight: p.CellH,
		ContentTopRow: ChromeRows + 1, ContentRows: contentRows,
		ContentWidthPx: p.WidthPx, ContentHeightPx: contentHeight,
	}
}

func (g Geometry) String() string {
	return fmt.Sprintf("%dx%d cells, %dx%d px", g.Cols, g.Rows, g.ContentWidthPx, g.ContentHeightPx)
}

// CellToPixelClamped maps a 1-based terminal cell to the page viewport, clamping
// cells outside the content area to its nearest edge. A drag keeps producing
// motion after the pointer leaves the page area, and the page expects the
// pointer to keep tracking along the edge instead of freezing.
func (g Geometry) CellToPixelClamped(col, row int) (x, y float64, ok bool) {
	if g.Cols <= 0 || g.ContentRows <= 0 || g.ContentWidthPx <= 0 || g.ContentHeightPx <= 0 {
		return 0, 0, false
	}
	return g.CellToPixel(
		min(max(col, 1), g.Cols),
		min(max(row, g.ContentTopRow), g.ContentTopRow+g.ContentRows-1),
	)
}
