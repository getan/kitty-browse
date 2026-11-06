//go:build unix

package browserhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/control"
)

func TestInterventionPausesNativeActionsIntegration(t *testing.T) {
	chrome := hostChrome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><meta charset=utf-8><title>Pause fixture</title><button id="send" onclick="this.textContent='submitted'">send</button><div id="gate" role="dialog">操作过快，请慢一点[5014][502]</div>`)
	}))
	defer server.Close()
	opts := DefaultOptions()
	opts.Chrome, opts.URL, opts.Private, opts.Socket = chrome, server.URL, true, hostTempSocket(t)
	h := startHost(t, opts)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := func(name, params string) action.Response {
		t.Helper()
		raw, err := control.Action(ctx, h.Socket(), []byte(fmt.Sprintf(`{"id":"test","action":%q,"params":%s}`, name, params)))
		if err != nil {
			t.Fatal(err)
		}
		var response action.Response
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	for _, name := range []string{"click", "click", "reload", "tab.new"} {
		params := `{}`
		if name == "click" {
			params = `{"selector":"#send"}`
		}
		got := run(name, params)
		if got.OK || got.Error == nil || got.Error.Code != "browser_paused" || got.Error.Retryable {
			t.Fatalf("%s not stopped: %+v", name, got)
		}
	}
	if got := run("get", `{"kind":"text","selector":"#send"}`); !got.OK || string(got.Result) != `"send"` {
		t.Fatalf("blocked action changed page or observation failed: %+v", got)
	}
	// A person can complete/remove the challenge through the page even though
	// automatic eval/click/reload are blocked. Removing it does not auto-resume.
	if err := h.tabs.Active().Click(1, 1); err != nil {
		t.Fatal(err)
	}
	if got := run("interaction.status", `{}`); !got.OK {
		t.Fatal("status unavailable while paused")
	}
	// Explicit operator resume clears the latch, but a still-visible challenge
	// is detected again before another mutation is sent.
	if got := run("interaction.resume", `{}`); !got.OK {
		t.Fatal("explicit resume failed")
	}
	if got := run("click", `{"selector":"#send"}`); got.OK || got.Error == nil || got.Error.Code != "browser_paused" {
		t.Fatal("resume bypassed a visible challenge")
	}
}

// A trusted in-process operator can dismiss a challenge, but the ordinary
// control socket cannot call the operator primitive or resume itself by doing so.
func TestOperatorInputDuringPauseIntegration(t *testing.T) {
	chrome := hostChrome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><meta charset=utf-8><title>Pause fixture</title><div id="gate" role="dialog" onclick="this.hidden=true" style="position:fixed;left:0;top:0;width:200px;height:100px">操作过快，请慢一点[5014][502]</div>`)
	}))
	defer server.Close()
	opts := DefaultOptions()
	opts.Chrome, opts.URL, opts.Private, opts.Socket = chrome, server.URL, true, hostTempSocket(t)
	h := startHost(t, opts)
	ctx, cancel := context.WithTimeout(context.Background(), hostRequestTimeout)
	defer cancel()
	blocked := hostActionRaw(t, ctx, h.Socket(), `{"id":"blocked","action":"eval","params":{"script":"1"}}`)
	if blocked.OK || blocked.Error == nil || blocked.Error.Code != "browser_paused" {
		t.Fatalf("automation was not paused: %+v", blocked)
	}
	info := h.bridge.Load().Info
	if len(info.Tabs) != 1 {
		t.Fatalf("tabs = %+v", info.Tabs)
	}
	tab := info.Tabs[0]
	event := OperatorInput{BrowserID: info.BrowserID, TabID: tab.ID, TargetID: tab.TargetID, Kind: "pointer-down", X: 20, Y: 20}
	if err := h.SendOperatorInput(ctx, event); err != nil {
		t.Fatal(err)
	}
	event.Kind = "pointer-up"
	if err := h.SendOperatorInput(ctx, event); err != nil {
		t.Fatal(err)
	}
	if got := hostActionRaw(t, ctx, h.Socket(), `{"id":"check","action":"is","params":{"kind":"visible","selector":"#gate"}}`); !got.OK || string(got.Result) != "false" {
		t.Fatalf("operator did not dismiss the fixture: %+v", got)
	}
	if !h.bridge.Interaction().Paused {
		t.Fatal("operator input silently resumed automation")
	}
	if got := hostActionRaw(t, ctx, h.Socket(), `{"id":"bad","action":"operator.input","params":{}}`); got.OK {
		t.Fatal("manual input became accessible via the public socket")
	}
}

func hostActionRaw(t *testing.T, ctx context.Context, socket, body string) action.Response {
	t.Helper()
	raw, err := control.Action(ctx, socket, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var response action.Response
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	return response
}
