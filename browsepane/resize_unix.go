//go:build unix

package browsepane

import (
	"os"
	"syscall"
)

// resizeSignals is the terminal resize notification this platform delivers.
// Run subscribes only for the duration of one pane and stops the subscription
// with signal.Stop before it returns, so an embedding process keeps its own
// signal handling.
func resizeSignals() []os.Signal {
	return []os.Signal{syscall.SIGWINCH}
}
