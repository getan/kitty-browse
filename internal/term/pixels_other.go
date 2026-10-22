//go:build !darwin && !linux

package term

import "os"

func ioctlCellPixels(_ *os.File, _, _ int) (CellPixels, bool) {
	return CellPixels{}, false
}
