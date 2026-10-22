package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/getan/kitty-browse/internal/browser"
	"github.com/getan/kitty-browse/internal/control"
	"github.com/getan/kitty-browse/internal/screenshot"
)

// This talks to the SAME live tab shown in the pane; it never starts Chrome
// or reloads a URL, so its screenshots retain login, clicks and scroll state.
func runScreenshot(args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("screenshot", flag.ContinueOnError)
	fs.SetOutput(errOut)
	socket := fs.String("socket", os.Getenv("KITTY_BROWSE_SOCKET"), "running browser's -control-socket path")
	path := fs.String("out", "", "PNG output path (default: timestamped .local/screenshots file)")
	fullPage := fs.Bool("full-page", false, "capture the full scrollable page, without changing viewport or scroll")
	jsonOut := fs.Bool("json", false, "print saved path, dimensions and byte count as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: kitty-browse screenshot -socket PATH [-out FILE.png] [-full-page] [-json]")
	}
	if *socket == "" {
		return fmt.Errorf("specify -socket or KITTY_BROWSE_SOCKET; start the pane browser with -control-socket PATH")
	}
	if *path == "" {
		*path = screenshot.AutoPath(".local/screenshots", *fullPage)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	data, err := control.Capture(ctx, *socket, *fullPage)
	if err != nil {
		return err
	}
	info, err := screenshot.Save(*path, data)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(out).Encode(info)
	}
	_, err = fmt.Fprintf(out, "%s (%dx%d, %d bytes)\n", info.Path, info.Width, info.Height, info.Bytes)
	return err
}

func saveSessionScreenshot(ctx context.Context, sess *browser.Session, path string, fullPage bool) (screenshot.Info, error) {
	data, err := sess.Screenshot(ctx, fullPage)
	if err != nil {
		return screenshot.Info{}, err
	}
	return screenshot.Save(path, data)
}
