package browsepane

import (
	"io"
	"sync"
)

// A stopped writer acknowledges bytes without retaining or forwarding them.
// Chromium's diagnostic copy goroutine may outlive the startup boundary.
type startupOnlyWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *startupOnlyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writer == nil {
		return len(p), nil
	}
	return w.writer.Write(p)
}

func (w *startupOnlyWriter) stop() {
	w.mu.Lock()
	w.writer = nil
	w.mu.Unlock()
}
