package browsepane

import (
	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/browser"
	"github.com/getan/kitty-browse/internal/host"
)

// The action contract lives in internal/host so the headless browserhost runs
// exactly the same validation, target pinning and host operations. These thin
// wrappers keep the pane's own call sites readable.

func clientError(code, message string) *action.Error { return host.ClientError(code, message) }

func browserScopedAction(name string) bool { return host.BrowserScopedAction(name) }

func displayedTabID(info action.Info, value string) (string, error) {
	return host.DisplayedTabID(info, value)
}

func resolveHostTabID(info action.Info, req *action.Request) error {
	return host.ResolveTabID(info, req)
}

func cacheTabTitle(titles map[*browser.Session]string, sess *browser.Session) {
	host.CacheTitle(titles, sess)
}
