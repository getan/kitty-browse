package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitize(t *testing.T) {
	preserved := "中文 e\u0301 👍🏽 👩‍💻 🇨🇳 a\u200cb"
	if got := Sanitize(preserved); got != preserved {
		t.Fatalf("Sanitize changed drawable Unicode:\n got %q\nwant %q", got, preserved)
	}

	unsafe := "\x1b\x00\t\n\r\u007f\u0085\u009b\u061c\u200e\u2028\u2029\u202e\u2066\u2069"
	got := Sanitize(unsafe)
	if got != strings.Repeat(" ", 15) {
		t.Fatalf("Sanitize controls = %q, want 15 spaces", got)
	}
	for _, r := range got {
		if r < ' ' || r == '\u007f' || (r >= '\u0080' && r <= '\u009f') {
			t.Fatalf("Sanitize retained terminal control U+%04X", r)
		}
	}
	if got := Sanitize("\x1b]0;owned\x07"); got != " ]0;owned " {
		t.Fatalf("Sanitize OSC = %q", got)
	}

	invalid := string([]byte{'a', 0xff, 'b'})
	got = Sanitize(invalid)
	if !utf8.ValidString(got) || got != "a\ufffdb" {
		t.Fatalf("Sanitize invalid UTF-8 = %q (valid=%v)", got, utf8.ValidString(got))
	}
}

func TestWidth(t *testing.T) {
	tests := []struct {
		s    string
		want int
	}{
		{"ChineseASCII中文", 16},
		{"百度一下你就知道", 16},
		{"e\u0301", 1},
		{"👍🏽", 2},
		{"👩‍💻", 2},
		{"🇨🇳", 2},
	}
	for _, tt := range tests {
		if got := Width(tt.s); got != tt.want {
			t.Errorf("Width(%q) = %d, want %d", tt.s, got, tt.want)
		}
	}
}

func TestClipWholeGraphemes(t *testing.T) {
	tests := []struct {
		name string
		s    string
		cols int
		want string
	}{
		{"no columns", "中文", 0, ""},
		{"negative columns", "abc", -1, ""},
		{"mixed gap", "ab中文cd", 5, "ab中"},
		{"combining", "e\u0301x", 1, "e\u0301"},
		{"skin tone too narrow", "👍🏽x", 1, ""},
		{"skin tone", "👍🏽x", 2, "👍🏽"},
		{"ZWJ", "👩‍💻x", 2, "👩‍💻"},
		{"flag", "🇨🇳x", 2, "🇨🇳"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Clip(tt.s, tt.cols)
			if got != tt.want {
				t.Fatalf("Clip(%q, %d) = %q, want %q", tt.s, tt.cols, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("Clip returned invalid UTF-8: %q", got)
			}
			if tt.cols >= 0 && Width(got) > tt.cols {
				t.Fatalf("Clip overflow: Width(%q) = %d > %d", got, Width(got), tt.cols)
			}
		})
	}
}

func TestFit(t *testing.T) {
	tests := []struct {
		s    string
		cols int
		want string
	}{
		{"中文", 5, "中文 "},
		{"👩‍💻x", 3, "👩‍💻x"},
		{"👍🏽", 1, " "},
		{"anything", 0, ""},
	}
	for _, tt := range tests {
		got := Fit(tt.s, tt.cols)
		if got != tt.want {
			t.Errorf("Fit(%q, %d) = %q, want %q", tt.s, tt.cols, got, tt.want)
		}
		if Width(got) != max(tt.cols, 0) {
			t.Errorf("Width(Fit(%q, %d)) = %d", tt.s, tt.cols, Width(got))
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		s    string
		cols int
		want string
	}{
		{"百度一下你就知道", 7, "百度一…"},
		{"中文", 4, "中文"},
		{"中文", 1, "…"},
		{"abc", 0, ""},
		{"a\x1bb", 3, "a b"},
		{"abcd\x1b[31m", 4, "abc…"},
	}
	for _, tt := range tests {
		got := Truncate(tt.s, tt.cols)
		if got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.s, tt.cols, got, tt.want)
		}
		if tt.cols >= 0 && Width(got) > tt.cols {
			t.Errorf("Truncate overflow: Width(%q) = %d > %d", got, Width(got), tt.cols)
		}
	}
}

func TestDropLast(t *testing.T) {
	tests := []struct {
		s, want string
	}{
		{"", ""},
		{"abc", "ab"},
		{"xe\u0301", "x"},
		{"x👍🏽", "x"},
		{"x👨‍👩‍👧‍👦", "x"},
		{"x🇨🇳", "x"},
		{"中文", "中"},
	}
	for _, tt := range tests {
		if got := DropLast(tt.s); got != tt.want {
			t.Errorf("DropLast(%q) = %q, want %q", tt.s, got, tt.want)
		}
	}
}
