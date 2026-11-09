package render

import (
	"image"
	"io"

	"github.com/kenshaw/rasterm"
)

// ImageWriter shares the pane encoders with a remote session surface without
// launching Chromium or retaining a CDP connection.
type ImageWriter struct{ encoder rasterm.Encoder }

func NewImageWriter(cols, rows int, kitty bool) (*ImageWriter, error) {
	enc := rasterm.NewDefaultEncoder(&kittyEncoder{Cols: cols, Rows: rows, force: kitty}, rasterm.ITermEncoder{NoNewline: true}, rasterm.SixelEncoder{NoNewline: true})
	if !enc.Available() {
		return nil, ErrNoGraphics
	}
	return &ImageWriter{encoder: enc}, nil
}

func (w *ImageWriter) Draw(out io.Writer, img image.Image) error { return w.encoder.Encode(out, img) }
