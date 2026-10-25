package browserhost

import (
	"testing"

	"github.com/getan/kitty-browse/internal/browser"
)

// The host's title cache is keyed by session pointer and was only trimmed by the
// tab.close action, so a page or popup an external client closed stayed cached
// for the browser's lifetime. The serve loop prunes on every reconcile; this
// exercises that wiring without Chromium, since a tab set with no sessions means
// every cached title is stale.
func TestHostPruneTitlesDropsClosedSessions(t *testing.T) {
	closedPopup := &browser.Session{}
	h := &Host{tabs: &browser.Tabs{}, titles: map[*browser.Session]string{closedPopup: "popup"}}
	h.pruneTitles()
	if len(h.titles) != 0 {
		t.Fatalf("titles = %+v, want an empty cache after the tab set lost every page", h.titles)
	}
}

// Pruning must not touch a host that never cached a title.
func TestHostPruneTitlesToleratesAnEmptyCache(t *testing.T) {
	h := &Host{tabs: &browser.Tabs{}}
	h.pruneTitles()
	if len(h.titles) != 0 {
		t.Fatalf("titles = %+v", h.titles)
	}
}
