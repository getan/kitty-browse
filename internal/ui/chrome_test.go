package ui

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/getan/kitty-browse/internal/term"
	"github.com/getan/kitty-browse/internal/textutil"
)

var ansiColor = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func clickPart(t *testing.T, m *Model, cols, row int, kind ActionKind, index int) Action {
	t.Helper()
	for _, p := range m.chromeLayout(cols)[row-1] {
		if p.kind == kind && p.index == index && p.enabled {
			return m.HandleMouse(cols, term.MouseEvent{Button: 0, Col: p.start, Row: row, Press: true})
		}
	}
	t.Fatalf("no enabled part kind=%v index=%d", kind, index)
	return Action{}
}

func TestChromeTabMouseTargets(t *testing.T) {
	m := New("https://one.example")
	m.Tabs = []TabInfo{{Title: "one"}, {Title: "two"}}
	m.ActiveTab = 1
	if got := clickPart(t, m, 80, 1, ActionSelectTab, 0); got.Kind != ActionSelectTab || got.Index != 0 {
		t.Fatalf("select = %+v", got)
	}
	if got := clickPart(t, m, 80, 1, ActionCloseTab, 1); got.Kind != ActionCloseTab || got.Index != 1 {
		t.Fatalf("close = %+v", got)
	}
	if got := clickPart(t, m, 80, 1, ActionNewTab, 0); got.Kind != ActionNewTab {
		t.Fatalf("new = %+v", got)
	}
}

func TestChromeToolbarMouseTargets(t *testing.T) {
	m := New("https://old.example")
	m.CanGoBack, m.CanGoForward = true, true
	for _, kind := range []ActionKind{ActionBack, ActionForward, ActionReload, ActionScreenshot} {
		if got := clickPart(t, m, 80, 2, kind, 0); got.Kind != kind {
			t.Fatalf("kind %v = %+v", kind, got)
		}
	}
	if got := clickPart(t, m, 80, 2, ActionEditAddress, 0); got.Kind != ActionEditAddress || !m.Editing {
		t.Fatalf("address click = %+v editing=%v", got, m.Editing)
	}
	m.Handle(term.Event{Kind: term.KeyText, Rune: 'g'})
	if m.Address != "g" {
		t.Fatalf("first typed character did not replace selection: %q", m.Address)
	}
	m.Address = "example.org"
	if got := clickPart(t, m, 80, 2, ActionNavigate, 0); got.Kind != ActionNavigate || got.URL != "https://example.org" {
		t.Fatalf("Go = %+v", got)
	}
}

func TestChromeDisabledAndIgnoredMouse(t *testing.T) {
	m := New("https://example.com")
	for _, kind := range []ActionKind{ActionBack, ActionForward} {
		var p chromePart
		for _, candidate := range m.chromeLayout(80)[1] {
			if candidate.kind == kind {
				p = candidate
			}
		}
		if got := m.HandleMouse(80, term.MouseEvent{Col: p.start, Row: 2, Press: true}); got.Kind != ActionNone {
			t.Fatalf("disabled kind %v = %+v", kind, got)
		}
	}
	for _, mouse := range []term.MouseEvent{
		{Col: 1, Row: 3, Press: true},
		{Col: 1, Row: 1, Press: false},
		{Col: 1, Row: 1, Press: true, Motion: true},
		{Col: 1, Row: 1, Press: true, Wheel: -1},
	} {
		if got := m.HandleMouse(80, mouse); got.Kind != ActionNone {
			t.Fatalf("ignored mouse %+v = %+v", mouse, got)
		}
	}
}

func TestChromeWidthAndTitleSanitizing(t *testing.T) {
	m := New("https://example.com")
	m.Tabs = []TabInfo{{Title: "bad\x1b[31m\n中title"}}
	for cols := 1; cols <= 40; cols++ {
		rows := m.Chrome(cols)
		if len(rows) != ChromeRows {
			t.Fatalf("cols %d: rows=%d", cols, len(rows))
		}
		for _, row := range rows {
			visible := ansiColor.ReplaceAllString(row, "")
			if textutil.Width(visible) > cols || !utf8.ValidString(visible) {
				t.Fatalf("cols %d overflow/invalid UTF-8: %q (%d cells)", cols, visible, textutil.Width(visible))
			}
			if strings.Contains(row, "\x1b[31m") || strings.Contains(row, "\n") {
				t.Fatalf("page title injected control sequence: %q", row)
			}
		}
	}
}

func TestChromeAddressBackspaceClearsSelection(t *testing.T) {
	m := New("https://example.com/path")
	clickPart(t, m, 80, 2, ActionEditAddress, 0)
	m.Handle(term.Event{Kind: term.KeyBackspace})
	if m.Address != "" {
		t.Fatalf("Backspace left address %q", m.Address)
	}
}

func TestChineseTabsKeepTextAndCellHitboxes(t *testing.T) {
	m := New("https://www.baidu.com/")
	m.Tabs = []TabInfo{{Title: "百度一下，你就知道"}, {Title: "中文测试 👩🏽‍💻 e\u0301"}}
	visible := ansiColor.ReplaceAllString(m.Chrome(107)[0], "")
	if !strings.Contains(visible, "百度一下，你就知道") || strings.Contains(visible, "?") || !utf8.ValidString(visible) {
		t.Fatalf("Chinese tab corrupted: %q", visible)
	}
	for i, col := range []int{23, 47} {
		got := m.HandleMouse(107, term.MouseEvent{Button: 0, Col: col, Row: 1, Press: true})
		if got.Kind != ActionCloseTab || got.Index != i {
			t.Fatalf("close at cell %d = %+v", col, got)
		}
	}
	for cols := 1; cols <= 120; cols++ {
		for _, row := range m.Chrome(cols) {
			visible := ansiColor.ReplaceAllString(row, "")
			if !utf8.ValidString(visible) || textutil.Width(visible) > cols || strings.ContainsRune(visible, '\ufffd') {
				t.Fatalf("cols=%d invalid Unicode/clipping: %q", cols, visible)
			}
		}
	}
}

func TestChineseAddressDoesNotMoveGoAndShotButtons(t *testing.T) {
	m := New("https://例子.测试/中文路径")
	visible := ansiColor.ReplaceAllString(m.Chrome(107)[1], "")
	if !strings.Contains(visible, "例子.测试/中文路径") || textutil.Width(visible) != 107 {
		t.Fatalf("Unicode address field = %q", visible)
	}
	got := m.HandleMouse(107, term.MouseEvent{Button: 0, Col: 103, Row: 2, Press: true})
	if got.Kind != ActionScreenshot {
		t.Fatalf("Shot moved after Unicode address: %+v", got)
	}
}
