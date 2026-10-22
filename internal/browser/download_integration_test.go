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

// TestNativeDownloadEndToEnd is opt-in and uses only a local HTTP server and
// the configured Chromium executable.
func TestNativeDownloadEndToEnd(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run with local Chromium")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/file" {
			w.Header().Set("Content-Disposition", `attachment; filename="same.txt"`)
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "download payload")
			return
		}
		fmt.Fprint(w, `<a id="download" href="/file">download</a>`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	allocator, closeAllocator, _, err := NewBrowserContext(ctx, LaunchOptions{
		ExecutablePath: chrome, Private: true, DownloadDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAllocator()
	sess, err := Open(allocator, server.URL, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	var paths []string
	for i := 0; i < 2; i++ {
		if _, err := chromedp.Run(sess.Context(), chromedp.Click(`#download`)); err != nil {
			t.Fatal(err)
		}
		download, err := sess.WaitDownload(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(download.Path)
		if err != nil || string(data) != "download payload" {
			t.Fatalf("download contents = %q, err=%v", data, err)
		}
		paths = append(paths, download.Path)
	}
	if paths[0] == paths[1] {
		t.Fatalf("duplicate suggested filenames overwrote one path: %q", paths[0])
	}
}
