// Command kitty-browse draws a real Chromium page in a terminal that supports
// the Kitty graphics protocol (or iTerm2/Sixel through rasterm).
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/getan/kitty-browse/internal/browser"
	"github.com/getan/kitty-browse/internal/control"
	"github.com/getan/kitty-browse/internal/history"
	"github.com/getan/kitty-browse/internal/render"
	"github.com/getan/kitty-browse/internal/screenshot"
	"github.com/getan/kitty-browse/internal/term"
	"github.com/getan/kitty-browse/internal/tuios"
	"github.com/getan/kitty-browse/internal/ui"
)

var debugLog *log.Logger

func debugf(format string, args ...any) {
	if debugLog != nil {
		debugLog.Printf(format, args...)
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "screenshot" {
		if err := runScreenshot(os.Args[2:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
			log.Fatal(err)
		}
		return
	}
	var (
		fps       = flag.Float64("fps", 8, "maximum frames per second")
		quality   = flag.Int("quality", 85, "JPEG quality, 0-100")
		maxWidth  = flag.Int("width", 0, "maximum frame width in pixels; 0 sizes to the pane")
		maxHeight = flag.Int("height", 0, "maximum frame height in pixels; 0 sizes to the pane")
		load      = flag.Duration("load-timeout", 30*time.Second, "page load timeout")
		measure   = flag.Bool("measure-cells", true, "query cell pixel size asynchronously (disable with -measure-cells=false)")
		chrome    = flag.String("chrome", os.Getenv("KITTY_BROWSE_CHROME"), "Chromium executable path (or KITTY_BROWSE_CHROME)")
		socket    = flag.String("control-socket", os.Getenv("KITTY_BROWSE_SOCKET"), "optional Unix socket for screenshots of this running browser")
		shotDir   = flag.String("screenshot-dir", ".local/screenshots", "directory for s/S screenshot hotkeys")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: %s [flags] [url]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if path := os.Getenv("KITTY_BROWSE_DEBUG"); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			debugLog = log.New(f, "", log.Lmicroseconds)
			defer f.Close()
		}
	}
	debugf("start target=%q", flag.Arg(0))

	target := strings.TrimSpace(flag.Arg(0))
	if target == "" {
		target = "https://example.com"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	browserCtx := ctx
	if *chrome != "" {
		opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(*chrome))
		allocCtx, closeAlloc := chromedp.NewExecAllocator(ctx, opts...)
		defer closeAlloc()
		browserCtx = allocCtx
	}
	debugf("starting browser executable=%q", *chrome)
	tabs, err := browser.OpenTabs(browserCtx, target, *load)
	if err != nil {
		log.Fatal(err)
	}
	defer tabs.Close()
	sess := tabs.Active()
	debugf("browser open url=%s", sess.URL())

	pane := tuios.FromEnv(nil)
	defer pane.Clear()

	statePath, pathErr := history.DefaultPath()
	store, err := history.Open(statePath)
	if pathErr != nil || err != nil {
		store, _ = history.Open("")
	}
	_ = store.Record(sess.URL())

	screen, err := term.Open(os.Stdin, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}
	defer screen.Close()
	debugf("screen open")

	rows, cols := screen.Size()
	pixels := term.TerminalCellPixels(os.Stdin, rows, cols)
	debugf("cell pixels=%+v", pixels)
	geom := paneGeometry(rows, cols, pixels, *maxWidth, *maxHeight)
	debugf("viewport geometry=%+v", geom)
	if err := sess.SetViewport(geom.ContentWidthPx, geom.ContentHeightPx); err != nil {
		debugf("set viewport error: %v", err)
		screen.Header("kitty-browse: viewport: " + err.Error())
		time.Sleep(2 * time.Second)
		return
	}
	debugf("viewport set")

	stream, err := startTabStream(sess, geom, *fps, *quality)
	if err != nil {
		debugf("render start error: %v", err)
		screen.Header("kitty-browse: " + err.Error())
		time.Sleep(2 * time.Second)
		screen.Close()
		if errors.Is(err, render.ErrNoGraphics) {
			log.Fatal("this terminal has no Kitty/iTerm2/Sixel graphics support")
		}
		log.Fatal(err)
	}
	defer func() { stream.Stop() }()
	defer func() { _ = screen.WriteBody(stream.Clear) }()
	debugf("render started")

	if *socket != "" {
		server, err := control.Start(*socket, tabs.Screenshot)
		if err != nil {
			debugf("control socket error: %v", err)
			screen.Close()
			log.Print(err)
			return
		}
		defer server.Close()
		debugf("screenshot control socket=%s", *socket)
	}

	model := ui.New(sess.URL())
	tabModels := []*ui.Model{model}
	titles := map[*browser.Session]string{}
	cacheTabTitle(titles, sess)
	syncTabChrome(model, tabs, titles)
	refreshLists(model, store)
	updatePaneTitle(pane, sess)
	drawChrome(screen, model, store)

	events := make(chan term.Event, 16)
	go readInput(os.Stdin, events)
	if *measure {
		screen.QueryPixels()
	}
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	tick := time.NewTicker(time.Second / time.Duration(max(int(*fps), 1)))
	defer tick.Stop()
	stateTick := time.NewTicker(time.Second)
	defer stateTick.Stop()
	var pending image.Image
	var lastImage image.Image
	var lastStill time.Time
	var lastStillFor time.Time
	var haveStill bool
	var reportedRenderError bool
	var receivedFrames int

	applyViewport := func() {
		geom = paneGeometry(rows, cols, pixels, *maxWidth, *maxHeight)
		stream.SetViewportSize(geom.ContentWidthPx, geom.ContentHeightPx)
		stream.SetCellSize(cols, geom.ContentRows)
		if err := sess.SetViewport(geom.ContentWidthPx, geom.ContentHeightPx); err != nil {
			model.Status = err.Error()
			debugf("viewport error: %v", err)
		}
		pending = nil
		haveStill = false
		debugf("updated geometry=%+v cell pixels=%+v", geom, pixels)
		if model.TextMode {
			screen.Text(model.TextView(screen.BodyRows(), cols))
		}
		drawChrome(screen, model, store)
	}
	activateTab := func() error {
		stream.Stop()
		sess = tabs.Active()
		model = tabModels[tabs.Index()]
		if err := sess.SetViewport(geom.ContentWidthPx, geom.ContentHeightPx); err != nil {
			return err
		}
		next, err := startTabStream(sess, geom, *fps, *quality)
		if err != nil {
			return err
		}
		stream = next
		pending, lastImage = nil, nil
		lastStill, lastStillFor = time.Time{}, time.Time{}
		haveStill, reportedRenderError = false, false
		receivedFrames = 0
		cacheTabTitle(titles, sess)
		syncTabChrome(model, tabs, titles)
		refreshLists(model, store)
		updatePaneTitle(pane, sess)
		if model.TextMode {
			screen.Text(model.TextView(screen.BodyRows(), cols))
		}
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			debugf("ctx done")
			return
		case <-resize:
			rows, cols = screen.Refresh()
			pixels.WidthPx, pixels.HeightPx = cols*pixels.CellW, rows*pixels.CellH
			applyViewport()
			if *measure {
				screen.QueryPixels()
			}
		case <-stateTick.C:
			oldTitle, oldBack, oldForward := titles[sess], model.CanGoBack, model.CanGoForward
			if title, err := sess.Sync(ctx); err == nil {
				titles[sess] = title
				changedURL := model.URL != sess.URL()
				if changedURL {
					model.SetURL(sess.URL())
					recordVisit(store, model, sess.URL())
				}
				syncTabChrome(model, tabs, titles)
				if changedURL || title != oldTitle || oldBack != model.CanGoBack || oldForward != model.CanGoForward {
					updatePaneTitle(pane, sess)
					drawChrome(screen, model, store)
				}
			}
		case <-tick.C:
			if err := stream.Err(); err != nil && !reportedRenderError {
				debugf("render transport error: %v", err)
				model.Status = err.Error()
				drawChrome(screen, model, store)
				reportedRenderError = true
			}
			if model.TextMode {
				continue
			}
			if pending != nil {
				drawFrame(screen, stream, pending, model, store)
				lastImage = pending
				pending = nil
			} else if last := stream.LastFrame(); shouldCaptureStill(time.Now(), last, lastStill, lastStillFor, haveStill) {
				// Also seed an initial frame when screencast produces no event.
				// Throttle attempts on failure as well as success.
				lastStill = time.Now()
				if img, err := stream.Still(ctx); err != nil {
					debugf("still capture error: %v", err)
					model.Status = err.Error()
					drawChrome(screen, model, store)
				} else {
					debugf("still frame %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
					drawFrame(screen, stream, img, model, store)
					lastImage = img
					lastStillFor, haveStill = last, true
				}
			}
		case ev := <-events:
			debugf("event kind=%d rune=%q mouse=%+v", ev.Kind, ev.Rune, ev.Mouse)
			if ev.Kind == term.KeyCellPixels {
				if next := mergeCellPixels(pixels, ev.Pixels, rows, cols); next != pixels {
					pixels = next
					applyViewport()
				}
				continue
			}
			var action ui.Action
			if ev.Kind == term.KeyMouse {
				if ev.Mouse.Row <= term.ChromeRows {
					action = model.HandleMouse(cols, ev.Mouse)
				} else {
					if err := handleMouse(sess, geom, ev.Mouse); err != nil {
						model.Status = err.Error()
						debugf("mouse error: %v", err)
					}
					drawChrome(screen, model, store)
					continue
				}
			} else {
				action = model.Handle(ev)
			}
			debugf("action kind=%d url=%s label=%s", action.Kind, action.URL, action.Label)
			switch action.Kind {
			case ui.ActionQuit:
				debugf("quit action")
				return
			case ui.ActionNewTab:
				if err := tabs.New(ctx, "about:blank", *load); err != nil {
					model.Status = err.Error()
				} else {
					fresh := ui.New("about:blank")
					fresh.Editing, fresh.Address = true, ""
					tabModels = append(tabModels, fresh)
					if err := activateTab(); err != nil {
						debugf("activate new tab: %v", err)
						return
					}
				}
			case ui.ActionSelectTab:
				if action.Index != tabs.Index() {
					if err := tabs.Select(action.Index); err != nil {
						model.Status = err.Error()
					} else if err := activateTab(); err != nil {
						debugf("select tab: %v", err)
						return
					}
				}
			case ui.ActionCloseTab:
				if err := tabs.CloseTab(action.Index); err != nil {
					model.Status = err.Error()
				} else {
					tabModels = append(tabModels[:action.Index], tabModels[action.Index+1:]...)
					if err := activateTab(); err != nil {
						debugf("activate after close: %v", err)
						return
					}
				}
			case ui.ActionNavigate:
				if runNavigation(sess, model, action, *load) {
					recordVisit(store, model, sess.URL())
					updatePaneTitle(pane, sess)
				}
			case ui.ActionBack, ui.ActionForward, ui.ActionReload:
				if runNavigation(sess, model, action, *load) {
					recordVisit(store, model, sess.URL())
					updatePaneTitle(pane, sess)
				}
			case ui.ActionShowHints:
				hints, err := sess.Hints(sess.Context(), 49)
				if err != nil {
					model.Status = err.Error()
				} else {
					model.SetHints(hints)
				}
			case ui.ActionClearHints:
				_ = sess.ClearHints(sess.Context())
				model.ClearHints()
			case ui.ActionHint:
				handleHint(sess, model, action.Label)
			case ui.ActionOpenHistory:
				model.OpenOverlay(ui.OverlayHistory, store.History())
			case ui.ActionOpenBookmarks:
				model.OpenOverlay(ui.OverlayBookmarks, store.Bookmarks())
			case ui.ActionToggleBookmark:
				on, err := store.ToggleBookmark(action.URL)
				if err != nil {
					model.Status = err.Error()
				} else if on {
					model.Status = "bookmarked"
				} else {
					model.Status = "bookmark removed"
				}
				refreshLists(model, store)
			case ui.ActionOverlaySelect:
				if runNavigation(sess, model, ui.Action{Kind: ui.ActionNavigate, URL: action.URL}, *load) {
					recordVisit(store, model, sess.URL())
					updatePaneTitle(pane, sess)
				}
			case ui.ActionOverlayClose:
				model.CloseOverlay()
			case ui.ActionScreenshot, ui.ActionScreenshotFull:
				fullPage := action.Kind == ui.ActionScreenshotFull
				info, err := saveSessionScreenshot(ctx, sess, screenshot.AutoPath(*shotDir, fullPage), fullPage)
				if err != nil {
					model.Status = err.Error()
					debugf("screenshot error: %v", err)
				} else {
					model.Status = "saved " + info.Path
					debugf("screenshot saved path=%s size=%dx%d bytes=%d", info.Path, info.Width, info.Height, info.Bytes)
				}
			case ui.ActionToggleText:
				handleText(screen, stream, model, store, sess, rows, cols, lastImage)
			case ui.ActionTextScroll:
				screen.Text(model.TextView(screen.BodyRows(), cols))
				drawChrome(screen, model, store)
			}
			if action.Kind == ui.ActionNavigate || action.Kind == ui.ActionBack || action.Kind == ui.ActionForward || action.Kind == ui.ActionReload || action.Kind == ui.ActionOverlaySelect {
				cacheTabTitle(titles, sess)
			}
			syncTabChrome(model, tabs, titles)
			drawChrome(screen, model, store)
		case img := <-stream.Frames():
			receivedFrames++
			if receivedFrames == 1 {
				debugf("first screencast frame %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
			}
			lastImage = img
			if !model.TextMode {
				pending = img
			}
		}
	}
}

func handleHint(sess *browser.Session, model *ui.Model, label string) {
	var target browser.Hint
	for _, h := range model.Hints {
		if h.Label == label {
			target = h
			break
		}
	}
	_ = sess.ClearHints(sess.Context())
	if target.Label != "" {
		_ = sess.Click(float64(target.CX), float64(target.CY))
	}
	model.ClearHints()
}

func recordVisit(store *history.Store, model *ui.Model, url string) {
	_ = store.Record(url)
	refreshLists(model, store)
}

func refreshLists(model *ui.Model, store *history.Store) {
	model.SetLists(store.History(), store.Bookmarks())
}

func updatePaneTitle(pane *tuios.Pane, sess *browser.Session) {
	if !pane.Enabled() {
		return
	}
	if title, err := sess.Title(sess.Context()); err == nil && title != "" {
		pane.Rename(title)
	}
}

func handleMouse(sess *browser.Session, geom term.Geometry, m term.MouseEvent) error {
	x, y, ok := geom.CellToPixel(m.Col, m.Row)
	if !ok {
		return nil
	}
	switch {
	case m.Wheel != 0:
		return sess.Scroll(x, y, float64(m.Wheel)*float64(geom.ContentHeightPx)/float64(max(geom.ContentRows, 1))*4)
	case m.Press && !m.Motion && m.Button == 0:
		return sess.Click(x, y)
	}
	return nil
}

func readInput(in *os.File, events chan<- term.Event) {
	r := bufio.NewReaderSize(in, 4096)
	for {
		ev, err := term.ReadEvent(r)
		if err != nil {
			debugf("input error: %v", err)
			return
		}
		debugf("raw event kind=%d rune=%q", ev.Kind, ev.Rune)
		events <- ev
	}
}

func runNavigation(sess *browser.Session, model *ui.Model, action ui.Action, timeout time.Duration) bool {
	_ = sess.ClearHints(sess.Context())
	model.ClearHints()
	model.CloseOverlay()
	var err error
	switch action.Kind {
	case ui.ActionNavigate:
		err = sess.Navigate(sess.Context(), action.URL, timeout)
	case ui.ActionBack:
		err = sess.Back(sess.Context(), timeout)
	case ui.ActionForward:
		err = sess.Forward(sess.Context(), timeout)
	case ui.ActionReload:
		err = sess.Reload(sess.Context(), timeout)
	}
	if err != nil {
		model.Status = err.Error()
		return false
	}
	model.SetURL(sess.URL())
	return true
}

func drawFrame(screen *term.Screen, stream *render.Stream, img image.Image, model *ui.Model, store *history.Store) {
	if img == nil || screen.BodyRows() == 0 {
		return
	}
	if err := screen.WriteBody(func(w io.Writer) error {
		return stream.Draw(w, img)
	}); err != nil {
		// A broken pipe or a terminal that went away is a normal exit path.
		debugf("draw frame error: %v", err)
		return
	}
	debugf("drawn frame %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	screen.Footer(model.Footer(store.IsBookmarked(model.URL)))
}

func drawChrome(screen *term.Screen, model *ui.Model, store *history.Store) {
	_, cols := screen.Size()
	screen.Chrome(model.Chrome(cols))
	screen.Footer(model.Footer(store.IsBookmarked(model.URL)))
}

func handleText(screen *term.Screen, stream *render.Stream, model *ui.Model, store *history.Store,
	sess *browser.Session, rows, cols int, last image.Image) {
	if !model.TextMode {
		screen.ClearBody()
		if last != nil {
			drawFrame(screen, stream, last, model, store)
		}
		return
	}
	text, err := sess.Text(sess.Context())
	if err != nil {
		model.Status = err.Error()
		model.TextMode = false
		return
	}
	model.SetText(text)
	_ = screen.WriteBody(stream.Clear)
	screen.Text(model.TextView(screen.BodyRows(), cols))
}

// Upgrade an idle page to PNG once per content change, not once every two
// seconds forever. When screencast is silent from startup, keep the fallback.
func shouldCaptureStill(now, last, attempt, capturedFor time.Time, haveStill bool) bool {
	return (last.IsZero() || now.Sub(last) > 400*time.Millisecond) &&
		now.Sub(attempt) > 2*time.Second &&
		(last.IsZero() || !haveStill || !last.Equal(capturedFor))
}
