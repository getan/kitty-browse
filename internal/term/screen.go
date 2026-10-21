// Package term owns the terminal mode and the cell grid around the browser
// image. The image occupies rows 2..N; row 1 is reserved for the address bar.
package term

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	xterm "golang.org/x/term"
)

// Screen switches the terminal into raw mode and an alternate screen.
type Screen struct {
	in  *os.File
	out io.Writer

	mu    sync.Mutex
	state *xterm.State
	rows  int
	cols  int
}

// Open enters the alternate screen and raw mode.
func Open(in *os.File, out io.Writer) (*Screen, error) {
	if !xterm.IsTerminal(int(in.Fd())) {
		return nil, fmt.Errorf("stdin is not a terminal")
	}
	state, err := xterm.MakeRaw(int(in.Fd()))
	if err != nil {
		return nil, fmt.Errorf("raw mode: %w", err)
	}
	rows, cols, err := xterm.GetSize(int(in.Fd()))
	if err != nil {
		_ = xterm.Restore(int(in.Fd()), state)
		return nil, fmt.Errorf("terminal size: %w", err)
	}
	s := &Screen{in: in, out: out, state: state, rows: rows, cols: cols}
	_, _ = io.WriteString(out, "\x1b[?1049h\x1b[?25l\x1b[?1002h\x1b[?1006h\x1b[2J\x1b[H")
	return s, nil
}

// Close restores the cursor, the main screen and the terminal mode.
func (s *Screen) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.state == nil {
		s.mu.Unlock()
		return
	}
	_, _ = io.WriteString(s.out, "\x1b[?1002l\x1b[?1006l\x1b[?25h\x1b[?1049l")
	state := s.state
	s.state = nil
	s.mu.Unlock()
	_ = xterm.Restore(int(s.in.Fd()), state)
}

// Refresh reads the terminal size again.
func (s *Screen) Refresh() (rows, cols int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, c, err := xterm.GetSize(int(s.in.Fd())); err == nil {
		s.rows, s.cols = r, c
	}
	return s.rows, s.cols
}

// Size returns the last known terminal grid.
func (s *Screen) Size() (rows, cols int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows, s.cols
}

// BodyRows is the number of rows available to the page image.
func (s *Screen) BodyRows() int {
	rows, _ := s.Size()
	if rows <= 2 {
		return 1
	}
	return rows - 2
}

// Header draws the one-line address/status bar.
func (s *Screen) Header(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cols := s.cols
	if cols < 1 {
		cols = 1
	}
	_, _ = fmt.Fprintf(s.out, "\x1b[1;1H\x1b[2K%s", truncate(text, cols))
}

// Footer draws the status/help line on the last row.
func (s *Screen) Footer(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows < 1 {
		return
	}
	cols := s.cols
	if cols < 1 {
		cols = 1
	}
	_, _ = fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K%s", s.rows, truncate(text, cols))
}

// WriteBody clears only the browser area and lets draw write an image there.
// Clearing from row 2 leaves the address bar intact; the footer is redrawn by
// the caller after the image.
func (s *Screen) WriteBody(draw func(io.Writer) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := io.WriteString(s.out, "\x1b[2;1H\x1b[J"); err != nil {
		return err
	}
	return draw(s.out)
}

// ClearBody clears the image area.
func (s *Screen) ClearBody() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = io.WriteString(s.out, "\x1b[2;1H\x1b[J")
}

func truncate(s string, cols int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	r := []rune(s)
	if len(r) <= cols {
		return s
	}
	if cols <= 1 {
		return string(r[:cols])
	}
	return string(r[:cols-1]) + "…"
}
