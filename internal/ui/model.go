// Package ui holds the browser chrome state independent of terminal I/O.
package ui

import (
	"fmt"
	"strings"

	"github.com/getan/kitty-browse/internal/browser"
	"github.com/getan/kitty-browse/internal/term"
)

// ActionKind is a command the caller should run against the browser.
type ActionKind int

const (
	ActionNone ActionKind = iota
	ActionNavigate
	ActionBack
	ActionForward
	ActionReload
	ActionShowHints
	ActionHint
	ActionClearHints
	ActionOpenHistory
	ActionOpenBookmarks
	ActionToggleBookmark
	ActionOverlaySelect
	ActionOverlayClose
	ActionQuit
)

// Action is the result of one key event.
type Action struct {
	Kind  ActionKind
	URL   string
	Label string
}

// Model is the address bar and status state.
type Model struct {
	URL     string
	Address string
	Editing bool
	Status  string

	HintMode   bool
	HintBuffer string
	Hints      []browser.Hint

	Overlay      OverlayKind
	OverlayItems []string
	OverlayIndex int
	HistoryURLs  []string
	BookmarkURLs []string
}

// OverlayKind selects the one-line picker.
type OverlayKind int

const (
	OverlayNone OverlayKind = iota
	OverlayHistory
	OverlayBookmarks
)

// New returns a browse-mode model for url.
func New(url string) *Model {
	return &Model{URL: url, Address: url}
}

// Header renders the one-line chrome.
func (m *Model) Header() string {
	if m == nil {
		return ""
	}
	if m.Editing {
		return "URL: " + m.Address
	}
	if m.HintMode {
		return "hint > " + m.HintBuffer
	}
	if m.Overlay != OverlayNone {
		name := "history"
		if m.Overlay == OverlayBookmarks {
			name = "bookmarks"
		}
		if len(m.OverlayItems) == 0 {
			return name + ": empty (Esc)"
		}
		return fmt.Sprintf("%s %d/%d: %s", name, m.OverlayIndex+1, len(m.OverlayItems), m.OverlayItems[m.OverlayIndex])
	}
	line := m.URL
	if m.Status != "" {
		line += "  [" + m.Status + "]"
	}
	return line
}

// SetURL updates the displayed URL after navigation.
func (m *Model) SetURL(url string) {
	m.URL = url
	if !m.Editing {
		m.Address = url
	}
	m.Status = ""
}

// Handle consumes one key event.
func (m *Model) Handle(ev term.Event) Action {
	if m == nil {
		return Action{}
	}
	if m.Editing {
		switch ev.Kind {
		case term.KeyEnter:
			u, err := browser.Normalize(m.Address)
			if err != nil {
				m.Status = "invalid URL"
				return Action{}
			}
			m.Editing = false
			m.Status = ""
			return Action{Kind: ActionNavigate, URL: u}
		case term.KeyEscape:
			m.Editing = false
			m.Address = m.URL
			m.Status = ""
		case term.KeyBackspace:
			r := []rune(m.Address)
			if len(r) > 0 {
				m.Address = string(r[:len(r)-1])
			}
		case term.KeyText:
			m.Address += string(ev.Rune)
		}
		return Action{}
	}

	if m.Overlay != OverlayNone {
		switch ev.Kind {
		case term.KeyEscape:
			m.Overlay = OverlayNone
			return Action{Kind: ActionOverlayClose}
		case term.KeyDown, term.KeyText:
			if ev.Kind == term.KeyDown || ev.Rune == 'j' {
				if m.OverlayIndex+1 < len(m.OverlayItems) {
					m.OverlayIndex++
				}
			} else if ev.Rune == 'k' {
				if m.OverlayIndex > 0 {
					m.OverlayIndex--
				}
			}
		case term.KeyUp:
			if m.OverlayIndex > 0 {
				m.OverlayIndex--
			}
		case term.KeyEnter:
			if len(m.OverlayItems) == 0 {
				m.Overlay = OverlayNone
				return Action{Kind: ActionOverlayClose}
			}
			url := m.OverlayItems[m.OverlayIndex]
			m.Overlay = OverlayNone
			return Action{Kind: ActionOverlaySelect, URL: url}
		}
		return Action{}
	}

	if ev.Kind == term.KeyAltF {
		if m.HintMode {
			m.HintMode = false
			m.HintBuffer = ""
			m.Hints = nil
			return Action{Kind: ActionClearHints}
		}
		m.HintMode = true
		m.HintBuffer = ""
		return Action{Kind: ActionShowHints}
	}
	if m.HintMode {
		switch ev.Kind {
		case term.KeyEscape:
			m.HintMode = false
			m.HintBuffer = ""
			m.Hints = nil
			return Action{Kind: ActionClearHints}
		case term.KeyText:
			m.HintBuffer += string(ev.Rune)
			for _, h := range m.Hints {
				if h.Label == m.HintBuffer {
					label := h.Label
					m.HintMode = false
					m.HintBuffer = ""
					m.Hints = nil
					return Action{Kind: ActionHint, Label: label}
				}
			}
			// Keep a valid prefix; otherwise start a new label with this key.
			valid := false
			for _, h := range m.Hints {
				if strings.HasPrefix(h.Label, m.HintBuffer) {
					valid = true
					break
				}
			}
			if !valid {
				m.HintBuffer = string(ev.Rune)
			}
		}
		return Action{}
	}

	switch ev.Kind {
	case term.KeyCtrlL:
		m.Editing = true
		m.Address = m.URL
		m.Status = ""
	case term.KeyCtrlR:
		return Action{Kind: ActionReload}
	case term.KeyCtrlC, term.KeyCtrlQ:
		return Action{Kind: ActionQuit}
	case term.KeyAltLeft, term.KeyLeft:
		return Action{Kind: ActionBack}
	case term.KeyAltRight, term.KeyRight:
		return Action{Kind: ActionForward}
	case term.KeyEscape:
		return Action{Kind: ActionQuit}
	case term.KeyText:
		switch ev.Rune {
		case 'q':
			return Action{Kind: ActionQuit}
		case 'r':
			return Action{Kind: ActionReload}
		case 'l':
			m.Editing = true
			m.Address = m.URL
		case 'H':
			if len(m.HistoryURLs) == 0 {
				m.Status = "history empty"
				return Action{}
			}
			return Action{Kind: ActionOpenHistory}
		case 'B':
			if len(m.BookmarkURLs) == 0 {
				m.Status = "bookmarks empty"
				return Action{}
			}
			return Action{Kind: ActionOpenBookmarks}
		case 'b':
			return Action{Kind: ActionToggleBookmark, URL: m.URL}
		}
	}
	return Action{}
}

// SetHints installs a fresh page overlay and enters hint mode.
func (m *Model) SetHints(hints []browser.Hint) {
	m.Hints = hints
	m.HintBuffer = ""
	if len(hints) == 0 {
		m.HintMode = false
		m.Status = "no hints"
		return
	}
	m.HintMode = true
	m.Status = ""
}

// ClearHints leaves hint mode.
func (m *Model) ClearHints() {
	m.HintMode = false
	m.HintBuffer = ""
	m.Hints = nil
}

// SetLists refreshes the persisted history and bookmark lists.
func (m *Model) SetLists(history, bookmarks []string) {
	m.HistoryURLs = append([]string(nil), history...)
	m.BookmarkURLs = append([]string(nil), bookmarks...)
}

// OpenOverlay shows one picker.
func (m *Model) OpenOverlay(kind OverlayKind, items []string) {
	m.Overlay = kind
	m.OverlayItems = append([]string(nil), items...)
	m.OverlayIndex = 0
}

// CloseOverlay leaves any picker.
func (m *Model) CloseOverlay() {
	m.Overlay = OverlayNone
	m.OverlayItems = nil
	m.OverlayIndex = 0
}

// AddressTrimmed returns the address without surrounding whitespace.
func (m *Model) AddressTrimmed() string { return strings.TrimSpace(m.Address) }
