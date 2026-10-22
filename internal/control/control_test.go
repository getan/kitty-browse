package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScreenshotHandlerRouting(t *testing.T) {
	t.Parallel()
	calls := 0
	h := screenshotHandler(func(context.Context, bool) ([]byte, error) {
		calls++
		return []byte("png"), nil
	})
	tests := []struct {
		name   string
		method string
		target string
		status int
	}{
		{"not found", http.MethodGet, "/other", http.StatusNotFound},
		{"wrong method", http.MethodPost, "/screenshot", http.StatusMethodNotAllowed},
		{"invalid value", http.MethodGet, "/screenshot?fullPage=yes", http.StatusBadRequest},
		{"empty value", http.MethodGet, "/screenshot?fullPage=", http.StatusBadRequest},
		{"duplicate value", http.MethodGet, "/screenshot?fullPage=true&fullPage=false", http.StatusBadRequest},
		{"unknown parameter", http.MethodGet, "/screenshot?x=true", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(tt.method, tt.target, nil))
			if rr.Code != tt.status {
				t.Fatalf("status = %d, want %d", rr.Code, tt.status)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("capture called %d times for rejected requests", calls)
	}
}

func TestScreenshotHandlerError(t *testing.T) {
	t.Parallel()
	h := screenshotHandler(func(context.Context, bool) ([]byte, error) {
		return nil, errors.New("capture failed")
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/screenshot", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "capture failed") {
		t.Fatalf("response does not contain callback error: %q", rr.Body.String())
	}
}

func TestSafeErrorTextRejectsBinary(t *testing.T) {
	t.Parallel()
	if got := safeErrorText([]byte("\x89PNG\r\n\x1a\nsecret")); got != "" {
		t.Fatalf("safeErrorText exposed binary data: %q", got)
	}
}
