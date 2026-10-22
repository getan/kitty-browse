package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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

func TestSessionClickIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to a Chromium executable to run this integration test")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/a":
			_, _ = fmt.Fprint(w, `<!doctype html><title>Page A</title><body style="margin:0"><a id="to-b" href="/b" style="display:block;margin:40px;width:200px;height:40px">go to B</a></body>`)
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

	type linkBox struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
		W float64 `json:"w"`
		H float64 `json:"h"`
	}
	box, err := chromedp.Run(session.Context(), chromedp.Evaluate[linkBox](`(() => {
		const r = document.querySelector("#to-b").getBoundingClientRect();
		return {x: r.x, y: r.y, w: r.width, h: r.height};
	})()`))
	if err != nil {
		t.Fatalf("measure link: %v", err)
	}
	if box.W <= 0 || box.H <= 0 {
		t.Fatalf("link box = %+v", box)
	}
	// Session.Click is the terminal-mouse path: move, then press and release.
	if err := session.Click(box.X+box.W/2, box.Y+box.H/2); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if title := waitForSyncedPage(t, session, server.URL+"/b", "Page B"); title != "Page B" {
		t.Fatalf("title after Click = %q", title)
	}
}

func TestSessionDragAndTypingIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to a Chromium executable to run this integration test")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Inputs</title>
<body style="margin:0">
<input id="text" style="position:absolute;left:20px;top:20px;width:320px;height:40px">
<input id="range" type="range" min="0" max="100" value="0" style="position:absolute;left:20px;top:120px;width:400px;height:40px">
</body>`)
	}))
	defer server.Close()

	allocCtx, allocCancel := integrationAllocator(t, chrome)
	defer allocCancel()
	session, err := Open(allocCtx, server.URL, 15*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer session.Close()

	type box struct {
		X, Y, W, H float64
	}
	measure := func(selector string) box {
		t.Helper()
		b, err := chromedp.Run(session.Context(), chromedp.Evaluate[box](`(() => {
			const r = document.querySelector(`+strconv.Quote(selector)+`).getBoundingClientRect();
			return {X: r.x, Y: r.y, W: r.width, H: r.height};
		})()`))
		if err != nil {
			t.Fatalf("measure %s: %v", selector, err)
		}
		return b
	}

	field := measure("#text")
	if err := session.Click(field.X+field.W/2, field.Y+field.H/2); err != nil {
		t.Fatalf("click field: %v", err)
	}
	if focused, err := session.EditableFocus(session.Context()); err != nil || !focused {
		t.Fatalf("EditableFocus = %v, %v", focused, err)
	}
	// Composed CJK text must arrive as one insertion, not as per-key events.
	if err := session.InsertText("你好abc"); err != nil {
		t.Fatalf("InsertText: %v", err)
	}
	if got := evalPageString(t, session, `document.querySelector("#text").value`); got != "你好abc" {
		t.Fatalf("value after typing = %q", got)
	}
	if err := session.KeyPress("Backspace"); err != nil {
		t.Fatalf("Backspace: %v", err)
	}
	if got := evalPageString(t, session, `document.querySelector("#text").value`); got != "你好ab" {
		t.Fatalf("value after Backspace = %q", got)
	}
	if err := session.BlurFocused(session.Context()); err != nil {
		t.Fatalf("BlurFocused: %v", err)
	}
	if focused, err := session.EditableFocus(session.Context()); err != nil || focused {
		t.Fatalf("focus after blur = %v, %v", focused, err)
	}

	slider := measure("#range")
	midY := slider.Y + slider.H/2
	if err := session.MouseDown(slider.X+2, midY); err != nil {
		t.Fatalf("MouseDown: %v", err)
	}
	for _, frac := range []float64{0.25, 0.5, 0.75, 0.97} {
		if err := session.MouseMove(slider.X+slider.W*frac, midY); err != nil {
			t.Fatalf("MouseMove: %v", err)
		}
	}
	if err := session.MouseUp(slider.X+slider.W*0.97, midY); err != nil {
		t.Fatalf("MouseUp: %v", err)
	}
	value, err := chromedp.Run(session.Context(), chromedp.Evaluate[float64](`Number(document.querySelector("#range").value)`))
	if err != nil || value < 80 {
		t.Fatalf("slider value after drag = %v, %v", value, err)
	}
}

func evalPageString(t *testing.T, session *Session, expression string) string {
	t.Helper()
	value, err := chromedp.Run(session.Context(), chromedp.Evaluate[string](expression))
	if err != nil {
		t.Fatalf("eval %s: %v", expression, err)
	}
	return value
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
