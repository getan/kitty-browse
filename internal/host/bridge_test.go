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

func TestExplicitSelectionRevisionIsMonotonicAndSnapshotsAreImmutable(t *testing.T) {
	b := NewBridge("browser-test")
	tabs := &browser.Tabs{}
	b.Update(tabs, nil)
	before := b.Load()
	if before.Info.ExplicitSelection != nil {
		t.Fatal("initial browser advertised an explicit selection")
	}
	b.RecordExplicitSelection(action.TabInfo{ID: "a", TargetID: "ta"})
	b.Update(tabs, nil)
	first := b.Load()
	if selection := first.Info.ExplicitSelection; selection == nil || selection.Revision != 1 || selection.TabID != "a" || selection.TargetID != "ta" {
		t.Fatalf("first selection = %+v", selection)
	}
	b.Update(tabs, nil)
	if selection := b.Load().Info.ExplicitSelection; selection == nil || selection.Revision != 1 {
		t.Fatalf("a passive refresh changed the selection revision: %+v", selection)
	}
	b.RecordExplicitSelection(action.TabInfo{ID: "b", TargetID: "tb"})
	b.Update(tabs, nil)
	if selection := b.Load().Info.ExplicitSelection; selection == nil || selection.Revision != 2 || selection.TabID != "b" {
		t.Fatalf("second selection = %+v", selection)
	}
	if before.Info.ExplicitSelection != nil || first.Info.ExplicitSelection.Revision != 1 || first.Info.ExplicitSelection.TabID != "a" {
		t.Fatal("published snapshots were mutated")
	}
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
