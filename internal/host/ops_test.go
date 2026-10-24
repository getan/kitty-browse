package host

import (
	"context"
	"strings"
	"testing"

	"github.com/getan/kitty-browse/internal/action"
)

// quit is part of the host contract: a client asks for a graceful shutdown
// through the typed action or the socket endpoint, never through key injection.
func TestQuitIsAdvertisedAndHostManaged(t *testing.T) {
	advertised := false
	for _, capability := range action.Capabilities() {
		if capability.Name == "quit" {
			advertised = true
		}
	}
	if !advertised {
		t.Fatal("quit is not advertised in /v1/info capabilities")
	}
	if !BrowserScopedAction("quit") || !HostManagedAction("quit") {
		t.Fatal("quit must be a browser-scoped, host-managed action")
	}
	if err := action.ValidateRequest(action.Request{Action: "quit"}); err != nil {
		t.Fatalf("quit request rejected: %v", err)
	}
}

func TestQuitRequestsOnce(t *testing.T) {
	quit := NewQuit()
	if quit.Requested() {
		t.Fatal("a fresh quit already reports a request")
	}
	quit.Request()
	quit.Request()
	if !quit.Requested() {
		t.Fatal("quit did not record the request")
	}
	select {
	case <-quit.Done():
	default:
		t.Fatal("quit did not close Done")
	}
	var nilQuit *Quit
	nilQuit.Request() // Must not panic.
	if nilQuit.Requested() {
		t.Fatal("nil quit reported a request")
	}
}

func TestResultDocuments(t *testing.T) {
	if string(ResultQuitting) != `{"quitting":true}` {
		t.Fatalf("quitting result = %s", ResultQuitting)
	}
	if string(ResultDone) != `{"released":true}` {
		t.Fatalf("done result = %s", ResultDone)
	}
	raw, err := ViewportDocument(800, 600)
	if err != nil || string(raw) != `{"height":600,"width":800}` {
		t.Fatalf("viewport document = %s, %v", raw, err)
	}
}

func TestDisplayedTabID(t *testing.T) {
	info := action.Info{Tabs: []action.TabInfo{{ID: "tab-1", Index: 1}, {ID: "tab-2", Index: 2}}}
	if got, err := DisplayedTabID(info, "2"); err != nil || got != "tab-2" {
		t.Fatalf("index 2 = %q, %v", got, err)
	}
	if got, err := DisplayedTabID(info, "tab-1"); err != nil || got != "tab-1" {
		t.Fatalf("opaque id = %q, %v", got, err)
	}
	for _, bad := range []string{"0", "-1", "9"} {
		if _, err := DisplayedTabID(info, bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestCheckViewportBounds(t *testing.T) {
	for _, size := range [][2]int{{0, 100}, {100, 0}, {8193, 100}, {100, 8193}} {
		err := CheckViewport(size[0], size[1])
		if err == nil || !strings.Contains(err.Error(), "between 1 and 8192") {
			t.Fatalf("%v accepted: %v", size, err)
		}
	}
	if err := CheckViewport(1, 8192); err != nil {
		t.Fatalf("bounds rejected: %v", err)
	}
}

func TestNewBrowserIDIsUniqueAndPrefixed(t *testing.T) {
	seen := make(map[string]bool)
	for range 32 {
		id := NewBrowserID()
		if !strings.HasPrefix(id, "browser-") {
			t.Fatalf("id = %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

// The bridge still refuses an action the contract does not advertise, and the
// refusal never reaches a browser.
func TestBridgeRejectsUnadvertisedAction(t *testing.T) {
	b := testBridge([]action.TabInfo{{ID: "tab", TargetID: "target", Active: true}})
	raw := []byte(`{"id":"r","action":"browser.destroy"}`)
	responseRaw, err := b.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	assertResponseError(t, responseRaw, "unsupported_action")
}
