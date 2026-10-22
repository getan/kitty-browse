package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestSessionSyncAndChromeHistoryIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to a Chromium executable to run this integration test")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/a":
			_, _ = fmt.Fprint(w, `<!doctype html><title>Page A</title><body><a id="to-b" href="/b">go to B</a></body>`)
		case "/b":
			_, _ = fmt.Fprint(w, `<!doctype html><title>Page B</title><body>B</body>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	allocCtx, allocCancel := integrationAllocator(t, chrome)
	defer allocCancel()
	session, err := Open(allocCtx, server.URL+"/a", 15*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()

	title, err := session.Sync(context.Background())
	if err != nil {
		t.Fatalf("initial Sync: %v", err)
	}
	if title != "Page A" || session.URL() != server.URL+"/a" {
		t.Fatalf("initial state: title=%q URL=%q", title, session.URL())
	}
	initialHistory, initialIndex := session.History()
	if initialIndex < 0 || initialIndex >= len(initialHistory) || initialHistory[initialIndex] != server.URL+"/a" {
		t.Fatalf("initial history=%q index=%d", initialHistory, initialIndex)
	}

	// A DOM click exercises a navigation Chromium owns rather than Session.Navigate.
	if _, err := chromedp.Run(session.Context(), chromedp.Evaluate[any](`document.querySelector("#to-b").click()`)); err != nil {
		t.Fatalf("click link to B: %v", err)
	}
	title = waitForSyncedPage(t, session, server.URL+"/b", "Page B")
	if title != "Page B" {
		t.Fatalf("title after click = %q, want Page B", title)
	}
	clickedHistory, clickedIndex := session.History()
	if len(clickedHistory) != len(initialHistory)+1 || clickedIndex != initialIndex+1 {
		t.Fatalf("history after click=%q index=%d; initial=%q index=%d", clickedHistory, clickedIndex, initialHistory, initialIndex)
	}
	if clickedHistory[clickedIndex] != server.URL+"/b" || !session.CanBack() || session.CanForward() {
		t.Fatalf("state after click: history=%q index=%d back=%v forward=%v", clickedHistory, clickedIndex, session.CanBack(), session.CanForward())
	}

	if err := session.Back(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("Back: %v", err)
	}
	title, err = session.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync after Back: %v", err)
	}
	backHistory, backIndex := session.History()
	if title != "Page A" || session.URL() != server.URL+"/a" || len(backHistory) != len(clickedHistory) || backIndex != initialIndex {
		t.Fatalf("after Back: title=%q URL=%q history=%q index=%d", title, session.URL(), backHistory, backIndex)
	}

	if err := session.Forward(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	title, err = session.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync after Forward: %v", err)
	}
	forwardHistory, forwardIndex := session.History()
	if title != "Page B" || session.URL() != server.URL+"/b" || len(forwardHistory) != len(clickedHistory) || forwardIndex != clickedIndex {
		t.Fatalf("after Forward: title=%q URL=%q history=%q index=%d", title, session.URL(), forwardHistory, forwardIndex)
	}
}

func waitForSyncedPage(t *testing.T, session *Session, wantURL, wantTitle string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		title, err := session.Sync(context.Background())
		if err == nil && session.URL() == wantURL && title == wantTitle {
			return title
		}
		if time.Now().After(deadline) {
			t.Fatalf("Sync did not reach URL=%q title=%q: URL=%q title=%q last error=%v", wantURL, wantTitle, session.URL(), title, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
