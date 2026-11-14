package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSegmentedSnapshotAndTextNodeRecoveryIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run Chromium integration")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><body><div role="radiogroup"><label id="label" onclick="document.body.dataset.clicked='yes'"><input type="radio" aria-label="Choice" style="opacity:0;position:absolute;width:0;height:0"><span>Choice</span></label></div></body></html>`)
	}))
	defer server.Close()
	alloc, cancel := integrationAllocator(t, chrome)
	defer cancel()
	s, err := Open(alloc, server.URL, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, err := s.Automate(context.Background(), "snapshot", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Nodes []snapshotNode `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	var radio, text *snapshotNode
	for i := range snapshot.Nodes {
		n := &snapshot.Nodes[i]
		if n.Name == "Choice" && n.Role == "radio" {
			radio = n
		}
		if n.Name == "Choice" && n.Role == "StaticText" {
			text = n
		}
	}
	if radio == nil || radio.Visible == nil || *radio.Visible || text == nil || text.Visible == nil || !*text.Visible {
		t.Fatalf("expected hidden radio and visible text evidence: %s", raw)
	}
	// The hidden radio has no clickable geometry, so it still fails.
	if _, err := s.Automate(context.Background(), "click", json.RawMessage(`{"ref":`+strconvQuote(radio.Ref)+`}`)); automationErrorCode(err) != "not_interactable" {
		t.Fatalf("hidden radio click: %v", err)
	}
	// A StaticText ref is not an element, but it sits inside a label that is:
	// the geometry falls back to the parent element instead of failing.
	if _, err := s.Automate(context.Background(), "click", json.RawMessage(`{"ref":`+strconvQuote(text.Ref)+`}`)); err != nil {
		t.Fatalf("StaticText click did not reach its parent element: %v", err)
	}
	result, err := s.Automate(context.Background(), "get", json.RawMessage(`{"kind":"attr","selector":"body","name":"data-clicked"}`))
	if err != nil || string(result) != `"yes"` {
		t.Fatalf("text-node click did not reach the label: %s, %v", result, err)
	}
}

func TestSnapshotParentLinksAndTargetedTextVisibility(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run Chromium integration")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><body>`)
		for i := 0; i < 70; i++ {
			fmt.Fprintf(w, `<span>background %d</span>`, i)
		}
		fmt.Fprint(w, `<div>用户身份权限 user_access_token</div><table><tr><td><input type="checkbox"></td><td>search:docs:read</td></tr></table></body></html>`)
	}))
	defer server.Close()
	alloc, cancel := integrationAllocator(t, chrome)
	defer cancel()
	s, err := Open(alloc, server.URL, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, err := s.Automate(context.Background(), "snapshot", json.RawMessage(`{"includeText":false}`))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		ParentLinks bool           `json:"parentLinks"`
		Nodes       []snapshotNode `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil || !snapshot.ParentLinks {
		t.Fatalf("snapshot lacks parent links: %s %v", raw, err)
	}
	byRef := make(map[string]snapshotNode)
	var check, label snapshotNode
	for _, n := range snapshot.Nodes {
		byRef[n.Ref] = n
		if n.Role == "checkbox" {
			check = n
		}
		if n.Role == "StaticText" && n.Name == "search:docs:read" {
			label = n
		}
	}
	ancestor := func(n snapshotNode) string {
		for i := 0; i < 8 && n.ParentRef != ""; i++ {
			n = byRef[n.ParentRef]
			if strings.EqualFold(n.Role, "LayoutTableRow") || strings.EqualFold(n.Role, "row") {
				return n.Ref
			}
		}
		return ""
	}
	if check.Ref == "" || label.Ref == "" || ancestor(check) == "" || ancestor(check) != ancestor(label) {
		t.Fatalf("target and label did not retain their row: %s", raw)
	}
	if label.Visible != nil {
		t.Fatal("fixture did not exhaust snapshot's initial visibility budget")
	}
	params, _ := json.Marshal(map[string]string{"kind": "visible", "ref": label.Ref})
	visible, err := s.Automate(context.Background(), "is", params)
	if err != nil || string(visible) != "true" {
		t.Fatalf("targeted text visibility = %s, %v", visible, err)
	}
}
