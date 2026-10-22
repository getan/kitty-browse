// Package textutil provides terminal-cell-aware operations on Unicode text.
package textutil

import (
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
)

// Sanitize makes s safe to draw as terminal text. Control and formatting
// characters which could affect the terminal or the visual ordering of text
// are replaced with spaces. ZWNJ and ZWJ are retained because they participate
// in legitimate grapheme clusters. Invalid UTF-8 is converted to utf8.RuneError
// by ranging over the string.
func Sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unsafeRune(r) {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// unsafeRune reports code points which must not be emitted verbatim to a
// terminal. Emoji tag characters are retained: although categorized as
// formatting characters, they are part of valid extended emoji sequences.
func unsafeRune(r rune) bool {
	if unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
		return true
	}
	if unicode.In(r, unicode.Cf) {
		switch {
		case r == '\u200c', // ZERO WIDTH NON-JOINER
			r == '\u200d',                          // ZERO WIDTH JOINER
			r >= '\U000e0020' && r <= '\U000e007f': // emoji tag characters
			return false
		default:
			return true
		}
	}
	return false
}

// Width returns the number of terminal cells occupied by s. Callers should
// sanitize untrusted input with Sanitize before measuring or drawing it.
// East Asian ambiguous-width characters use uniseg's standard width of one.
func Width(s string) int {
	return uniseg.StringWidth(s)
}

// Clip returns the longest prefix of whole grapheme clusters whose terminal
// width does not exceed cols. It does not pad or sanitize its input.
func Clip(s string, cols int) string {
	if cols <= 0 || s == "" {
		return ""
	}

	graphemes := uniseg.NewGraphemes(s)
	used, end := 0, 0
	for graphemes.Next() {
		width := graphemes.Width()
		if used+width > cols {
			break
		}
		used += width
		end += len(graphemes.Str())
	}
	return s[:end]
}

// Fit clips s to cols terminal cells and pads the result with spaces so that
// its width is exactly cols. It does not sanitize its input.
func Fit(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	clipped := Clip(s, cols)
	return clipped + strings.Repeat(" ", cols-Width(clipped))
}

// Truncate sanitizes s and, when it is wider than cols, clips it to make room
// for a one-cell ellipsis. It returns an empty string when cols is non-positive.
func Truncate(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	s = Sanitize(s)
	if Width(s) <= cols {
		return s
	}
	return Clip(s, cols-1) + "…"
}

// DropLast removes the final complete grapheme cluster from s. This is useful
// for implementing Backspace without splitting UTF-8, combining sequences, or
// emoji. It does not sanitize its input.
func DropLast(s string) string {
	graphemes := uniseg.NewGraphemes(s)
	offset, lastStart := 0, 0
	found := false
	for graphemes.Next() {
		found = true
		lastStart = offset
		offset += len(graphemes.Str())
	}
	if !found {
		return ""
	}
	return s[:lastStart]
}
