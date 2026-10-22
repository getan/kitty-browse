package render

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"

	"github.com/kenshaw/rasterm"
)

// kittyEncoder keeps rasterm's capability detection but uses a self-contained
// first packet. rasterm v0.1.17 sends an empty PNG header packet without s/v;
// tuios cannot infer the size from that packet and creates a zero-cell placement.
type kittyEncoder struct {
	rasterm.KittyEncoder
	Cols int
	Rows int
}

func (e kittyEncoder) Encode(w io.Writer, img image.Image) error {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, img); err != nil {
		return err
	}
	data := base64.StdEncoding.EncodeToString(pngData.Bytes())
	b := img.Bounds()
	for start := 0; start < len(data); start += 4096 {
		end := min(start+4096, len(data))
		more := 0
		if end < len(data) {
			more = 1
		}
		var err error
		if start == 0 {
			// Fixed image AND placement IDs replace the old frame in place.
			// q=2 suppresses keyboard-stream replies; C=1 keeps the cursor.
			_, err = fmt.Fprintf(w, "\x1b_Ga=T,f=100,s=%d,v=%d,c=%d,r=%d,i=1,p=1,q=2,C=1,m=%d;%s\x1b\\",
				b.Dx(), b.Dy(), e.Cols, e.Rows, more, data[start:end])
		} else {
			_, err = fmt.Fprintf(w, "\x1b_Gm=%d;%s\x1b\\", more, data[start:end])
		}
		if err != nil {
			return err
		}
	}
	return nil
}
