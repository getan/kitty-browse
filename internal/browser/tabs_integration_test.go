package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	cdptarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

func TestTabsIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to a Chromium executable to run this integration test")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		background := "rgb(220, 20, 20)"
		name := "red"
		if r.URL.Path == "/blue" {
			background = "rgb(20, 20, 220)"
			name = "blue"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><style>
			html,body { margin:0; padding:0; background:%s }
			body { height:1800px }
		</style><body data-page=%q></body>`, background, name)
	}))
	defer server.Close()

	allocCtx, allocCancel := integrationAllocator(t, chrome)
	defer allocCancel()
	tabs, err := OpenTabs(allocCtx, server.URL+"/red", 15*time.Second)
	if err != nil {
		t.Fatalf("OpenTabs: %v", err)
	}
	defer tabs.Close()

	red := tabs.Active()
	if red == nil || tabs.Index() != 0 || len(tabs.Sessions()) != 1 {
		t.Fatalf("initial tabs state: active=%p index=%d count=%d", red, tabs.Index(), len(tabs.Sessions()))
	}
	copyOfSessions := tabs.Sessions()
	copyOfSessions[0] = nil
	if tabs.Active() == nil {
		t.Fatal("Sessions returned the owned slice instead of a copy")
	}
	if err := red.SetViewport(320, 240); err != nil {
		t.Fatalf("red SetViewport: %v", err)
	}
	if _, err := chromedp.Run(red.Context(),
		chromedp.Evaluate[any](`document.body.dataset.memory = "red-state"; scrollTo(0, 500)`),
	); err != nil {
		t.Fatalf("set red state: %v", err)
	}

	if err := tabs.New(context.Background(), server.URL+"/blue", 15*time.Second); err != nil {
		t.Fatalf("New: %v", err)
	}
	blue := tabs.Active()
	if blue == red || tabs.Index() != 1 || len(tabs.Sessions()) != 2 {
		t.Fatalf("new tabs state: active=%p index=%d count=%d", blue, tabs.Index(), len(tabs.Sessions()))
	}
	if err := blue.SetViewport(320, 240); err != nil {
		t.Fatalf("blue SetViewport: %v", err)
	}
	if _, err := chromedp.Run(blue.Context(), chromedp.Evaluate[any](`document.body.dataset.memory = "blue-state"`)); err != nil {
		t.Fatalf("set blue state: %v", err)
	}
	assertActiveScreenshotColor(t, tabs, color.RGBA{R: 20, G: 20, B: 220, A: 255})

	if err := tabs.Select(0); err != nil {
		t.Fatalf("Select red: %v", err)
	}
	if tabs.Active() != red || tabs.Index() != 0 {
		t.Fatalf("selected red: active=%p index=%d", tabs.Active(), tabs.Index())
	}
	state, err := chromedp.Run(red.Context(), chromedp.Evaluate[string](`document.body.dataset.memory + ":" + scrollY`))
	if err != nil {
		t.Fatalf("read preserved red state: %v", err)
	}
	if state != "red-state:500" {
		t.Fatalf("red state after switch = %q, want red-state:500", state)
	}
	assertActiveScreenshotColor(t, tabs, color.RGBA{R: 220, G: 20, B: 20, A: 255})

	if err := tabs.Select(1); err != nil {
		t.Fatalf("Select blue: %v", err)
	}
	if err := tabs.CloseTab(0); err != nil {
		t.Fatalf("CloseTab(first): %v", err)
	}
	if tabs.Active() != blue || tabs.Index() != 0 || len(tabs.Sessions()) != 1 {
		t.Fatalf("after closing first: active=%p index=%d count=%d", tabs.Active(), tabs.Index(), len(tabs.Sessions()))
	}
	state, err = chromedp.Run(blue.Context(), chromedp.Evaluate[string](`document.body.dataset.memory`))
	if err != nil || state != "blue-state" {
		t.Fatalf("remaining tab after closing first: state=%q err=%v", state, err)
	}
	if err := tabs.CloseTab(0); !errors.Is(err, ErrLastTab) {
		t.Fatalf("CloseTab(final) error = %v, want ErrLastTab", err)
	}
	assertActiveScreenshotColor(t, tabs, color.RGBA{R: 20, G: 20, B: 220, A: 255})
}

func TestScrollWheelIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to a Chromium executable to run this integration test")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><style>html,body{margin:0}body{height:2000px}</style><body></body>`)
	}))
	defer server.Close()

	allocCtx, allocCancel := integrationAllocator(t, chrome)
	defer allocCancel()
	session, err := Open(allocCtx, server.URL, 15*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()
	if err := session.SetViewport(640, 480); err != nil {
		t.Fatalf("SetViewport: %v", err)
	}
	if err := session.Scroll(100, 100, 300); err != nil {
		t.Fatalf("Scroll: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		y, err := chromedp.Run(session.Context(), chromedp.Evaluate[float64](`scrollY`))
		if err != nil {
			t.Fatalf("read scrollY: %v", err)
		}
		if y > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wheel event did not increase scrollY")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTabsReconcilePopupAndExternalCloseIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run this integration test")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/popup" {
			_, _ = fmt.Fprint(w, `<!doctype html><title>popup</title><script>window.popupMemory=42</script><body>popup</body>`)
			return
		}
		_, _ = fmt.Fprint(w, `<!doctype html><title>opener</title><body><button id="popup" onclick="window.open('/popup','popup-test')">open</button></body>`)
	}))
	defer server.Close()
	alloc, cancel := integrationAllocator(t, chrome)
	defer cancel()
	tabs, err := OpenTabs(alloc, server.URL, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tabs.Close()
	originalID := tabs.Active().TargetID()
	if err := chromedp.Do(tabs.Active().Context(), chromedp.Click("#popup")); err != nil {
		t.Fatal(err)
	}
	waitForReconcile(t, tabs, func() bool { return len(tabs.Sessions()) == 2 })
	if tabs.Active().URL() != server.URL+"/popup" {
		t.Fatalf("reconciled active popup URL = %q", tabs.Active().URL())
	}
	value, err := chromedp.Run(tabs.Active().Context(), chromedp.Evaluate[int](`window.popupMemory`))
	if err != nil || value != 42 {
		t.Fatalf("popup state was reloaded/lost: value=%d err=%v", value, err)
	}
	if _, err := chromedp.Run(tabs.Active().Context(), chromedp.Evaluate[any](`window.close()`)); err != nil {
		t.Fatal(err)
	}
	waitForReconcile(t, tabs, func() bool { return len(tabs.Sessions()) == 1 })
	if tabs.Active().TargetID() != originalID {
		t.Fatal("closing popup did not restore the existing user tab")
	}
	if _, err := chromedp.CallBrowser(tabs.anchor.Context(), cdptarget.CloseTarget,
		cdptarget.CloseTargetParams{TargetID: cdptarget.ID(originalID)}); err != nil {
		t.Fatal(err)
	}
	waitForReconcile(t, tabs, func() bool {
		return len(tabs.Sessions()) == 1 && tabs.Active().URL() == "about:blank" && tabs.Active().TargetID() != originalID
	})
}

func waitForReconcile(t *testing.T, tabs *Tabs, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, err := tabs.Reconcile(ctx)
		if err == nil && done() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("reconcile did not reach expected state: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func integrationAllocator(t *testing.T, chrome string) (context.Context, context.CancelFunc) {
	t.Helper()
	return chromedp.NewExecAllocator(context.Background(),
		chromedp.ExecPath(chrome),
		chromedp.Headless,
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
	)
}

func assertActiveScreenshotColor(t *testing.T, tabs *Tabs, want color.RGBA) {
	t.Helper()
	data, err := tabs.Screenshot(context.Background(), false)
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	image, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode screenshot: %v", err)
	}
	r, g, b, _ := image.At(image.Bounds().Dx()/2, image.Bounds().Dy()/2).RGBA()
	got := color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255}
	if got != want {
		t.Fatalf("active screenshot center = %#v, want %#v", got, want)
	}
}
