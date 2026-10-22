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

func TestWaitForPopupIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run popup integration")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/popup":
			_, _ = fmt.Fprintf(w, `<!doctype html><title>Receipt %s</title><body>popup</body>`, r.URL.Query().Get("from"))
		default:
			_, _ = fmt.Fprintf(w, `<!doctype html><title>%s</title><button id=open onclick="window.open('/popup?from=%s')">open</button>`, r.URL.Query().Get("tab"), r.URL.Query().Get("tab"))
		}
	}))
	defer server.Close()
	alloc, cancel := integrationAllocator(t, chrome)
	defer cancel()
	tabs, err := OpenTabs(alloc, server.URL+"/?tab=one", 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tabs.Close()
	if err := tabs.New(context.Background(), server.URL+"/?tab=two", 15*time.Second); err != nil {
		t.Fatal(err)
	}
	sessions := tabs.Sessions()
	one, two := sessions[0], sessions[1]
	oneToken := one.BeginPopupWait()
	if _, err := two.Automate(context.Background(), "click", json.RawMessage(`{"selector":"#open"}`)); err != nil {
		t.Fatal(err)
	}
	short, shortCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer shortCancel()
	if _, err := one.WaitForPopup(short, json.RawMessage(fmt.Sprintf(`{"sinceToken":%d}`, oneToken))); err == nil {
		t.Fatal("popup from another tab matched the first tab")
	}

	token := one.BeginPopupWait()
	if _, err := one.Automate(context.Background(), "click", json.RawMessage(`{"selector":"#open"}`)); err != nil {
		t.Fatal(err)
	}
	wait, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	raw, err := one.WaitForPopup(wait, json.RawMessage(fmt.Sprintf(`{"sinceToken":%d,"urlPattern":"*/popup?from=one","titlePattern":"Receipt*"}`, token)))
	if err != nil {
		t.Fatal(err)
	}
	var result popupWaitResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.TargetID == "" || result.OpenerTargetID != one.TargetID() || !strings.Contains(result.URL, "/popup?from=one") || result.Title != "Receipt one" {
		t.Fatalf("popup result = %+v", result)
	}
	consumeCtx, consumeCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer consumeCancel()
	if _, err := one.WaitForPopup(consumeCtx, json.RawMessage(fmt.Sprintf(`{"sinceToken":%d}`, token))); err == nil {
		t.Fatal("popup was returned more than once")
	}
}
