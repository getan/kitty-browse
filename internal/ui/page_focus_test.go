package ui

import (
	"testing"

	"github.com/getan/kitty-browse/internal/term"
)

func TestPageClickReleasesAddressKeyboardFocus(t *testing.T) {
	m := New("https://example.com")
	clickPart(t, m, 80, 2, ActionEditAddress, 0)
	m.Handle(term.Event{Kind: term.KeyText, Rune: 'x'})
	// Motion, releases, wheel and right clicks must not discard an address edit.
	for _, mouse := range []term.MouseEvent{
		{Col: 20, Row: 6, Press: true, Motion: true},
		{Col: 20, Row: 6},
		{Col: 20, Row: 6, Press: true, Wheel: -1},
		{Col: 20, Row: 6, Press: true, Button: 2},
	} {
		m.HandleMouse(80, mouse)
		if !m.Editing || m.Address != "x" {
			t.Fatalf("non-focus event discarded address edit: %+v", mouse)
		}
	}
	m.HandleMouse(80, term.MouseEvent{Col: 20, Row: 6, Press: true})
	if m.Editing || m.Address != m.URL {
		t.Fatalf("page click left the toolbar owning keys: editing=%v address=%q", m.Editing, m.Address)
	}
}
