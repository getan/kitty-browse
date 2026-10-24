//go:build !unix

package browsepane

import "os"

// resizeSignals is empty where the terminal does not report resizes as a
// signal; the pane then relies on the geometry it measured at startup.
func resizeSignals() []os.Signal { return nil }
