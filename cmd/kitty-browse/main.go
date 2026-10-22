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

	"github.com/getan/kitty-browse/internal/browser"
	"github.com/getan/kitty-browse/internal/history"
	"github.com/getan/kitty-browse/internal/render"
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
	var (
		fps       = flag.Float64("fps", 8, "maximum frames per second")
		quality   = flag.Int("quality", 85, "JPEG quality, 0-100")
		maxWidth  = flag.Int("width", 0, "maximum frame width in pixels; 0 sizes to the pane")
		maxHeight = flag.Int("height", 0, "maximum frame height in pixels; 0 sizes to the pane")
		load      = flag.Duration("load-timeout", 30*time.Second, "page load timeout")
		measure   = flag.Bool("measure-cells", false, "query CSI 14t/16t for exact cell pixel size")
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

	sess, err := browser.Open(ctx, target, *load)
	if err != nil {
		log.Fatal(err)
	}
	defer sess.Close()
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
	width, height := *maxWidth, *maxHeight
	pixels := term.FallbackCellPixels(rows, cols)
	if *measure {
		pixels = term.QueryCellPixels(os.Stdin, os.Stdout, rows, cols)
	}
	debugf("cell pixels=%+v", pixels)
	geom := term.GeometryFor(rows, cols, pixels)
	if width <= 0 {
		width = geom.ContentWidthPx
	}
	if height <= 0 {
		height = geom.ContentHeightPx
	}
	geom.ContentWidthPx, geom.ContentHeightPx = width, height
	debugf("viewport %dx%d geometry=%+v", width, height, geom)
	if err := sess.SetViewport(width, height); err != nil {
		debugf("set viewport error: %v", err)
		screen.Header("kitty-browse: viewport: " + err.Error())
		time.Sleep(2 * time.Second)
		return
	}
	debugf("viewport set")

	stream, err := render.Start(sess.Context(), render.Config{
		FPS: *fps, Quality: *quality, Width: width, Height: height,
	})
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
	defer stream.Stop()
	debugf("render started")

	model := ui.New(sess.URL())
	refreshLists(model, store)
	updatePaneTitle(pane, sess)
	drawChrome(screen, model, store)

	events := make(chan term.Event, 16)
	go readInput(os.Stdin, events)
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	tick := time.NewTicker(time.Second / time.Duration(max(int(*fps), 1)))
	defer tick.Stop()
	var pending image.Image
	var lastImage image.Image
	var lastStill time.Time

	for {
		select {
		case <-ctx.Done():
			debugf("ctx done")
			return
		case <-resize:
			rows, cols = screen.Refresh()
			geom = term.GeometryFor(rows, cols, pixels)
			_ = sess.SetViewport(geom.ContentWidthPx, geom.ContentHeightPx)
			drawChrome(screen, model, store)
		case <-tick.C:
			if model.TextMode {
				continue
			}
			if pending != nil {
				drawFrame(screen, stream, pending, model, store)
				lastImage = pending
				pending = nil
			} else if last := stream.LastFrame(); !last.IsZero() &&
				time.Since(last) > 400*time.Millisecond &&
				time.Since(lastStill) > 2*time.Second {
				if img, err := stream.Still(ctx); err == nil {
					drawFrame(screen, stream, img, model, store)
					lastImage = img
					lastStill = time.Now()
				}
			}
		case ev := <-events:
			debugf("event kind=%d rune=%q mouse=%+v", ev.Kind, ev.Rune, ev.Mouse)
			if ev.Kind == term.KeyMouse {
				handleMouse(sess, geom, ev.Mouse)
				continue
			}
			action := model.Handle(ev)
			debugf("action kind=%d url=%s label=%s", action.Kind, action.URL, action.Label)
			switch action.Kind {
			case ui.ActionQuit:
				debugf("quit action")
				return
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
			case ui.ActionToggleText:
				handleText(screen, stream, model, store, sess, rows, cols, lastImage)
			case ui.ActionTextScroll:
				screen.Text(model.TextView(max(rows-2, 1), cols))
				drawChrome(screen, model, store)
			}
			drawChrome(screen, model, store)
		case img := <-stream.Frames():
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

func handleMouse(sess *browser.Session, geom term.Geometry, m term.MouseEvent) {
	x, y, ok := geom.CellToPixel(m.Col, m.Row)
	if !ok {
		return
	}
	switch {
	case m.Wheel != 0:
		_ = sess.Scroll(x, y, float64(m.Wheel)*float64(geom.CellHeight)*4)
	case m.Press && m.Button == 0:
		_ = sess.Click(x, y)
	}
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
	if img == nil {
		return
	}
	if err := screen.WriteBody(func(w io.Writer) error {
		return stream.Draw(w, img)
	}); err != nil {
		// A broken pipe or a terminal that went away is a normal exit path.
		return
	}
	screen.Footer(model.Footer(store.IsBookmarked(model.URL)))
}

func drawChrome(screen *term.Screen, model *ui.Model, store *history.Store) {
	screen.Header(model.Header())
	screen.Footer(model.Footer(store.IsBookmarked(model.URL)))
}

func handleText(screen *term.Screen, stream *render.Stream, model *ui.Model, store *history.Store,
	sess *browser.Session, rows, cols int, last image.Image) {
	if !model.TextMode {
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
	screen.Text(model.TextView(max(rows-2, 1), cols))
}
