// Package screenshot validates and securely stores PNG screenshots.
package screenshot

import (
	"bytes"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// Info describes a saved screenshot.
type Info struct {
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
}

// Save validates data as PNG and creates path without replacing anything
// already there (including a symbolic link).
func Save(path string, data []byte) (Info, error) {
	var zero Info
	if !bytes.HasPrefix(data, pngSignature) {
		return zero, fmt.Errorf("save screenshot: data is not a PNG image")
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		if err == nil {
			err = fmt.Errorf("invalid image dimensions")
		}
		return zero, fmt.Errorf("save screenshot: invalid PNG: %w", err)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return zero, fmt.Errorf("save screenshot: resolve path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o700); err != nil {
		return zero, fmt.Errorf("save screenshot: create parent directory: %w", err)
	}

	file, err := os.OpenFile(absPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return zero, fmt.Errorf("save screenshot: create %s: %w", absPath, err)
	}
	created := true
	defer func() {
		if created {
			_ = os.Remove(absPath)
		}
	}()

	if _, err := io.Copy(file, bytes.NewReader(data)); err != nil {
		_ = file.Close()
		return zero, fmt.Errorf("save screenshot: write %s: %w", absPath, err)
	}
	if err := file.Close(); err != nil {
		return zero, fmt.Errorf("save screenshot: close %s: %w", absPath, err)
	}
	created = false
	return Info{Path: absPath, Width: config.Width, Height: config.Height, Bytes: len(data)}, nil
}

var lastAutoPathNanos atomic.Int64

// AutoPath returns a unique, sortable screenshot name beneath dir.
func AutoPath(dir string, fullPage bool) string {
	nanos := time.Now().UTC().UnixNano()
	for {
		previous := lastAutoPathNanos.Load()
		if nanos <= previous {
			nanos = previous + 1
		}
		if lastAutoPathNanos.CompareAndSwap(previous, nanos) {
			break
		}
	}
	stamp := time.Unix(0, nanos).UTC().Format("20060102T150405.000000000Z")
	suffix := ""
	if fullPage {
		suffix = "-full"
	}
	return filepath.Join(dir, "kitty-browse-"+stamp+suffix+".png")
}
