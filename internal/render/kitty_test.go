package render

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"math/rand/v2"
	"strings"
	"testing"
)

func TestKittyPackets(t *testing.T) {
	for _, size := range []int{1, 128} {
		t.Run(image.Pt(size, size).String(), func(t *testing.T) {
			img := image.NewRGBA(image.Rect(0, 0, size, size))
			rng := rand.New(rand.NewPCG(1, 2))
			for i := range img.Pix {
				img.Pix[i] = byte(rng.Uint32())
			}
			var out bytes.Buffer
			if err := (kittyEncoder{Cols: 107, Rows: 50}).Encode(&out, img); err != nil {
				t.Fatal(err)
			}
			packets := strings.Split(strings.TrimSuffix(out.String(), "\x1b\\"), "\x1b\\")
			if size == 128 && len(packets) < 2 {
				t.Fatal("expected a multi-chunk image")
			}
			var payload strings.Builder
			for i, packet := range packets {
				if !strings.HasPrefix(packet, "\x1b_G") {
					t.Fatalf("not a Kitty packet: %q", packet)
				}
				header, data, ok := strings.Cut(strings.TrimPrefix(packet, "\x1b_G"), ";")
				if !ok || len(data) == 0 || len(data) > 4096 {
					t.Fatalf("invalid chunk %d: header=%q data length=%d", i, header, len(data))
				}
				if i == 0 {
					for _, want := range []string{"a=T", "f=100", "s=", "v=", "c=107", "r=50", "i=1", "p=1", "q=2", "C=1"} {
						if !strings.Contains(header, want) {
							t.Fatalf("first header %q missing %q", header, want)
						}
					}
					// tuios must be able to read PNG IHDR from the FIRST chunk.
					first, err := base64.StdEncoding.DecodeString(data)
					if err != nil {
						t.Fatal(err)
					}
					cfg, err := png.DecodeConfig(bytes.NewReader(first))
					if err != nil || cfg.Width != size || cfg.Height != size {
						t.Fatalf("first chunk PNG size: %+v, %v", cfg, err)
					}
				}
				wantMore := "m=1"
				if i == len(packets)-1 {
					wantMore = "m=0"
				}
				if !strings.HasSuffix(header, wantMore) {
					t.Fatalf("chunk %d header = %q, want %s", i, header, wantMore)
				}
				payload.WriteString(data)
			}
			raw, err := base64.StdEncoding.DecodeString(payload.String())
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := png.Decode(bytes.NewReader(raw))
			if err != nil || decoded.Bounds() != img.Bounds() {
				t.Fatalf("reassembled PNG: %v, %v", decoded, err)
			}
		})
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestKittyWriteError(t *testing.T) {
	want := errors.New("broken pipe")
	err := (kittyEncoder{}).Encode(failingWriter{want}, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}
