//go:build unix

package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "kbctl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestCaptureRoundTrip(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(shortTempDir(t), "c.sock")
	var mu sync.Mutex
	var got []bool
	server, err := Start(socket, func(_ context.Context, fullPage bool) ([]byte, error) {
		mu.Lock()
		got = append(got, fullPage)
		mu.Unlock()
		if fullPage {
			return []byte("full-png"), nil
		}
		return []byte("viewport-png"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	viewport, err := Capture(context.Background(), socket, false)
	if err != nil {
		t.Fatal(err)
	}
	full, err := Capture(context.Background(), socket, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(viewport) != "viewport-png" || string(full) != "full-png" {
		t.Fatalf("unexpected captures: %q, %q", viewport, full)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("fullPage callback values = %v", got)
	}
}

func TestCaptureCancellationReachesCallback(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(shortTempDir(t), "c.sock")
	callbackStarted := make(chan struct{}, 1)
	callbackDone := make(chan error, 1)
	server, err := Start(socket, func(ctx context.Context, _ bool) ([]byte, error) {
		callbackStarted <- struct{}{}
		<-ctx.Done()
		callbackDone <- ctx.Err()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Capture(ctx, socket, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Capture error = %v, want context.Canceled", err)
	}
	// A request canceled before dialing need not call the callback. Send one
	// that reaches it, then cancel it.
	ctx, cancel = context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, requestErr := Capture(ctx, socket, false)
		result <- requestErr
	}()
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("request did not reach callback")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Capture error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Capture did not return after cancellation")
	}
	select {
	case err := <-callbackDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("callback context error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback context was not canceled")
	}
}

func TestServerErrorReturnedByClient(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(shortTempDir(t), "c.sock")
	server, err := Start(socket, func(context.Context, bool) ([]byte, error) {
		return nil, errors.New("renderer unavailable")
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	_, err = Capture(context.Background(), socket, false)
	if err == nil || !strings.Contains(err.Error(), "renderer unavailable") {
		t.Fatalf("Capture error = %v, want server error", err)
	}
}

func TestSocketPermissionsAndClose(t *testing.T) {
	t.Parallel()
	root := shortTempDir(t)
	parent := filepath.Join(root, "private")
	socket := filepath.Join(parent, "c.sock")
	server, err := Start(socket, func(context.Context, bool) ([]byte, error) { return []byte("x"), nil })
	if err != nil {
		t.Fatal(err)
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if parentInfo.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %o, want 700", parentInfo.Mode().Perm())
	}
	socketInfo, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if socketInfo.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %o, want 600", socketInfo.Mode().Perm())
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains after Close: %v", err)
	}
}

func TestStartDoesNotReplaceExistingFile(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(shortTempDir(t), "existing")
	const contents = "keep me"
	if err := os.WriteFile(socket, []byte(contents), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(socket, func(context.Context, bool) ([]byte, error) { return nil, nil }); err == nil {
		t.Fatal("Start succeeded with an existing file")
	}
	got, err := os.ReadFile(socket)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != contents {
		t.Fatalf("existing contents changed to %q", got)
	}
}

func TestCloseDoesNotRemoveReplacementFile(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(shortTempDir(t), "c.sock")
	server, err := Start(socket, func(context.Context, bool) ([]byte, error) { return []byte("x"), nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	const replacement = "replacement"
	if err := os.WriteFile(socket, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(socket)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != replacement {
		t.Fatalf("replacement changed to %q", got)
	}
}

func TestCaptureConnectionError(t *testing.T) {
	t.Parallel()
	_, err := Capture(context.Background(), filepath.Join(shortTempDir(t), "missing.sock"), false)
	if err == nil {
		t.Fatal("Capture unexpectedly succeeded")
	}
}

func TestCaptureRejectsOversizeResponse(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(shortTempDir(t), "raw.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		request, readErr := http.ReadRequest(bufio.NewReader(conn))
		if readErr != nil {
			return
		}
		_ = request.Body.Close()
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: image/png\r\nContent-Length: %d\r\n\r\n", maxPNGSize+1)
	}()
	_, err = Capture(context.Background(), socket, false)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Capture error = %v, want size-limit error", err)
	}
}

func TestInfoAndActionRoundTrip(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(shortTempDir(t), "api.sock")
	server, err := Start(socket,
		func(context.Context, bool) ([]byte, error) { return []byte("png"), nil },
		WithAPI(
			func(context.Context) (json.RawMessage, error) {
				return json.RawMessage(`{"apiVersion":"v1","ok":true,"tabs":[]}`), nil
			},
			func(_ context.Context, request json.RawMessage) (json.RawMessage, error) {
				return append(json.RawMessage(nil), request...), nil
			},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	info, err := Info(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(info) || !strings.Contains(string(info), `"tabs"`) {
		t.Fatalf("unexpected info: %s", info)
	}
	request := json.RawMessage(`{"id":"roundtrip","type":"noop"}`)
	response, err := Action(context.Background(), socket, request)
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != string(request) {
		t.Fatalf("action response = %s, want %s", response, request)
	}
	shot, err := Capture(context.Background(), socket, false)
	if err != nil || string(shot) != "png" {
		t.Fatalf("legacy screenshot = %q, %v", shot, err)
	}
}

func TestQuitRoundTripClosesTheHost(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(shortTempDir(t), "quit.sock")
	asked := make(chan struct{})
	server, err := Start(socket,
		func(context.Context, bool) ([]byte, error) { return nil, nil },
		WithQuit(func(context.Context) { close(asked) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := Quit(context.Background(), socket); err != nil {
		t.Fatalf("quit: %v", err)
	}
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("quit callback never ran")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still exists after Close: %v", err)
	}
}

func TestQuitIsAbsentUnlessEnabled(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(shortTempDir(t), "noquit.sock")
	server, err := Start(socket, func(context.Context, bool) ([]byte, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if err := Quit(context.Background(), socket); err == nil {
		t.Fatal("quit succeeded on a host that never enabled it")
	}
}

func TestActionReturnsJSONErrorEnvelope(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(shortTempDir(t), "api.sock")
	server, err := Start(socket,
		func(context.Context, bool) ([]byte, error) { return nil, nil },
		WithAPI(nil, func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("failed")
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	response, err := Action(context.Background(), socket, json.RawMessage(`{"id":"e"}`))
	if err != nil {
		t.Fatalf("Action rejected valid JSON error envelope: %v", err)
	}
	var envelope apiErrorEnvelope
	if err := json.Unmarshal(response, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "internal_error" || string(envelope.ID) != `"e"` {
		t.Fatalf("unexpected envelope: %s", response)
	}
}

func TestActionCancellationReachesCallback(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(shortTempDir(t), "api.sock")
	started := make(chan struct{})
	done := make(chan error, 1)
	server, err := Start(socket,
		func(context.Context, bool) ([]byte, error) { return nil, nil },
		WithAPI(nil, func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			close(started)
			<-ctx.Done()
			done <- ctx.Err()
			return nil, ctx.Err()
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, requestErr := Action(ctx, socket, json.RawMessage(`{}`))
		result <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("action callback did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Action error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Action did not return after cancellation")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("callback error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback context was not canceled")
	}
}
