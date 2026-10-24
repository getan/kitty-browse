//go:build unix

package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Start creates a Unix-domain socket screenshot server. Existing filesystem
// entries are never removed or replaced.
func Start(socketPath string, capture CaptureFunc, opts ...Option) (*Server, error) {
	if socketPath == "" {
		return nil, errors.New("control socket path is empty")
	}
	if capture == nil {
		return nil, errors.New("capture function is nil")
	}
	var config serverConfig
	for _, option := range opts {
		if option == nil {
			return nil, errors.New("control option is nil")
		}
		option(&config)
	}
	if err := ensureParent(socketPath); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(socketPath); err == nil {
		return nil, fmt.Errorf("control socket path already exists: %s", socketPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect control socket path: %w", err)
	}

	addr := &net.UnixAddr{Name: socketPath, Net: "unix"}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on control socket: %w", err)
	}
	// Manage unlinking ourselves so Close cannot remove a replacement entry.
	listener.SetUnlinkOnClose(false)
	info, err := os.Lstat(socketPath)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("inspect new control socket: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = listener.Close()
			current, statErr := os.Lstat(socketPath)
			if statErr == nil && os.SameFile(info, current) {
				_ = os.Remove(socketPath)
			}
		}
	}()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return nil, fmt.Errorf("secure control socket: %w", err)
	}
	current, err := os.Lstat(socketPath)
	if err != nil {
		return nil, fmt.Errorf("verify new control socket: %w", err)
	}
	if !os.SameFile(info, current) {
		return nil, errors.New("control socket path changed during startup")
	}

	s := newServer(socketPath, listener, info, capture, config)
	ok = true
	return s, nil
}

func ensureParent(socketPath string) error {
	parent := filepath.Dir(socketPath)
	if info, err := os.Stat(parent); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("control socket parent is not a directory: %s", parent)
		}
		return nil // Preserve permissions on an existing directory.
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect control socket parent: %w", err)
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create control socket parent: %w", err)
	}
	// MkdirAll is affected by umask. The newly requested parent itself is
	// private by contract; existing ancestors are deliberately left alone.
	if err := os.Chmod(parent, 0o700); err != nil {
		return fmt.Errorf("secure control socket parent: %w", err)
	}
	return nil
}

// Capture requests a screenshot through socketPath.
func Capture(ctx context.Context, socketPath string, fullPage bool) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("capture context is nil")
	}
	if socketPath == "" {
		return nil, errors.New("control socket path is empty")
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		ResponseHeaderTimeout: 17 * time.Second,
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(dialCtx, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: clientTimeout}
	endpoint := "http://unix/screenshot"
	if fullPage {
		endpoint += "?fullPage=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create screenshot request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request screenshot: %w", err)
	}
	defer resp.Body.Close()
	return readCaptureResponse(resp)
}

// Info requests browser information through socketPath.
func Info(ctx context.Context, socketPath string) (json.RawMessage, error) {
	return requestJSON(ctx, socketPath, http.MethodGet, "/v1/info", nil)
}

// Action sends an action request through socketPath. A valid JSON response is
// returned even when its HTTP status indicates an API error, so callers can
// inspect the API error envelope.
func Action(ctx context.Context, socketPath string, request json.RawMessage) (json.RawMessage, error) {
	return requestJSON(ctx, socketPath, http.MethodPost, "/v1/actions", request)
}

// Quit asks the browser on socketPath to shut down gracefully. The reply is the
// last document the socket serves: the host tears itself down only after the
// answer has been flushed.
func Quit(ctx context.Context, socketPath string) error {
	raw, err := requestJSON(ctx, socketPath, http.MethodPost, "/v1/quit", nil)
	if err != nil {
		return err
	}
	var response struct {
		OK     bool `json:"ok"`
		Result struct {
			Quitting bool `json:"quitting"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("control API returned an invalid quit response: %w", err)
	}
	if !response.OK || !response.Result.Quitting {
		return errors.New("control API did not confirm shutdown")
	}
	return nil
}

func requestJSON(ctx context.Context, socketPath, method, path string, body json.RawMessage) (json.RawMessage, error) {
	if ctx == nil {
		return nil, errors.New("control context is nil")
	}
	if socketPath == "" {
		return nil, errors.New("control socket path is empty")
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		ResponseHeaderTimeout: apiTimeout + 2*time.Second,
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(dialCtx, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: apiClientTimeout}
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, reader)
	if err != nil {
		return nil, fmt.Errorf("create control API request: %w", err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request control API: %w", err)
	}
	defer resp.Body.Close()
	return readJSONResponse(resp)
}

func readJSONResponse(resp *http.Response) (json.RawMessage, error) {
	if resp.ContentLength > maxJSONSize {
		return nil, fmt.Errorf("control API response exceeds %d bytes", maxJSONSize)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONSize+1))
	if err != nil {
		return nil, fmt.Errorf("read control API response: %w", err)
	}
	if len(body) > maxJSONSize {
		return nil, fmt.Errorf("control API response exceeds %d bytes", maxJSONSize)
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("control API returned %s with an invalid JSON body", resp.Status)
	}
	return json.RawMessage(body), nil
}
