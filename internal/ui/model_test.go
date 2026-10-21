package ui

import (
	"testing"

	"github.com/getan/kitty-browse/internal/browser"
	"github.com/getan/kitty-browse/internal/term"
)

func TestAddressBarAndNavigation(t *testing.T) {
	m := New("https://example.com")
	if a := m.Handle(term.Event{Kind: term.KeyCtrlL}); a.Kind != ActionNone || !m.Editing {
		t.Fatalf("ctrl-l = %+v editing=%v", a, m.Editing)
	}
	// The address starts as the current URL, so replace it first.
	for range "https://example.com" {
		m.Handle(term.Event{Kind: term.KeyBackspace})
	}
	for _, r := range "example.org" {
		m.Handle(term.Event{Kind: term.KeyText, Rune: r})
	}
	a := m.Handle(term.Event{Kind: term.KeyEnter})
	if a.Kind != ActionNavigate || a.URL != "https://example.org" {
		t.Fatalf("enter = %+v", a)
	}
	if m.Editing {
		t.Fatal("address bar still editing after Enter")
	}
}

func TestAddressEscapeAndInvalidURL(t *testing.T) {
	m := New("https://example.com")
	m.Handle(term.Event{Kind: term.KeyCtrlL})
	m.Address = "not a url"
	a := m.Handle(term.Event{Kind: term.KeyEnter})
	if a.Kind != ActionNone || m.Status == "" || !m.Editing {
		t.Fatalf("invalid enter = %+v status=%q editing=%v", a, m.Status, m.Editing)
	}
	m.Handle(term.Event{Kind: term.KeyEscape})
	if m.Editing || m.Address != "https://example.com" {
		t.Fatalf("escape left editing=%v address=%q", m.Editing, m.Address)
	}
}

func TestNavigationKeys(t *testing.T) {
	m := New("https://example.com")
	cases := []struct {
		ev   term.Event
		want ActionKind
	}{
		{term.Event{Kind: term.KeyAltLeft}, ActionBack},
		{term.Event{Kind: term.KeyAltRight}, ActionForward},
		{term.Event{Kind: term.KeyCtrlR}, ActionReload},
		{term.Event{Kind: term.KeyCtrlC}, ActionQuit},
	}
	for _, tc := range cases {
		if got := m.Handle(tc.ev); got.Kind != tc.want {
			t.Errorf("%+v = %v, want %v", tc.ev, got.Kind, tc.want)
		}
	}
}

func TestHintModeSelectsLabel(t *testing.T) {
	m := New("https://example.com")
	if a := m.Handle(term.Event{Kind: term.KeyAltF}); a.Kind != ActionShowHints {
		t.Fatalf("alt-f = %+v", a)
	}
	m.SetHints([]browser.Hint{{Label: "as"}, {Label: "df"}})
	if !m.HintMode {
		t.Fatal("hint mode not entered")
	}
	if a := m.Handle(term.Event{Kind: term.KeyText, Rune: 'a'}); a.Kind != ActionNone {
		t.Fatalf("prefix = %+v", a)
	}
	a := m.Handle(term.Event{Kind: term.KeyText, Rune: 's'})
	if a.Kind != ActionHint || a.Label != "as" || m.HintMode {
		t.Fatalf("selection = %+v hintmode=%v", a, m.HintMode)
	}
}

func TestHintModeEscapeClears(t *testing.T) {
	m := New("https://example.com")
	m.Handle(term.Event{Kind: term.KeyAltF})
	m.SetHints([]browser.Hint{{Label: "a"}})
	if a := m.Handle(term.Event{Kind: term.KeyEscape}); a.Kind != ActionClearHints {
		t.Fatalf("escape = %+v", a)
	}
}

func TestHistoryOverlaySelection(t *testing.T) {
	m := New("https://example.com")
	m.SetLists([]string{"https://a", "https://b"}, nil)
	if a := m.Handle(term.Event{Kind: term.KeyText, Rune: 'H'}); a.Kind != ActionOpenHistory {
		t.Fatalf("open history = %+v", a)
	}
	m.OpenOverlay(OverlayHistory, m.HistoryURLs)
	m.Handle(term.Event{Kind: term.KeyText, Rune: 'j'})
	a := m.Handle(term.Event{Kind: term.KeyEnter})
	if a.Kind != ActionOverlaySelect || a.URL != "https://b" {
		t.Fatalf("select = %+v", a)
	}
}

func TestBookmarkToggle(t *testing.T) {
	m := New("https://example.com")
	a := m.Handle(term.Event{Kind: term.KeyText, Rune: 'b'})
	if a.Kind != ActionToggleBookmark || a.URL != "https://example.com" {
		t.Fatalf("bookmark = %+v", a)
	}
}
