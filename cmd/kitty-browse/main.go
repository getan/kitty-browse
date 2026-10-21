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
	"github.com/getan/kitty-browse/internal/render"
	"github.com/getan/kitty-browse/internal/term"
	"github.com/getan/kitty-browse/internal/ui"
)

func main() {
	var (
		fps       = flag.Float64("fps", 8, "maximum frames per second")
		quality   = flag.Int("quality", 85, "JPEG quality, 0-100")
		maxWidth  = flag.Int("width", 0, "maximum frame width in pixels; 0 sizes to the pane")
		maxHeight = flag.Int("height", 0, "maximum frame height in pixels; 0 sizes to the pane")
		load      = flag.Duration("load-timeout", 30*time.Second, "page load timeout")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: %s [flags] [url]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

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

	screen, err := term.Open(os.Stdin, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}
	defer screen.Close()

	rows, cols := screen.Size()
	width, height := *maxWidth, *maxHeight
	pixels := term.QueryCellPixels(os.Stdin, os.Stdout, rows, cols)
	geom := term.GeometryFor(rows, cols, pixels)
	if width <= 0 {
		width = geom.ContentWidthPx
	}
	if height <= 0 {
		height = geom.ContentHeightPx
	}
	geom.ContentWidthPx, geom.ContentHeightPx = width, height
	if err := sess.SetViewport(width, height); err != nil {
		screen.Header("kitty-browse: viewport: " + err.Error())
		time.Sleep(2 * time.Second)
		return
	}

	stream, err := render.Start(sess.Context(), render.Config{
		FPS: *fps, Quality: *quality, Width: width, Height: height,
	})
	if err != nil {
		screen.Header("kitty-browse: " + err.Error())
		time.Sleep(2 * time.Second)
		if errors.Is(err, render.ErrNoGraphics) {
			log.Fatal("this terminal has no Kitty/iTerm2/Sixel graphics support")
		}
		log.Fatal(err)
	}
	defer stream.Stop()

	model := ui.New(sess.URL())
	screen.Header(model.Header())

	events := make(chan term.Event, 16)
	go readInput(os.Stdin, events)
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	tick := time.NewTicker(time.Second / time.Duration(max(int(*fps), 1)))
	defer tick.Stop()
	var pending image.Image

	for {
		select {
		case <-ctx.Done():
			return
		case <-resize:
			rows, cols = screen.Refresh()
			geom = term.GeometryFor(rows, cols, pixels)
			_ = sess.SetViewport(geom.ContentWidthPx, geom.ContentHeightPx)
			screen.Header(model.Header())
		case <-tick.C:
			if pending != nil {
				drawFrame(screen, stream, pending)
				pending = nil
			}
		case ev := <-events:
			if ev.Kind == term.KeyMouse {
				handleMouse(sess, geom, ev.Mouse)
				continue
			}
			action := model.Handle(ev)
			switch action.Kind {
			case ui.ActionQuit:
				return
			case ui.ActionNavigate:
				runNavigation(sess, model, action, *load)
			case ui.ActionBack, ui.ActionForward, ui.ActionReload:
				runNavigation(sess, model, action, *load)
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
			}
			screen.Header(model.Header())
		case img := <-stream.Frames():
			pending = img
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
			return
		}
		events <- ev
	}
}

func runNavigation(sess *browser.Session, model *ui.Model, action ui.Action, timeout time.Duration) {
	_ = sess.ClearHints(sess.Context())
	model.ClearHints()
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
		return
	}
	model.SetURL(sess.URL())
}

func drawFrame(screen *term.Screen, stream *render.Stream, img image.Image) {
	if img == nil {
		return
	}
	if err := screen.WriteBody(func(w io.Writer) error {
		return stream.Draw(w, img)
	}); err != nil {
		// A broken pipe or a terminal that went away is a normal exit path.
		return
	}
}
