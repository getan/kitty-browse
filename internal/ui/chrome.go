package ui

import (
	"fmt"
	"strings"

	"github.com/getan/kitty-browse/internal/term"
	"github.com/getan/kitty-browse/internal/textutil"
)

// ChromeRows is the number of terminal rows reserved for browser chrome.
const ChromeRows = 2

type chromePart struct {
	text    string
	kind    ActionKind
	index   int
	enabled bool
	active  bool
	start   int // one-based visible columns, inclusive
	end     int // one-based visible columns, inclusive
}

// Chrome renders the tab strip and toolbar. Untrusted text is sanitized, but
// Unicode is preserved; layout and mouse hitboxes use display cells, not bytes.
func (m *Model) Chrome(cols int) []string {
	if cols <= 0 || m == nil {
		return []string{"", ""}
	}
	rows := m.chromeLayout(cols)
	return []string{renderChrome(rows[0], cols), renderChrome(rows[1], cols)}
}

// HandleMouse maps a press to the same parts used by Chrome. Mouse positions
// are one-based SGR terminal coordinates.
func (m *Model) HandleMouse(cols int, mouse term.MouseEvent) Action {
	if m == nil || cols <= 0 || !mouse.Press || mouse.Motion || mouse.Wheel != 0 || mouse.Button != 0 || mouse.Row < 1 || mouse.Row > ChromeRows || mouse.Col < 1 || mouse.Col > cols {
		return Action{}
	}
	parts := m.chromeLayout(cols)[mouse.Row-1]
	for _, part := range parts {
		if mouse.Col < part.start || mouse.Col > part.end || !part.enabled {
			continue
		}
		switch part.kind {
		case ActionEditAddress:
			m.Editing = true
			m.Address = m.URL
			m.Status = ""
			m.replaceAddress = true
			return Action{Kind: ActionEditAddress}
		case ActionNavigate: // The Go button uses the keyboard submission path.
			return m.submitAddress()
		case ActionNone:
			return Action{}
		default:
			return Action{Kind: part.kind, Index: part.index}
		}
	}
	return Action{}
}

func (m *Model) chromeLayout(cols int) [2][]chromePart {
	var rows [2][]chromePart
	if cols <= 0 {
		return rows
	}

	// Leave room for [+], then divide the remaining space equally. Keeping
	// every tab the same width makes both rendering and hit-testing stable.
	tabCount := len(m.Tabs)
	tabWidth := 0
	if tabCount > 0 && cols > 3 {
		tabWidth = (cols - 3) / tabCount
		if tabWidth > 24 {
			tabWidth = 24
		}
	}
	for i := 0; i < tabCount && tabWidth >= 4; i++ {
		// Keep the close target separate: byte slicing a label at a cell
		// offset would corrupt Chinese or split an emoji grapheme.
		rows[0] = appendPart(rows[0], tabBody(m.Tabs[i], i, tabWidth), ActionSelectTab, i, true, i == m.ActiveTab)
		rows[0] = appendPart(rows[0], "x", ActionCloseTab, i, true, i == m.ActiveTab)
		rows[0] = appendPart(rows[0], "]", ActionSelectTab, i, true, i == m.ActiveTab)
	}
	rows[0] = appendPart(rows[0], "[+]", ActionNewTab, 0, true, false)

	rows[1] = appendPart(rows[1], "[<]", ActionBack, 0, m.CanGoBack, false)
	rows[1] = appendPart(rows[1], " ", ActionNone, 0, false, false)
	rows[1] = appendPart(rows[1], "[>]", ActionForward, 0, m.CanGoForward, false)
	rows[1] = appendPart(rows[1], " ", ActionNone, 0, false, false)
	rows[1] = appendPart(rows[1], "[R]", ActionReload, 0, true, false)
	rows[1] = appendPart(rows[1], " ", ActionNone, 0, false, false)

	// Fixed controls consume 23 columns including separators. The address is
	// allowed to disappear in a very narrow pane; clipping remains safe.
	addressWidth := cols - 23
	if addressWidth < 0 {
		addressWidth = 0
	}
	address := m.URL
	if m.Editing {
		address = m.Address
	}
	rows[1] = appendPart(rows[1], textutil.Fit(textutil.Sanitize(address), addressWidth), ActionEditAddress, 0, addressWidth > 0, m.Editing)
	rows[1] = appendPart(rows[1], "[Go]", ActionNavigate, 0, true, false)
	rows[1] = appendPart(rows[1], " ", ActionNone, 0, false, false)
	rows[1] = appendPart(rows[1], "[Shot]", ActionScreenshot, 0, true, false)
	return rows
}

func appendPart(parts []chromePart, text string, kind ActionKind, index int, enabled, active bool) []chromePart {
	if text == "" {
		return parts
	}
	start := 1
	if len(parts) > 0 {
		start = parts[len(parts)-1].end + 1
	}
	return append(parts, chromePart{text: text, kind: kind, index: index, enabled: enabled, active: active, start: start, end: start + textutil.Width(text) - 1})
}

func renderChrome(parts []chromePart, cols int) string {
	var out strings.Builder
	for _, part := range parts {
		if part.start > cols {
			break
		}
		text := part.text
		if n := cols - part.start + 1; textutil.Width(text) > n {
			text = textutil.Clip(text, n)
		}
		bg, fg := "35;40;52", "230;235;245"
		switch {
		case part.active:
			bg, fg = "42;96;153", "255;255;255"
		case !part.enabled && part.kind != ActionNone:
			bg, fg = "35;38;44", "105;112;120"
		case part.kind == ActionEditAddress:
			bg, fg = "22;28;36", "230;235;245"
		}
		fmt.Fprintf(&out, "\x1b[48;2;%sm\x1b[38;2;%sm%s\x1b[0m", bg, fg, text)
	}
	return out.String()
}

func tabBody(tab TabInfo, index, width int) string {
	name := tab.Title
	if name == "" {
		name = tab.URL
	}
	middle := fmt.Sprintf("%d %s", index+1, textutil.Sanitize(name))
	return "[" + textutil.Fit(middle, width-3)
}
