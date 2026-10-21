// Command kitty-browse draws a real Chromium page in a terminal that supports
// the Kitty graphics protocol (or iTerm2/Sixel through rasterm).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chromedp/termcast"
	"github.com/getan/kitty-browse/internal/browser"
)

func main() {
	var (
		fps       = flag.Float64("fps", 8, "maximum frames per second")
		quality   = flag.Int("quality", 85, "JPEG quality, 0-100")
		maxWidth  = flag.Int("width", 0, "maximum frame width in pixels; 0 uses termcast's default")
		maxHeight = flag.Int("height", 0, "maximum frame height in pixels; 0 uses termcast's default")
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

	opts := []termcast.Option{
		termcast.WithFPS(*fps),
		termcast.WithQuality(*quality),
	}
	if *maxWidth > 0 && *maxHeight > 0 {
		opts = append(opts, termcast.WithMaxSize(*maxWidth, *maxHeight))
	}
	stream, err := termcast.Start(sess.Context(), opts...)
	if err != nil {
		if errors.Is(err, termcast.ErrNoGraphics) {
			log.Fatal("this terminal has no Kitty/iTerm2/Sixel graphics support")
		}
		log.Fatal(err)
	}
	defer stream.Stop()

	log.SetOutput(stream.LogWriter())
	log.Printf("kitty-browse: %s", sess.URL())

	<-ctx.Done()
}
