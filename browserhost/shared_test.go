package browserhost

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/browser"
	"github.com/getan/kitty-browse/internal/host"
)

func TestSharedOperatorCapabilitiesCannotCrossViews(t *testing.T) {
	aID, bID := ViewID("gui-a", "default"), ViewID("tui-b", "default")
	a := &Host{bridge: host.NewBridge("browser-a"), socket: "a.sock", done: make(chan struct{})}
	b := &Host{bridge: host.NewBridge("browser-b"), socket: "b.sock", done: make(chan struct{})}
	s := &sharedServer{token: "master-private", records: map[string]sharedRecord{aID: {view: a, key: "operator-a"}, bID: {view: b, key: "operator-b"}}}
	for _, tc := range []struct {
		path, id, key string
		status        int
	}{
		{"/reconnect", aID, "master-private", 200},
		{"/reconnect", aID, "operator-a", 403},
		{"/open", bID, "operator-a", 403},
		{"/input", bID, "operator-a", 403},
		{"/operator-action", bID, "operator-a", 403},
		{"/input", aID, "master-private", 403},
		{"/open", aID, "", 403},
	} {
		body, _ := json.Marshal(sharedRequest{ID: tc.id})
		r := httptest.NewRequest("POST", tc.path, strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+tc.key)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: status=%d want=%d body=%s", tc.path, w.Code, tc.status, w.Body.String())
		}
	}
	if _, err := a.OperatorAction(context.Background(), json.RawMessage(`{"action":"eval","target":{"browserId":"browser-a"},"params":{"script":"1"}}`)); err == nil {
		t.Fatal("operator chrome accepted arbitrary script")
	}
}

func TestViewIdentityIncludesConversationAndInstance(t *testing.T) {
	if ViewID("gui", "default") == ViewID("tui", "default") || ViewID("gui", "one") == ViewID("gui", "two") {
		t.Fatal("view identity collapsed")
	}
	for _, bad := range []string{"", "../escape", strings.Repeat("a", 25), strings.Repeat("A", 24)} {
		if validViewID(bad) {
			t.Errorf("accepted invalid view %q", bad)
		}
	}
}

func TestOperatorChromeDoesNotClearAutomationPause(t *testing.T) {
	bridge := host.NewBridge("view-a")
	bridge.Update(&browser.Tabs{}, nil)
	bridge.Pause("manual")
	view := &Host{bridge: bridge}
	raw := json.RawMessage(`{"id":"new","action":"tab.new","params":{"url":"about:blank"},"target":{"browserId":"view-a"}}`)
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	response, err := bridge.Execute(ctx, raw)
	if err != nil || !strings.Contains(string(response), "browser_paused") {
		t.Fatalf("automation was not stopped: %s %v", response, err)
	}
	go func() {
		select {
		case call := <-bridge.Calls:
			call.Reply <- action.Response{ID: call.Req.ID, OK: true, Result: json.RawMessage(`{}`)}
		case <-ctx.Done():
		}
	}()
	response, err = view.OperatorAction(ctx, raw)
	if err != nil || !strings.Contains(string(response), `"ok":true`) {
		t.Fatalf("authenticated operator chrome did not work: %s %v", response, err)
	}
	if !bridge.Interaction().Paused {
		t.Fatal("manual browser chrome implicitly resumed automation")
	}
	response, err = bridge.Execute(ctx, raw)
	if err != nil || !strings.Contains(string(response), "browser_paused") {
		t.Fatalf("operator action bypassed the later automation gate: %s %v", response, err)
	}
}

func TestExpiredFrontendReleasesOnlyItsView(t *testing.T) {
	a, b := fakeSessionHost("a.sock"), fakeSessionHost("b.sock")
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	now := time.Now()
	s := &sharedServer{records: map[string]sharedRecord{
		"a": {view: a, key: "a", seen: now.Add(-3 * time.Minute)},
		"b": {view: b, key: "b", seen: now},
	}}
	s.expireViews(now)
	select {
	case <-a.done:
	default:
		t.Fatal("crashed frontend's view stayed alive")
	}
	select {
	case <-b.done:
		t.Fatal("expired lease closed another frontend's view")
	default:
	}
	if _, retained := s.records["a"]; retained {
		t.Fatal("expired view retained its operator capability")
	}
}
