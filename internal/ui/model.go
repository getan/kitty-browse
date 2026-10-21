// Package ui holds the browser chrome state independent of terminal I/O.
package ui

import (
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
	ActionQuit
)

// Action is the result of one key event.
type Action struct {
	Kind ActionKind
	URL  string
}

// Model is the address bar and status state.
type Model struct {
	URL     string
	Address string
	Editing bool
	Status  string
}

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
		}
	}
	return Action{}
}

// AddressTrimmed returns the address without surrounding whitespace.
func (m *Model) AddressTrimmed() string { return strings.TrimSpace(m.Address) }
