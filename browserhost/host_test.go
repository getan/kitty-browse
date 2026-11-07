package browserhost

import (
	"context"
	"testing"

	"github.com/getan/kitty-browse/internal/browser"
)

func TestHostRejectsInvalidProxyBeforeStartingBrowser(t *testing.T) {
	for _, proxy := range []string{"127.0.0.1:7897", "http://user:secret@127.0.0.1:7897", "ftp://127.0.0.1:21"} {
		if _, err := Start(context.Background(), Options{ProxyServer: proxy}); err == nil {
			t.Errorf("accepted invalid proxy %q", proxy)
		}
	}
}

func TestHostPassesExplicitProxyAndSandboxToLauncher(t *testing.T) {
	opts := launchOptions(Options{ProxyServer: "http://127.0.0.1:7897", RequireSandbox: true})
	if opts.ProxyServer != "http://127.0.0.1:7897" || !opts.RequireSandbox {
		t.Fatalf("launch options did not preserve proxy and sandbox: %+v", opts)
	}
}

// The host's title cache is keyed by session pointer and was only trimmed by the
// tab.close action, so a page or popup an external client closed stayed cached
// for the browser's lifetime. The serve loop prunes on every reconcile; this
// exercises that wiring without Chromium, since a tab set with no sessions means
// every cached title is stale.
func TestHostPruneTitlesDropsClosedSessions(t *testing.T) {
	closedPopup := &browser.Session{}
	h := &Host{tabs: &browser.Tabs{}, titles: map[*browser.Session]string{closedPopup: "popup"}, viewports: map[*browser.Session]viewportSize{closedPopup: {640, 480}}}
	h.pruneTitles()
	if len(h.titles) != 0 {
		t.Fatalf("titles = %+v, want an empty cache after the tab set lost every page", h.titles)
	}
	if len(h.viewports) != 0 {
		t.Fatalf("viewports = %+v, want an empty cache after the tab set lost every page", h.viewports)
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
