package host

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/browser"
)

func assertResponseError(t *testing.T, raw json.RawMessage, code string) {
	t.Helper()
	var response action.Response
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.OK || response.Error == nil || response.Error.Code != code {
		t.Fatalf("response = %s, want error %q", raw, code)
	}
}

func testBridge(tabs []action.TabInfo) *Bridge {
	b := NewBridge("browser-test")
	info := action.Info{APIVersion: action.APIVersion, BrowserID: "browser-test", Backend: action.Backend, Tabs: tabs, Capabilities: action.Capabilities()}
	if len(tabs) != 0 {
		info.ActiveTabID = tabs[0].ID
	}
	b.view.Store(&Snapshot{Info: info, Sessions: make(map[string]*browser.Session)})
	return b
}

func TestActionBridgeRejectsUnknownParamsBeforeCDP(t *testing.T) {
	tab := action.TabInfo{ID: "tab", TargetID: "target", Active: true}
	b := testBridge([]action.TabInfo{tab})
	req := action.Request{ID: "r", Action: "click", Params: json.RawMessage(`{"ref":"@e1","unknown":true}`)}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	responseRaw, err := b.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var response action.Response
	if err := json.Unmarshal(responseRaw, &response); err != nil || response.OK || response.Error == nil || response.Error.Code != "invalid_arguments" {
		t.Fatalf("response = %+v, %v", response, err)
	}
}

func TestActionBridgeBrowserScopedCallNeedsNoTab(t *testing.T) {
	b := testBridge(nil)
	done := make(chan json.RawMessage, 1)
	go func() {
		raw, _ := json.Marshal(action.Request{ID: "r", Action: "tab.new", Params: json.RawMessage(`{"url":"about:blank"}`)})
		result, _ := b.Execute(context.Background(), raw)
		done <- result
	}()
	select {
	case call := <-b.Calls:
		call.Reply <- action.Respond(call.Req, b.Load().Info, action.TabInfo{}, json.RawMessage(`{"created":true}`), nil)
	case <-time.After(time.Second):
		t.Fatal("browser-scoped action was rejected before reaching the UI host")
	}
	select {
	case raw := <-done:
		var response action.Response
		if err := json.Unmarshal(raw, &response); err != nil || !response.OK {
			t.Fatalf("response = %+v, %v", response, err)
		}
	case <-time.After(time.Second):
		t.Fatal("browser-scoped response was not delivered")
	}
}

func TestActionBridgeHasBoundedRequestAdmission(t *testing.T) {
	tab := action.TabInfo{ID: "tab", TargetID: "target", Active: true}
	b := testBridge([]action.TabInfo{tab})
	for range cap(b.limit) {
		b.limit <- struct{}{}
	}
	raw, _ := json.Marshal(action.Request{ID: "r", Action: "get", Params: json.RawMessage(`{"kind":"url"}`)})
	result, err := b.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var response action.Response
	if err := json.Unmarshal(result, &response); err != nil || response.Error == nil || response.Error.Code != "target_busy" {
		t.Fatalf("bounded admission response = %+v, %v", response, err)
	}
}
