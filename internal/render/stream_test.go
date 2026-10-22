package render

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"strings"
	"testing"
)

type fakeEncoder struct {
	available bool
	payload   string
}

func (f fakeEncoder) Available() bool { return f.available }
func (f fakeEncoder) Encode(w io.Writer, _ image.Image) error {
	_, err := io.WriteString(w, f.payload)
	return err
}

func TestDrawDoesNotDeleteVisiblePlacement(t *testing.T) {
	var out bytes.Buffer
	s := &Stream{enc: fakeEncoder{available: true, payload: "FRAME"}}
	if err := s.Draw(&out, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if got != "FRAME" {
		t.Fatalf("draw = %q", got)
	}
}

func TestClearTargetsOnlyBrowserImage(t *testing.T) {
	var out bytes.Buffer
	if err := (&Stream{}).Clear(&out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "d=I,i=1") || strings.Contains(got, "d=A") {
		t.Fatalf("clear = %q", got)
	}
}

func TestDrawIgnoresNilImage(t *testing.T) {
	s := &Stream{enc: fakeEncoder{available: true, payload: "FRAME"}}
	if err := s.Draw(io.Discard, nil); err != nil {
		t.Fatal(err)
	}
}

func TestStillRespectsCancelledCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Stream{ctx: context.Background()}
	if _, err := s.Still(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestViewportRejectsScreenshotAndResizeSurfaces(t *testing.T) {
	s := &Stream{}
	s.SetViewportSize(856, 816)
	if !s.matchesViewport(856, 816) || s.matchesViewport(127, 816) || s.matchesViewport(856, 5492) {
		t.Fatal("full-page screenshot surfaces must not become pane frames")
	}
	s.SetViewportSize(640, 480)
	if s.matchesViewport(856, 816) || !s.matchesViewport(640, 480) {
		t.Fatal("resize must update the accepted viewport")
	}
}

func TestSetCellSizeUpdatesKittyPlacement(t *testing.T) {
	k := &kittyEncoder{Cols: 1, Rows: 1}
	s := &Stream{enc: k, kitty: k}
	s.SetCellSize(107, 50)
	var out bytes.Buffer
	if err := s.Draw(&out, image.NewRGBA(image.Rect(0, 0, 8, 18))); err != nil {
		t.Fatal(err)
	}
	header, _, ok := strings.Cut(strings.TrimPrefix(out.String(), "\x1b_G"), ";")
	if !ok || !strings.Contains(header, "c=107") || !strings.Contains(header, "r=50") {
		t.Fatalf("Kitty header = %q", header)
	}
}

func TestSetCellSizeLeavesCustomEncoderAlone(t *testing.T) {
	s := &Stream{enc: fakeEncoder{available: true, payload: "FRAME"}}
	s.SetCellSize(80, 24)
	var out bytes.Buffer
	if err := s.Draw(&out, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil || out.String() != "FRAME" {
		t.Fatalf("custom draw = %q, %v", out.String(), err)
	}
}
