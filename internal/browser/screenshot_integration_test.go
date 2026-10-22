package browser

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestScreenshotIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to a Chromium executable to run this integration test")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><style>
			html,body { margin:0; padding:0 }
			body { height:1600px; background:linear-gradient(#f00,#00f) }
		</style><body></body>`)
	}))
	defer server.Close()

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(),
		chromedp.ExecPath(chrome),
		chromedp.Headless,
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
	)
	defer allocCancel()
	session, err := Open(allocCtx, server.URL, 15*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()
	if err := session.SetViewport(640, 480); err != nil {
		t.Fatalf("SetViewport: %v", err)
	}

	viewport, err := session.Screenshot(context.Background(), false)
	if err != nil {
		t.Fatalf("viewport Screenshot: %v", err)
	}
	viewportConfig, err := png.DecodeConfig(bytes.NewReader(viewport))
	if err != nil {
		t.Fatalf("decode viewport screenshot: %v", err)
	}
	if viewportConfig.Width != 640 || viewportConfig.Height != 480 {
		t.Fatalf("viewport size = %dx%d, want 640x480", viewportConfig.Width, viewportConfig.Height)
	}

	if _, err := chromedp.Run(session.ctx, chromedp.Evaluate[any](`scrollTo(0, 500)`)); err != nil {
		t.Fatalf("scroll page: %v", err)
	}
	before, err := chromedp.Run(session.ctx, chromedp.Evaluate[float64](`scrollY`))
	if err != nil {
		t.Fatalf("read initial scrollY: %v", err)
	}
	if before == 0 {
		t.Fatal("page did not scroll")
	}

	viewport, err = session.Screenshot(context.Background(), false)
	if err != nil {
		t.Fatalf("scrolled viewport Screenshot: %v", err)
	}
	assertScrollY(t, session, before, "viewport screenshot")

	full, err := session.Screenshot(context.Background(), true)
	if err != nil {
		t.Fatalf("full-page Screenshot: %v", err)
	}
	fullConfig, err := png.DecodeConfig(bytes.NewReader(full))
	if err != nil {
		t.Fatalf("decode full-page screenshot: %v", err)
	}
	if fullConfig.Width != 640 || fullConfig.Height <= viewportConfig.Height {
		t.Fatalf("full-page size = %dx%d, want width 640 and height > %d", fullConfig.Width, fullConfig.Height, viewportConfig.Height)
	}
	assertScrollY(t, session, before, "full-page screenshot")
}

func assertScrollY(t *testing.T, session *Session, want float64, operation string) {
	t.Helper()
	got, err := chromedp.Run(session.ctx, chromedp.Evaluate[float64](`scrollY`))
	if err != nil {
		t.Fatalf("read scrollY after %s: %v", operation, err)
	}
	if got != want {
		t.Fatalf("scrollY after %s = %v, want %v", operation, got, want)
	}
}
