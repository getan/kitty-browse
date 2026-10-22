//go:build !unix

package control

import (
	"context"
	"encoding/json"
	"errors"
)

var errUnixSocketsUnsupported = errors.New("control sockets are unsupported on this platform")

// Start is unavailable on platforms without Unix-domain socket support.
func Start(socketPath string, capture CaptureFunc, opts ...Option) (*Server, error) {
	return nil, errUnixSocketsUnsupported
}

// Capture is unavailable on platforms without Unix-domain socket support.
func Capture(ctx context.Context, socketPath string, fullPage bool) ([]byte, error) {
	return nil, errUnixSocketsUnsupported
}

// Info is unavailable on platforms without Unix-domain socket support.
func Info(ctx context.Context, socketPath string) (json.RawMessage, error) {
	return nil, errUnixSocketsUnsupported
}

// Action is unavailable on platforms without Unix-domain socket support.
func Action(ctx context.Context, socketPath string, request json.RawMessage) (json.RawMessage, error) {
	return nil, errUnixSocketsUnsupported
}
