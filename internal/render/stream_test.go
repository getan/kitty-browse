package render

import (
	"bytes"
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

func TestDrawDeletesOldPlacementBeforeEncoding(t *testing.T) {
	var out bytes.Buffer
	s := &Stream{enc: fakeEncoder{available: true, payload: "FRAME"}}
	if err := s.Draw(&out, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "\x1b_Ga=d,d=A,q=2;\x1b\\") || !strings.HasSuffix(got, "FRAME") {
		t.Fatalf("draw = %q", got)
	}
}

func TestDrawIgnoresNilImage(t *testing.T) {
	s := &Stream{enc: fakeEncoder{available: true, payload: "FRAME"}}
	if err := s.Draw(io.Discard, nil); err != nil {
		t.Fatal(err)
	}
}
