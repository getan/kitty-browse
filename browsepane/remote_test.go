package browsepane

import (
	"testing"

	"github.com/getan/kitty-browse/internal/term"
)

func TestSharedPaneForwardsTypingWithoutBrowserShortcuts(t *testing.T) {
	for _, r := range []rune{'q', 'r', 'T', 'W', '中'} {
		in, ok := remotePageKey(term.Event{Kind: term.KeyText, Rune: r})
		if !ok || in.Kind != "text" || in.Text != string(r) {
			t.Fatalf("page typing %q became a browser shortcut: %+v", r, in)
		}
	}
	if in, ok := remotePageKey(term.Event{Kind: term.KeyTab}); !ok || in.Key != "Tab" {
		t.Fatal("Tab cannot move between page fields")
	}
	if _, ok := remotePageKey(term.Event{Kind: term.KeyIgnored}); ok {
		t.Fatal("terminal metadata became page input")
	}
}
