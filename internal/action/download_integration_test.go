package action

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/getan/kitty-browse/internal/browser"
)

// TestDownloadActionsEndToEnd is opt-in. It uses a local attachment endpoint
// and the configured Chromium executable, never an external helper or site.
func TestDownloadActionsEndToEnd(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run with local Chromium")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/artifact" {
			w.Header().Set("Content-Disposition", `attachment; filename="artifact.txt"`)
			fmt.Fprint(w, "action download")
			return
		}
		fmt.Fprint(w, `<a id="download" href="/artifact">download</a>`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	allocator, closeAllocator, _, err := browser.NewBrowserContext(ctx, browser.LaunchOptions{
		ExecutablePath: chrome, Private: true, DownloadDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAllocator()
	sess, err := browser.Open(allocator, server.URL, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if _, err := chromedp.Run(sess.Context(), chromedp.Click(`#download`)); err != nil {
		t.Fatal(err)
	}

	waited, err := Execute(ctx, Request{Action: "download.wait", Params: json.RawMessage(`{}`)}, sess)
	if err != nil {
		t.Fatal(err)
	}
	var download browser.Download
	if err := json.Unmarshal(waited, &download); err != nil {
		t.Fatal(err)
	}
	if download.State != "completed" || download.Path == "" {
		t.Fatalf("download.wait = %s", waited)
	}
	listed, err := Execute(ctx, Request{Action: "download.list", Params: json.RawMessage(`{}`)}, sess)
	if err != nil {
		t.Fatal(err)
	}
	var downloads []browser.Download
	if err := json.Unmarshal(listed, &downloads); err != nil || len(downloads) != 1 || downloads[0].GUID != download.GUID {
		t.Fatalf("download.list = %s, err=%v", listed, err)
	}
}
