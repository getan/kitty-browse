//go:build unix

package browserhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/control"
	"github.com/getan/kitty-browse/internal/testchromium"
)

const (
	hostStartupTimeout = 45 * time.Second
	hostRequestTimeout = 20 * time.Second
)

// hostChrome returns a Chromium executable, or skips. The env override keeps the
// repository's integration convention; the PATH lookup lets an ordinary
// developer machine run this without extra setup.
func hostChrome(t *testing.T) string {
	t.Helper()
	if chrome := strings.TrimSpace(os.Getenv("KITTY_BROWSE_CHROME")); chrome != "" {
		return chrome
	}
	for _, name := range []string{"chromium", "chrome-headless-shell", "chromium-browser", "google-chrome", "chrome"} {
		if path, err := exec.LookPath(name); err == nil {
			return testchromium.Path(t, path)
		}
	}
	t.Skip("no Chromium found; set KITTY_BROWSE_CHROME to run this integration test")
	return ""
}

// hostTempSocket keeps the path short: macOS caps a Unix socket path at 104
// bytes, and t.TempDir() is far longer than that.
func hostTempSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "kbbh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "control.sock")
}

func hostPage(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>headless host</title><body><h1 id="h">host</h1></body>`)
	}))
	t.Cleanup(server.Close)
	return server
}

func startHost(t *testing.T, opts Options) *Host {
	t.Helper()
	// The host owns the browser context for its whole life, so this context is
	// deliberately not cancelled here: it only bounds a hung startup.
	ctx, cancel := context.WithTimeout(context.Background(), hostStartupTimeout)
	t.Cleanup(cancel)
	started := time.Now()
	h, err := Start(ctx, opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	if elapsed := time.Since(started); elapsed > hostStartupTimeout {
		t.Fatalf("startup took %v", elapsed)
	}
	return h
}

func hostAction(t *testing.T, socket, name, params string) action.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), hostRequestTimeout)
	defer cancel()
	body := fmt.Sprintf(`{"id":"t-%d","action":%q,"params":%s}`, time.Now().UnixNano(), name, params)
	raw, err := control.Action(ctx, socket, []byte(body))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var response action.Response
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("%s: decode %s: %v", name, raw, err)
	}
	if !response.OK {
		t.Fatalf("%s failed: %s", name, raw)
	}
	return response
}

// The headless host must serve the same contract as the visible pane, and a
// socket quit must leave nothing behind: no socket file, no Chromium.
func TestHostServesTheControlContractIntegration(t *testing.T) {
	chrome := hostChrome(t)
	socket := hostTempSocket(t)
	page := hostPage(t)

	h := startHost(t, Options{
		URL: page.URL, Socket: socket, Chrome: chrome, Private: true, Stealth: true, LoadTimeout: hostRequestTimeout,
	})
	if h.Socket() != socket {
		t.Fatalf("socket = %q", h.Socket())
	}

	info := hostAction(t, socket, "info", "{}")
	var document action.Info
	if err := json.Unmarshal(info.Result, &document); err != nil {
		t.Fatalf("info result: %v", err)
	}
	if document.APIVersion != "v1" || document.Backend != "native-cdp" || len(document.Tabs) != 1 {
		t.Fatalf("info = %+v", document)
	}

	navigated := hostAction(t, socket, "navigate", fmt.Sprintf(`{"url":%q}`, page.URL+"?n=2"))
	var afterNavigate action.Info
	if err := json.Unmarshal(navigated.Result, &afterNavigate); err != nil {
		t.Fatalf("navigate result: %v", err)
	}
	if !strings.Contains(afterNavigate.Tabs[0].URL, "?n=2") {
		t.Fatalf("navigation did not reach the tab: %+v", afterNavigate.Tabs)
	}

	created := hostAction(t, socket, "tab.new", `{"url":"about:blank"}`)
	var afterNew action.Info
	if err := json.Unmarshal(created.Result, &afterNew); err != nil {
		t.Fatalf("tab.new result: %v", err)
	}
	if len(afterNew.Tabs) != 2 || afterNew.Tabs[1].Index != 2 || afterNew.ActiveTabID != afterNew.Tabs[1].ID {
		t.Fatalf("tabs after tab.new = %+v", afterNew.Tabs)
	}
	closed := hostAction(t, socket, "tab.close", fmt.Sprintf(`{"tabId":%q}`, afterNew.Tabs[1].ID))
	var afterClose action.Info
	if err := json.Unmarshal(closed.Result, &afterClose); err != nil {
		t.Fatalf("tab.close result: %v", err)
	}
	if len(afterClose.Tabs) != 1 {
		t.Fatalf("tabs after tab.close = %+v", afterClose.Tabs)
	}

	// Page actions run through the same native execution path as the pane.
	hostAction(t, socket, "eval", `{"script":"document.title"}`)

	ctx, cancel := context.WithTimeout(context.Background(), hostRequestTimeout)
	defer cancel()
	// Once Chromium is available a broken screenshot is a failed contract,
	// not a successful integration with a diagnostic line.
	data, err := control.Capture(ctx, socket, false)
	if err != nil {
		t.Fatalf("screenshot capture: %v", err)
	}
	image, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode screenshot pixels: %v", err)
	}
	if got := image.Bounds(); got.Dx() != 1280 || got.Dy() != 800 {
		t.Fatalf("screenshot bounds = %v, want 1280x800", got)
	}

	quit := hostAction(t, socket, "quit", "{}")
	if string(quit.Result) != `{"quitting":true}` {
		t.Fatalf("quit result = %s", quit.Result)
	}
	waitForExit(t, h)
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("control socket survived the quit: %v", err)
	}
	if _, err := control.Info(context.Background(), socket); err == nil {
		t.Fatal("the control socket still answers after quit")
	}
}

// A cancelled context and the /v1/quit endpoint must both stop the host and
// remove its socket.
func TestHostStopsOnCancellationAndEndpointIntegration(t *testing.T) {
	chrome := hostChrome(t)

	t.Run("cancellation", func(t *testing.T) {
		socket := hostTempSocket(t)
		ctx, cancel := context.WithCancel(context.Background())
		h, err := Start(ctx, Options{URL: "about:blank", Socket: socket, Chrome: chrome, Private: true, LoadTimeout: hostRequestTimeout})
		if err != nil {
			cancel()
			t.Fatalf("Start: %v", err)
		}
		cancel()
		waitForExit(t, h)
		if _, err := os.Stat(socket); !os.IsNotExist(err) {
			t.Fatalf("control socket survived cancellation: %v", err)
		}
	})

	t.Run("quit endpoint", func(t *testing.T) {
		socket := hostTempSocket(t)
		h := startHost(t, Options{URL: "about:blank", Socket: socket, Chrome: chrome, Private: true, LoadTimeout: hostRequestTimeout})
		ctx, cancel := context.WithTimeout(context.Background(), hostRequestTimeout)
		defer cancel()
		if err := control.Quit(ctx, socket); err != nil {
			t.Fatalf("quit endpoint: %v", err)
		}
		waitForExit(t, h)
		if _, err := os.Stat(socket); !os.IsNotExist(err) {
			t.Fatalf("control socket survived the quit endpoint: %v", err)
		}
	})
}

func TestHostRejectsAnUnusableChromium(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), hostRequestTimeout)
	defer cancel()
	started := time.Now()
	h, err := Start(ctx, Options{URL: "about:blank", Chrome: filepath.Join(t.TempDir(), "missing-chromium"), Private: true})
	if err == nil {
		_ = h.Close()
		t.Fatal("Start accepted a missing Chromium executable")
	}
	if elapsed := time.Since(started); elapsed > hostRequestTimeout {
		t.Fatalf("failed startup took %v", elapsed)
	}
}

// waitForExit bounds Wait so a hung teardown fails the test instead of the suite.
func waitForExit(t *testing.T, h *Host) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- h.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(hostRequestTimeout):
		t.Fatal("the host did not stop")
	}
}
