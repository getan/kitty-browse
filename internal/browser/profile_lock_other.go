//go:build !darwin && !linux

package browser

import (
	"fmt"
	"os"
)

func acquireProfileLock(path string) (*os.File, error) {
	return nil, fmt.Errorf("persistent profile locking is unsupported on this operating system")
}

func releaseProfileLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
