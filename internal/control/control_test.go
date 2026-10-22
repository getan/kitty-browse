package control

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestControlHandlerAPIRouting(t *testing.T) {
	t.Parallel()
	h := controlHandler(
		func(context.Context, bool) ([]byte, error) { return []byte("png"), nil },
		serverConfig{
			info: func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{"kind":"info"}`), nil },
			action: func(_ context.Context, request json.RawMessage) (json.RawMessage, error) {
				return request, nil
			},
		},
	)
	tests := []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodGet, "/v1/screenshot", "", http.StatusOK},
		{http.MethodGet, "/v1/info", "", http.StatusOK},
		{http.MethodPost, "/v1/actions", `{"id":"a"}`, http.StatusOK},
		{http.MethodPost, "/v1/info", "", http.StatusMethodNotAllowed},
		{http.MethodGet, "/v1/actions", "", http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body)))
		if rr.Code != tt.status {
			t.Errorf("%s %s status = %d, want %d", tt.method, tt.path, rr.Code, tt.status)
		}
	}

	disabled := controlHandler(func(context.Context, bool) ([]byte, error) { return nil, nil }, serverConfig{})
	for _, path := range []string{"/v1/info", "/v1/actions"} {
		rr := httptest.NewRecorder()
		disabled.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Errorf("disabled %s status = %d, want 404", path, rr.Code)
		}
	}
}

func TestActionHandlerRejectsInvalidRequests(t *testing.T) {
	t.Parallel()
	calls := 0
	h := actionHandler(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	})
	tests := []struct {
		name   string
		body   []byte
		status int
		code   string
	}{
		{"invalid json", []byte(`{"x":`), http.StatusBadRequest, "invalid_request"},
		{"non-object", []byte(`[]`), http.StatusBadRequest, "invalid_request"},
		{"too large", bytes.Repeat([]byte(" "), maxRequestSize+1), http.StatusRequestEntityTooLarge, "request_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/actions", bytes.NewReader(tt.body)))
			if rr.Code != tt.status {
				t.Fatalf("status = %d, want %d", rr.Code, tt.status)
			}
			var envelope apiErrorEnvelope
			if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("invalid error envelope: %v", err)
			}
			if envelope.APIVersion != "v1" || envelope.OK || envelope.Error.Code != tt.code || envelope.Error.Retryable {
				t.Fatalf("unexpected error envelope: %+v", envelope)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("callback called %d times", calls)
	}
}

func TestAPIHandlerErrorsAreSafeJSON(t *testing.T) {
	t.Parallel()
	h := actionHandler(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("\x1b[31msecret")
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/actions", strings.NewReader(`{"id":"request-1"}`)))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "secret") || strings.ContainsRune(rr.Body.String(), '\x1b') {
		t.Fatalf("callback error leaked into response: %q", rr.Body.String())
	}
	var envelope apiErrorEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if string(envelope.ID) != `"request-1"` || envelope.Error.Code != "internal_error" {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
}

func TestAPICallbackContextErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"timeout", context.DeadlineExceeded, http.StatusGatewayTimeout, "timeout"},
		{"canceled", context.Canceled, http.StatusRequestTimeout, "canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := infoHandler(func(context.Context) (json.RawMessage, error) { return nil, tt.err })
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/info", nil))
			if rr.Code != tt.status {
				t.Fatalf("status = %d, want %d", rr.Code, tt.status)
			}
			var envelope apiErrorEnvelope
			if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != tt.code || envelope.Error.Retryable {
				t.Fatalf("unexpected envelope: %+v", envelope)
			}
		})
	}
}
