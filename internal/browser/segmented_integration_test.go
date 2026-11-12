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
	for _, n := range []*snapshotNode{radio, text} {
		_, err := s.Automate(context.Background(), "click", json.RawMessage(`{"ref":`+strconvQuote(n.Ref)+`}`))
		if automationErrorCode(err) != "not_interactable" {
			t.Fatalf("%s click: %v", n.Role, err)
		}
		if n.Role == "StaticText" && !strings.Contains(err.Error(), "non-element") {
			t.Fatalf("text node received unclear error: %v", err)
		}
	}
	result, err := s.Automate(context.Background(), "get", json.RawMessage(`{"kind":"attr","selector":"body","name":"data-clicked"}`))
	if err != nil || string(result) != "null" {
		t.Fatalf("failed targets dispatched a click: %s, %v", result, err)
	}
}
