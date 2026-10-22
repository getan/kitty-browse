// Package control provides a local control channel for a running kitty-browse
// process.
package control

import (
	"bytes"
	"context"
	"encoding/json"
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
	requestTimeout   = 15 * time.Second
	clientTimeout    = 20 * time.Second
	apiTimeout       = 120 * time.Second
	apiClientTimeout = 125 * time.Second
	maxRequestSize   = 1 << 20   // 1 MiB
	maxJSONSize      = 16 << 20  // 16 MiB
	maxPNGSize       = 128 << 20 // 128 MiB
	maxErrorSize     = 64 << 10
)

// CaptureFunc captures a PNG. fullPage is false for the current viewport and
// true for the complete page.
type CaptureFunc func(context.Context, bool) ([]byte, error)

// InfoFunc returns the current browser information as JSON.
type InfoFunc func(context.Context) (json.RawMessage, error)

// ActionFunc executes a JSON action request and returns its JSON response.
type ActionFunc func(context.Context, json.RawMessage) (json.RawMessage, error)

type serverConfig struct {
	info   InfoFunc
	action ActionFunc
}

// Option configures optional control server functionality.
type Option func(*serverConfig)

// WithAPI enables the information and action API. A nil callback leaves its
// corresponding route disabled.
func WithAPI(info InfoFunc, action ActionFunc) Option {
	return func(c *serverConfig) {
		c.info = info
		c.action = action
	}
}

// Server is a screenshot control server.
type Server struct {
	httpServer *http.Server
	listener   net.Listener
	socketPath string
	socketInfo os.FileInfo

	closeOnce sync.Once
	closeErr  error
}

func newServer(socketPath string, listener net.Listener, socketInfo os.FileInfo, capture CaptureFunc, config serverConfig) *Server {
	s := &Server{
		listener:   listener,
		socketPath: socketPath,
		socketInfo: socketInfo,
	}
	s.httpServer = &http.Server{
		Handler:           controlHandler(capture, config),
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

func controlHandler(capture CaptureFunc, config serverConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case exactPath(r, "/screenshot") || exactPath(r, "/v1/screenshot"):
			screenshotHandler(capture).ServeHTTP(w, r)
		case exactPath(r, "/v1/info") && config.info != nil:
			infoHandler(config.info).ServeHTTP(w, r)
		case exactPath(r, "/v1/actions") && config.action != nil:
			actionHandler(config.action).ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func exactPath(r *http.Request, path string) bool {
	return r.URL.Path == path && r.URL.EscapedPath() == path
}

func screenshotHandler(capture CaptureFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !exactPath(r, "/screenshot") && !exactPath(r, "/v1/screenshot") {
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

func infoHandler(info InfoFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeAPIError(w, http.StatusMethodNotAllowed, nil, "invalid_request", "method not allowed", false)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), apiTimeout)
		defer cancel()
		response, err := info(ctx)
		writeAPICallbackResult(w, ctx, nil, response, err)
	})
}

func actionHandler(action ActionFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeAPIError(w, http.StatusMethodNotAllowed, nil, "invalid_request", "method not allowed", false)
			return
		}
		if r.ContentLength > maxRequestSize {
			writeAPIError(w, http.StatusRequestEntityTooLarge, nil, "request_too_large", "request exceeds 1 MiB", false)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestSize)
		request, err := io.ReadAll(r.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeAPIError(w, http.StatusRequestEntityTooLarge, nil, "request_too_large", "request exceeds 1 MiB", false)
				return
			}
			writeAPIError(w, http.StatusBadRequest, nil, "invalid_request", "could not read request", false)
			return
		}
		request = bytes.TrimSpace(request)
		if !json.Valid(request) {
			writeAPIError(w, http.StatusBadRequest, nil, "invalid_request", "request body is not valid JSON", false)
			return
		}
		if len(request) == 0 || request[0] != '{' {
			writeAPIError(w, http.StatusBadRequest, nil, "invalid_request", "request body must be a JSON object", false)
			return
		}
		id := requestID(request)
		ctx, cancel := context.WithTimeout(r.Context(), apiTimeout)
		defer cancel()
		response, callbackErr := action(ctx, json.RawMessage(request))
		writeAPICallbackResult(w, ctx, id, response, callbackErr)
	})
}

func requestID(request []byte) json.RawMessage {
	var value struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(request, &value) == nil && len(value.ID) != 0 {
		return value.ID
	}
	return nil
}

func writeAPICallbackResult(w http.ResponseWriter, ctx context.Context, id, response json.RawMessage, err error) {
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			writeAPIError(w, http.StatusGatewayTimeout, id, "timeout", "request timed out", false)
			return
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			writeAPIError(w, http.StatusRequestTimeout, id, "canceled", "request canceled", false)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, id, "internal_error", "internal error", false)
		return
	}
	if len(response) > maxJSONSize {
		writeAPIError(w, http.StatusInternalServerError, id, "internal_error", "response exceeds 16 MiB", false)
		return
	}
	if !json.Valid(response) {
		writeAPIError(w, http.StatusInternalServerError, id, "internal_error", "callback returned invalid JSON", false)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(len(response)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response)
}

type apiErrorEnvelope struct {
	APIVersion string          `json:"apiVersion"`
	ID         json.RawMessage `json:"id"`
	OK         bool            `json:"ok"`
	Error      struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
}

func writeAPIError(w http.ResponseWriter, status int, id json.RawMessage, code, message string, retryable bool) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	envelope := apiErrorEnvelope{APIVersion: "v1", ID: id, OK: false}
	envelope.Error.Code = code
	envelope.Error.Message = message
	envelope.Error.Retryable = retryable
	body, _ := json.Marshal(envelope)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
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
