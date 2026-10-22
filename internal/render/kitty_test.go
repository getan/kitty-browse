package render

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"io"
	"math/rand/v2"
	"strconv"
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
			for i := 3; i < len(img.Pix); i += 4 {
				img.Pix[i] = 0xff
			}
			var out bytes.Buffer
			if err := (&kittyEncoder{Cols: 107, Rows: 50}).Encode(&out, img); err != nil {
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
					for _, want := range []string{"a=T", "f=24", "o=z", "s=", "v=", "c=107", "r=50", "i=1", "p=1", "q=2", "C=1"} {
						if !strings.Contains(header, want) {
							t.Fatalf("first header %q missing %q", header, want)
						}
					}
					for _, want := range []string{"s=" + strconv.Itoa(size), "v=" + strconv.Itoa(size)} {
						if !strings.Contains(header, want) {
							t.Fatalf("first header %q missing exact size %q", header, want)
						}
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
			zr, err := zlib.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := io.ReadAll(zr)
			if closeErr := zr.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				t.Fatal(err)
			}
			want := make([]byte, 0, size*size*3)
			for i := 0; i < len(img.Pix); i += 4 {
				want = append(want, img.Pix[i:i+3]...)
			}
			if !bytes.Equal(decoded, want) {
				t.Fatalf("reassembled RGB differs: got %d bytes, want %d", len(decoded), len(want))
			}
		})
	}
}

func TestKittyTransparentImageUsesLosslessPNG(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.Pix = []byte{255, 31, 63, 0, 2, 127, 254, 128}
	var out bytes.Buffer
	if err := (&kittyEncoder{Cols: 2, Rows: 1}).Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	header, payload, ok := strings.Cut(strings.TrimPrefix(strings.TrimSuffix(out.String(), "\x1b\\"), "\x1b_G"), ";")
	if !ok || !strings.Contains(header, "f=100") || strings.Contains(header, "o=z") {
		t.Fatalf("header = %q, want uncompressed PNG", header)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	got, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	gotNRGBA, ok := got.(*image.NRGBA)
	if !ok || !bytes.Equal(gotNRGBA.Pix, img.Pix) {
		t.Fatalf("decoded pixels = %#v, want %#v", gotNRGBA, img)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestKittyWriteError(t *testing.T) {
	want := errors.New("broken pipe")
	err := (&kittyEncoder{}).Encode(failingWriter{want}, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if err := (&kittyEncoder{}).Encode(shortWriter{}, image.NewRGBA(image.Rect(0, 0, 1, 1))); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v", err)
	}
}
