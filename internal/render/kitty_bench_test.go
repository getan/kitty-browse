package render

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"testing"
)

type countingWriter struct {
	bytes  int64
	writes int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.bytes += int64(len(p))
	w.writes++
	return len(p), nil
}

// BenchmarkKittyEncodeScreenshot exercises a representative 856x900 browser
// frame and reports terminal-wire size and write calls as well as encode time.
func BenchmarkKittyEncodeScreenshot(b *testing.B) {
	img := benchmarkScreenshot(b)
	enc := kittyEncoder{Cols: 107, Rows: 50}
	benchmarkEncode(b, func(w io.Writer) error { return enc.Encode(w, img) })
}

func benchmarkScreenshot(b *testing.B) image.Image {
	b.Helper()
	path := os.Getenv("KITTY_BROWSE_BENCH_PNG")
	if path == "" {
		path = "../../.local/screenshots/toolbar-viewport.png"
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			b.Skip("set KITTY_BROWSE_BENCH_PNG to benchmark a representative browser PNG")
		}
		b.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		b.Fatal(err)
	}
	return img
}

func benchmarkEncode(b *testing.B, encode func(io.Writer) error) {
	b.Helper()
	var out countingWriter
	// Scrolling sends a long sequence of equal-sized frames. Warm the encoder
	// so the benchmark measures that steady state, including buffer reuse.
	if err := encode(&out); err != nil {
		b.Fatal(err)
	}
	out = countingWriter{}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := encode(&out); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(out.bytes)/float64(b.N), "wire_B/op")
	b.ReportMetric(float64(out.writes)/float64(b.N), "writes/op")
}

// Frozen pre-optimization transport, so both paths can be measured against
// exactly the same pixels. This code is only linked into benchmark binaries.
func BenchmarkKittyEncodeBaselinePNG(b *testing.B) {
	img := benchmarkScreenshot(b)
	benchmarkEncode(b, func(w io.Writer) error {
		var data bytes.Buffer
		enc := base64.NewEncoder(base64.StdEncoding, &data)
		if err := png.Encode(enc, img); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
		payload := data.String()
		for i := 0; i < len(payload); i += 4096 {
			end, more := min(i+4096, len(payload)), 0
			if end < len(payload) {
				more = 1
			}
			var err error
			if i == 0 {
				_, err = fmt.Fprintf(w, "\x1b_Ga=T,f=100,s=%d,v=%d,c=107,r=50,i=1,p=1,q=2,C=1,m=%d;%s\x1b\\", img.Bounds().Dx(), img.Bounds().Dy(), more, payload[i:end])
			} else {
				_, err = fmt.Fprintf(w, "\x1b_Gm=%d;%s\x1b\\", more, payload[i:end])
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

var _ io.Writer = (*countingWriter)(nil)
