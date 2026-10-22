//go:build darwin || linux

package browser

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func acquireProfileLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("profile is already in use")
		}
		return nil, fmt.Errorf("acquire advisory lock: %w", err)
	}
	return file, nil
}

func releaseProfileLock(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return fmt.Errorf("release advisory lock: %w", unlockErr)
	}
	return closeErr
}
