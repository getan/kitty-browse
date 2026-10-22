// Package control provides a deliberately small, read-only control channel for
// taking screenshots from a running kitty-browse process.
package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	requestTimeout = 15 * time.Second
	clientTimeout  = 20 * time.Second
	maxPNGSize     = 128 << 20 // 128 MiB
	maxErrorSize   = 64 << 10
)

// CaptureFunc captures a PNG. fullPage is false for the current viewport and
// true for the complete page.
type CaptureFunc func(context.Context, bool) ([]byte, error)

// Server is a screenshot control server.
type Server struct {
	httpServer *http.Server
	listener   net.Listener
	socketPath string
	socketInfo os.FileInfo

	closeOnce sync.Once
	closeErr  error
}

func newServer(socketPath string, listener net.Listener, socketInfo os.FileInfo, capture CaptureFunc) *Server {
	s := &Server{
		listener:   listener,
		socketPath: socketPath,
		socketInfo: socketInfo,
	}
	s.httpServer = &http.Server{
		Handler:           screenshotHandler(capture),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() {
		// Errors are intentionally not logged: Close normally causes Serve to
		// return, and socket details must not end up in an ambient logger.
		_ = s.httpServer.Serve(listener)
	}()
	return s
}

func screenshotHandler(capture CaptureFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/screenshot" || r.URL.EscapedPath() != "/screenshot" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		fullPage, err := parseFullPage(r.URL.RawQuery)
		if err != nil {
			http.Error(w, "invalid query parameters", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		png, err := capture(ctx, fullPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", fmt.Sprint(len(png)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(png)
	})
}

func parseFullPage(rawQuery string) (bool, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return false, err
	}
	for key := range values {
		if key != "fullPage" {
			return false, errors.New("unknown query parameter")
		}
	}
	fullPageValues, present := values["fullPage"]
	if !present {
		return false, nil
	}
	if len(fullPageValues) != 1 {
		return false, errors.New("fullPage must occur once")
	}
	switch fullPageValues[0] {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("fullPage must be true or false")
	}
}

// Close stops the HTTP server and removes the socket created by Start. It is
// safe to call Close more than once.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		var errs []error
		if s.httpServer != nil {
			if err := s.httpServer.Close(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
				errs = append(errs, err)
			}
		}
		if s.listener != nil {
			if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, err)
			}
		}
		if s.socketPath != "" && s.socketInfo != nil {
			current, err := os.Lstat(s.socketPath)
			switch {
			case err == nil && os.SameFile(s.socketInfo, current):
				if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					errs = append(errs, err)
				}
			case err != nil && !errors.Is(err, os.ErrNotExist):
				errs = append(errs, err)
			}
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

func readCaptureResponse(resp *http.Response) ([]byte, error) {
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorSize+1))
		message := safeErrorText(body)
		if message == "" {
			return nil, fmt.Errorf("screenshot server returned %s", resp.Status)
		}
		return nil, fmt.Errorf("screenshot server returned %s: %s", resp.Status, message)
	}
	if resp.ContentLength > maxPNGSize {
		return nil, fmt.Errorf("screenshot response exceeds %d bytes", maxPNGSize)
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if contentType != "image/png" {
		return nil, fmt.Errorf("screenshot server returned unexpected content type %q", contentType)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPNGSize+1))
	if err != nil {
		return nil, fmt.Errorf("read screenshot response: %w", err)
	}
	if len(body) > maxPNGSize {
		return nil, fmt.Errorf("screenshot response exceeds %d bytes", maxPNGSize)
	}
	return body, nil
}

// safeErrorText prevents a binary response (in particular PNG data) from
// being interpolated into an error that is likely to be printed to a terminal.
func safeErrorText(body []byte) string {
	if len(body) == 0 || len(body) > maxErrorSize || !utf8.Valid(body) {
		return ""
	}
	for _, b := range body {
		if b == '\n' || b == '\r' || b == '\t' {
			continue
		}
		if b < 0x20 || b == 0x7f {
			return ""
		}
	}
	return strings.TrimSpace(string(body))
}
