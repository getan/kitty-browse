package term

import (
	"bufio"
	"fmt"
	"io"
	"time"
	"unicode/utf8"
)

// EventKind is one parsed terminal input event.
type EventKind int

const (
	KeyText EventKind = iota
	KeyEnter
	KeyBackspace
	KeyEscape
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyAltLeft
	KeyAltRight
	KeyAltF
	KeyCtrlL
	KeyCtrlR
	KeyCtrlC
	KeyCtrlQ
	KeyResize
	KeyMouse
	KeyCellPixels
)

// Event is a key or terminal event.
type Event struct {
	Kind   EventKind
	Rune   rune
	Mouse  MouseEvent
	Pixels CellPixels
}

// MouseEvent is one SGR mouse report.
type MouseEvent struct {
	Button int
	Col    int
	Row    int
	Press  bool
	Motion bool
	Wheel  int // -1 up, 1 down, 0 not a wheel
	WheelX int // -1 left, 1 right; never reinterpret sideways touchpad drift as vertical
}

// ReadEvent reads one key. Escape sequences are recognized; a lone ESC is
// returned after a short grace period so Alt+key and arrow keys still work.
func ReadEvent(r *bufio.Reader) (Event, error) {
	b, err := r.ReadByte()
	if err != nil {
		return Event{}, err
	}
	switch b {
	case '\r', '\n':
		return Event{Kind: KeyEnter}, nil
	case 0x7f, 0x08:
		return Event{Kind: KeyBackspace}, nil
	case 0x03:
		return Event{Kind: KeyCtrlC}, nil
	case 0x11:
		return Event{Kind: KeyCtrlQ}, nil
	case 0x0c:
		return Event{Kind: KeyCtrlL}, nil
	case 0x12:
		return Event{Kind: KeyCtrlR}, nil
	case 0x1b:
		return readEscape(r)
	}
	if b < 0x20 {
		return Event{}, nil
	}
	if b < utf8.RuneSelf {
		return Event{Kind: KeyText, Rune: rune(b)}, nil
	}
	if err := r.UnreadByte(); err != nil {
		return Event{}, err
	}
	ru, _, err := r.ReadRune()
	if err != nil {
		return Event{}, err
	}
	return Event{Kind: KeyText, Rune: ru}, nil
}

func readEscape(r *bufio.Reader) (Event, error) {
	// Many terminals deliver ESC and the rest of the sequence in one read. If
	// nothing follows immediately, wait briefly before treating it as ESC.
	if r.Buffered() == 0 {
		time.Sleep(15 * time.Millisecond)
	}
	if r.Buffered() == 0 {
		return Event{Kind: KeyEscape}, nil
	}
	b, err := r.ReadByte()
	if err != nil {
		return Event{Kind: KeyEscape}, nil
	}
	switch b {
	case '[':
		return readCSI(r)
	case 'O':
		return readSS3(r)
	case 'f':
		return Event{Kind: KeyAltF}, nil
	default:
		if b >= 0x20 && b < utf8.RuneSelf {
			return Event{}, nil // Alt+letter; not a browser shortcut yet.
		}
		return Event{Kind: KeyEscape}, nil
	}
}

func readCSI(r *bufio.Reader) (Event, error) {
	var seq []byte
	for len(seq) < 32 {
		b, err := r.ReadByte()
		if err != nil {
			return Event{}, err
		}
		seq = append(seq, b)
		if b >= 0x40 && b <= 0x7e {
			break
		}
	}
	if len(seq) > 0 && seq[0] == '<' {
		if ev, ok := parseMouse(string(seq)); ok {
			return ev, nil
		}
		return Event{}, nil
	}
	var kind, height, width int
	if _, err := fmt.Sscanf(string(seq), "%d;%d;%dt", &kind, &height, &width); err == nil &&
		(kind == 4 || kind == 6) && height > 0 && width > 0 {
		p := CellPixels{}
		if kind == 4 {
			p.WidthPx, p.HeightPx = width, height
		} else {
			p.CellW, p.CellH = width, height
		}
		return Event{Kind: KeyCellPixels, Pixels: p}, nil
	}
	switch string(seq) {
	case "A":
		return Event{Kind: KeyUp}, nil
	case "B":
		return Event{Kind: KeyDown}, nil
	case "C":
		return Event{Kind: KeyRight}, nil
	case "D":
		return Event{Kind: KeyLeft}, nil
	case "1;3D":
		return Event{Kind: KeyAltLeft}, nil
	case "1;3C":
		return Event{Kind: KeyAltRight}, nil
	}
	return Event{}, nil
}

func parseMouse(seq string) (Event, bool) {
	if len(seq) < 6 || seq[len(seq)-1] != 'M' && seq[len(seq)-1] != 'm' {
		return Event{}, false
	}
	press := seq[len(seq)-1] == 'M'
	var b, x, y int
	if _, err := fmt.Sscanf(seq, "<%d;%d;%d", &b, &x, &y); err != nil {
		return Event{}, false
	}
	m := MouseEvent{Button: b & 3, Col: x, Row: y, Press: press, Motion: b&32 != 0}
	if b&64 != 0 {
		direction := -1
		if b&1 != 0 {
			direction = 1
		}
		if b&2 != 0 {
			m.WheelX = direction
		} else {
			m.Wheel = direction
		}
	}
	return Event{Kind: KeyMouse, Mouse: m}, true
}

func readSS3(r *bufio.Reader) (Event, error) {
	b, err := r.ReadByte()
	if err != nil {
		return Event{}, err
	}
	switch b {
	case 'A':
		return Event{Kind: KeyUp}, nil
	case 'B':
		return Event{Kind: KeyDown}, nil
	case 'C':
		return Event{Kind: KeyRight}, nil
	case 'D':
		return Event{Kind: KeyLeft}, nil
	}
	return Event{}, nil
}

// Parse parses all complete events in data. It is used by tests and callers
// that already buffered a chunk of input.
func Parse(data []byte) ([]Event, error) {
	r := bufio.NewReader(bytesReader(data))
	var out []Event
	for {
		ev, err := ReadEvent(r)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, fmt.Errorf("parse: %w", err)
		}
		out = append(out, ev)
	}
}
