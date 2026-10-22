//go:build !unix

package control

import (
	"context"
	"errors"
)

var errUnixSocketsUnsupported = errors.New("control sockets are unsupported on this platform")

// Start is unavailable on platforms without Unix-domain socket support.
func Start(socketPath string, capture CaptureFunc) (*Server, error) {
	return nil, errUnixSocketsUnsupported
}

// Capture is unavailable on platforms without Unix-domain socket support.
func Capture(ctx context.Context, socketPath string, fullPage bool) ([]byte, error) {
	return nil, errUnixSocketsUnsupported
}
