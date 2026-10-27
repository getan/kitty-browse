package render

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"strconv"

	"github.com/kenshaw/rasterm"
)

// kittyEncoder keeps rasterm's capability detection but uses a self-contained
// first packet. rasterm v0.1.17 sends an empty PNG header packet without s/v;
// tuios cannot infer the size from that packet and creates a zero-cell placement.
type kittyEncoder struct {
	rasterm.KittyEncoder
	Cols int
	Rows int
	// force is set when the terminal itself answered the graphics query.
	// rasterm's own answer is environment-only, so it cannot see a
	// multiplexer that forwards kitty graphics under a plain TERM.
	force bool

	// Encode is called under Stream.encMu. Keep the large frame buffers here so a
	// scrolling page does not allocate several megabytes for every frame.
	rgb        []byte
	compressed bytes.Buffer
	zlibWriter *zlib.Writer
	encoded    []byte
	wire       []byte
}

func (e *kittyEncoder) Available() bool {
	return e.force || e.KittyEncoder.Available()
}

func (e *kittyEncoder) Encode(w io.Writer, img image.Image) error {
	b := img.Bounds()

	format := 24
	payload, err := e.rgbPayload(img)
	if err != nil {
		return err
	}
	if payload == nil {
		// Raw RGB cannot represent transparency. Keep that uncommon path
		// lossless, while avoiding the default PNG encoder's slowest setting.
		format = 100
		e.compressed.Reset()
		if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&e.compressed, img); err != nil {
			return err
		}
		payload = e.compressed.Bytes()
	}

	encodedLen := base64.StdEncoding.EncodedLen(len(payload))
	e.encoded = resize(e.encoded, encodedLen)
	base64.StdEncoding.Encode(e.encoded, payload)
	e.buildWire(b.Dx(), b.Dy(), format)
	n, err := w.Write(e.wire)
	if err == nil && n != len(e.wire) {
		err = io.ErrShortWrite
	}
	return err
}

// rgbPayload converts an opaque image to the protocol's packed RGB form and
// zlib-compresses it. A nil payload asks the caller to retain alpha with PNG.
func (e *kittyEncoder) rgbPayload(img image.Image) ([]byte, error) {
	b := img.Bounds()
	e.rgb = resize(e.rgb, b.Dx()*b.Dy()*3)
	dst := e.rgb
	opaque := true

	switch src := img.(type) {
	case *image.RGBA:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			i := src.PixOffset(b.Min.X, y)
			row := src.Pix[i : i+b.Dx()*4]
			for x := 0; x < len(row); x += 4 {
				dst[0], dst[1], dst[2] = row[x], row[x+1], row[x+2]
				opaque = opaque && row[x+3] == 0xff
				dst = dst[3:]
			}
		}
	case *image.NRGBA:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			i := src.PixOffset(b.Min.X, y)
			row := src.Pix[i : i+b.Dx()*4]
			for x := 0; x < len(row); x += 4 {
				dst[0], dst[1], dst[2] = row[x], row[x+1], row[x+2]
				opaque = opaque && row[x+3] == 0xff
				dst = dst[3:]
			}
		}
	case *image.YCbCr:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := src.YCbCrAt(x, y)
				dst[0], dst[1], dst[2] = color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
				dst = dst[3:]
			}
		}
	default:
		opaqueImage, ok := img.(interface{ Opaque() bool })
		if !ok || !opaqueImage.Opaque() {
			return nil, nil
		}
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, blue, _ := img.At(x, y).RGBA()
				dst[0], dst[1], dst[2] = byte(r>>8), byte(g>>8), byte(blue>>8)
				dst = dst[3:]
			}
		}
	}
	if !opaque {
		return nil, nil
	}

	e.compressed.Reset()
	if e.zlibWriter == nil {
		zw, err := zlib.NewWriterLevel(&e.compressed, zlib.BestSpeed)
		if err != nil {
			return nil, err
		}
		e.zlibWriter = zw
	} else {
		e.zlibWriter.Reset(&e.compressed)
	}
	if _, err := e.zlibWriter.Write(e.rgb); err != nil {
		return nil, err
	}
	if err := e.zlibWriter.Close(); err != nil {
		return nil, err
	}
	return e.compressed.Bytes(), nil
}

func (e *kittyEncoder) buildWire(width, height, format int) {
	e.wire = e.wire[:0]
	for start := 0; start < len(e.encoded); start += 4096 {
		end := min(start+4096, len(e.encoded))
		more := end < len(e.encoded)
		if start == 0 {
			// Fixed image AND placement IDs replace the old frame in place.
			// q=2 suppresses replies; C=1 keeps the cursor. Raw RGB is zlib
			// compressed before base64, as required by o=z.
			e.wire = append(e.wire, "\x1b_Ga=T,f="...)
			e.wire = strconv.AppendInt(e.wire, int64(format), 10)
			if format == 24 {
				e.wire = append(e.wire, ",o=z"...)
			}
			e.wire = append(e.wire, ",s="...)
			e.wire = strconv.AppendInt(e.wire, int64(width), 10)
			e.wire = append(e.wire, ",v="...)
			e.wire = strconv.AppendInt(e.wire, int64(height), 10)
			e.wire = append(e.wire, ",c="...)
			e.wire = strconv.AppendInt(e.wire, int64(e.Cols), 10)
			e.wire = append(e.wire, ",r="...)
			e.wire = strconv.AppendInt(e.wire, int64(e.Rows), 10)
			e.wire = append(e.wire, ",i=1,p=1,q=2,C=1,m="...)
		} else {
			e.wire = append(e.wire, "\x1b_Gm="...)
		}
		if more {
			e.wire = append(e.wire, '1')
		} else {
			e.wire = append(e.wire, '0')
		}
		e.wire = append(e.wire, ';')
		e.wire = append(e.wire, e.encoded[start:end]...)
		e.wire = append(e.wire, '\x1b', '\\')
	}
}

func resize(buf []byte, size int) []byte {
	if cap(buf) < size {
		return make([]byte, size)
	}
	return buf[:size]
}
