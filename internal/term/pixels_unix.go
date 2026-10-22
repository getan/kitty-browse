//go:build darwin || linux

package term

import (
	"os"

	"golang.org/x/sys/unix"
)

func ioctlCellPixels(in *os.File, rows, cols int) (CellPixels, bool) {
	if rows <= 0 || cols <= 0 {
		return CellPixels{}, false
	}
	ws, err := unix.IoctlGetWinsize(int(in.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return CellPixels{}, false
	}
	p := CellPixels{WidthPx: int(ws.Xpixel), HeightPx: int(ws.Ypixel)}
	p.CellW = p.WidthPx / cols
	p.CellH = p.HeightPx / rows
	if p.CellW <= 0 || p.CellH <= 0 {
		return CellPixels{}, false
	}
	return p, true
}
