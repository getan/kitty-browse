package term

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"unicode/utf8"

	"github.com/getan/kitty-browse/internal/textutil"
)

func TestTerminalSizeRowsAndColumns(t *testing.T) {
	rows, cols, err := terminalSize(42, func(fd int) (int, int, error) {
		if fd != 42 {
			t.Fatalf("fd = %d", fd)
		}
		return 107, 53, nil // x/term returns width, height.
	})
	if err != nil || rows != 53 || cols != 107 {
		t.Fatalf("size = %d rows, %d cols, %v", rows, cols, err)
	}
	g := GeometryFor(rows, cols, FallbackCellPixels(rows, cols))
	if g.ContentWidthPx != 856 || g.ContentHeightPx != 800 || g.ContentRows != 50 {
		t.Fatalf("viewport = %v", g)
	}
}

func TestTerminalSizeError(t *testing.T) {
	want := errors.New("not a terminal")
	_, _, err := terminalSize(0, func(int) (int, int, error) { return 0, 0, want })
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestChineseStatusFitsCellWidth(t *testing.T) {
	if got := truncate("中文abcdef", 5); got != "中文…" {
		t.Fatalf("Unicode footer truncation = %q", got)
	}
	for width := 0; width < 24; width++ {
		got := truncate("已保存：截图👩🏽‍💻中文.png\x1b[31m", width)
		if !utf8.ValidString(got) || textutil.Width(got) > width {
			t.Fatalf("status overflows %d cells: %q", width, got)
		}
	}
}

func TestWriteBodyDoesNotErasePreviousFrame(t *testing.T) {
	var out bytes.Buffer
	s := &Screen{out: &out}
	for range 2 {
		if err := s.WriteBody(func(w io.Writer) error {
			_, err := io.WriteString(w, "FRAME")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := out.String(); got != "\x1b[3;1HFRAME\x1b[3;1HFRAME" {
		t.Fatalf("body updates = %q", got)
	}
}

func TestChromePreservesANSIAndDoesNotClearBody(t *testing.T) {
	var out bytes.Buffer
	s := &Screen{out: &out, rows: 20, cols: 80}
	s.Chrome([]string{"\x1b[1m tab \x1b[0m", "\x1b[34mnav\x1b[0m"})
	got := out.String()
	want := "\x1b[1;1H\x1b[2K\x1b[1m tab \x1b[0m" +
		"\x1b[2;1H\x1b[2K\x1b[34mnav\x1b[0m"
	if got != want {
		t.Fatalf("chrome = %q, want %q", got, want)
	}
	if bytes.Contains(out.Bytes(), []byte("\x1b[J")) || bytes.Contains(out.Bytes(), []byte("\x1b[3;")) {
		t.Fatalf("chrome cleared or touched page image: %q", got)
	}
}

func TestBodyRowsAndTextExcludeChromeAndFooter(t *testing.T) {
	var out bytes.Buffer
	s := &Screen{out: &out, rows: 6, cols: 20}
	if got := s.BodyRows(); got != 3 {
		t.Fatalf("BodyRows = %d, want 3", got)
	}
	s.Text([]string{"a", "b", "c", "must not reach footer"})
	got := out.String()
	if !bytes.Contains(out.Bytes(), []byte("\x1b[3;1H\x1b[2Ka")) ||
		bytes.Contains(out.Bytes(), []byte("must not reach footer")) ||
		bytes.Contains(out.Bytes(), []byte("\x1b[J")) {
		t.Fatalf("text output = %q", got)
	}
}

func TestQueryPixelsOnlyWritesQueries(t *testing.T) {
	var out bytes.Buffer
	(&Screen{out: &out}).QueryPixels()
	if got := out.String(); got != "\x1b[14t\x1b[16t" {
		t.Fatalf("query = %q", got)
	}
}
