// Package term owns the terminal mode and the cell grid around the browser
// image. The image starts after two chrome rows and ends before the footer.
package term

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	xterm "golang.org/x/term"
)

// ChromeRows is the fixed tab/navigation chrome above the page image.
const ChromeRows = 2

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
	rows, cols, err := terminalSize(int(in.Fd()), xterm.GetSize)
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
	if r, c, err := terminalSize(int(s.in.Fd()), xterm.GetSize); err == nil {
		s.rows, s.cols = r, c
	}
	return s.rows, s.cols
}

// x/term returns width, height (columns, rows), unlike Screen's row-first API.
func terminalSize(fd int, getSize func(int) (int, int, error)) (rows, cols int, err error) {
	cols, rows, err = getSize(fd)
	return
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
	body := rows - ChromeRows - 1
	if body < 0 {
		return 0
	}
	return body
}

// QueryPixels asynchronously requests pane and cell pixel dimensions. Replies
// are consumed by ReadEvent, so this method never competes for stdin.
func (s *Screen) QueryPixels() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = io.WriteString(s.out, "\x1b[14t\x1b[16t")
}

// Chrome draws exactly the two styled chrome lines. ANSI escapes are preserved
// verbatim; callers are responsible for keeping their visible width in bounds.
func (s *Screen) Chrome(lines []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < ChromeRows; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		_, _ = fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K%s", i+1, line)
	}
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

// WriteBody positions the image without clearing the previous frame. Erasing
// here makes tuios remove the visible placement before the replacement arrives.
func (s *Screen) WriteBody(draw func(io.Writer) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.out, "\x1b[%d;1H", ChromeRows+1); err != nil {
		return err
	}
	return draw(s.out)
}

// ClearBody clears the image area.
func (s *Screen) ClearBody() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearBodyLocked()
}

// Text clears the page area and writes plain text lines. The caller keeps the
// footer in sync.
func (s *Screen) Text(lines []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearBodyLocked()
	cols := s.cols
	if cols < 1 {
		cols = 1
	}
	for i := 0; i < s.rows-ChromeRows-1 && i < len(lines); i++ {
		_, _ = fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K%s", i+ChromeRows+1, truncate(lines[i], cols))
	}
}

func (s *Screen) clearBodyLocked() {
	for row := ChromeRows + 1; row < s.rows; row++ {
		_, _ = fmt.Fprintf(s.out, "\x1b[%d;1H\x1b[2K", row)
	}
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
