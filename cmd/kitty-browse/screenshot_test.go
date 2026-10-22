//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/kitty-browse/internal/control"
	"github.com/getan/kitty-browse/internal/screenshot"
)

func TestScreenshotCommandCapturesLiveBrowser(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "kb-cli-") // Stay below macOS's Unix socket path limit.
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "live.sock")
	server, err := control.Start(socket, func(_ context.Context, fullPage bool) ([]byte, error) {
		height := 10
		if fullPage {
			height = 30
		}
		var buf bytes.Buffer
		err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 20, height)))
		return buf.Bytes(), err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	t.Setenv("KITTY_BROWSE_SOCKET", socket)
	for _, fullPage := range []bool{false, true} {
		path := filepath.Join(dir, "viewport.png")
		args := []string{"-out", path, "-json"}
		wantHeight := 10
		if fullPage {
			path = filepath.Join(dir, "full.png")
			args = []string{"-socket", socket, "-out", path, "-full-page", "-json"}
			wantHeight = 30
		}
		var out bytes.Buffer
		if err := runScreenshot(args, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		var info screenshot.Info
		if err := json.Unmarshal(out.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		if info.Path != path || info.Width != 20 || info.Height != wantHeight || info.Bytes == 0 {
			t.Fatalf("screenshot metadata = %+v", info)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
		if err := runScreenshot(args, io.Discard, io.Discard); err == nil {
			t.Fatal("screenshot command overwrote an existing file")
		}
	}
}

func TestScreenshotCommandNeedsSocket(t *testing.T) {
	t.Setenv("KITTY_BROWSE_SOCKET", "")
	if err := runScreenshot(nil, io.Discard, io.Discard); err == nil {
		t.Fatal("missing socket must be an error, not a new browser launch")
	}
}
