package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/browser"
)

// Result documents every host returns unchanged, so a client cannot tell the
// visible pane from a headless host.
var (
	// ResultDone is the "done" action result: action mode ended, browser stays.
	ResultDone = json.RawMessage(`{"released":true}`)
	// ResultQuitting is the "quit" action result: the host is shutting down.
	ResultQuitting = json.RawMessage(`{"quitting":true}`)
)

// NewBrowserID returns a fresh opaque browser identity.
func NewBrowserID() string {
	var random [12]byte
	if _, err := rand.Read(random[:]); err == nil {
		return "browser-" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("browser-%d", time.Now().UnixNano())
}

// ClientError builds the API error the contract uses for rejected requests.
func ClientError(code, message string) *action.Error {
	return &action.Error{Code: code, Message: message}
}

// DisplayedTabID resolves the CLI's 1-based displayed tab number to the stable
// tab ID. Anything that is not a number is already an opaque ID.
func DisplayedTabID(info action.Info, value string) (string, error) {
	index, err := strconv.Atoi(value)
	if err != nil {
		return value, nil // Opaque stable ID.
	}
	if index < 1 {
		return "", ClientError("target_not_found", "tab number must be a positive displayed index")
	}
	for _, tab := range info.Tabs {
		if tab.Index == index {
			return tab.ID, nil
		}
	}
	return "", ClientError("target_not_found", "displayed tab number is not available")
}

// SessionFor returns the session of a previously advertised target. A tab that
// closed, or whose target moved to another ID, is rejected instead of acted on.
func SessionFor(tabs *browser.Tabs, target action.TabInfo) (*browser.Session, error) {
	index := target.Index - 1
	sessions := tabs.Sessions()
	if index < 0 || index >= len(sessions) || sessions[index].TargetID() != target.ID {
		return nil, ClientError("target_not_found", "selected tab has closed or changed")
	}
	return sessions[index], nil
}

// NewTab opens url in a new tab and leaves it active. An empty URL opens the
// same blank page the pane's own new-tab control uses.
func NewTab(ctx context.Context, tabs *browser.Tabs, url string, load time.Duration) error {
	if url == "" {
		url = "about:blank"
	}
	return tabs.New(ctx, url, load)
}

// SelectTab activates one tab by displayed index.
func SelectTab(tabs *browser.Tabs, index int) error { return tabs.Select(index) }

// CloseTab closes one tab by displayed index. The last tab is refused with the
// contract's last_tab error rather than a generic failure.
func CloseTab(tabs *browser.Tabs, index int) error {
	err := tabs.CloseTab(index)
	if errors.Is(err, browser.ErrLastTab) {
		return ClientError("last_tab", "the last tab cannot be closed; quit the browser explicitly")
	}
	return err
}

// Navigate runs one host-managed navigation action on a session.
func Navigate(ctx context.Context, sess *browser.Session, name, url string, load time.Duration) error {
	switch name {
	case "navigate":
		return sess.Navigate(ctx, url, load)
	case "back":
		return sess.Back(ctx, load)
	case "forward":
		return sess.Forward(ctx, load)
	case "reload":
		return sess.Reload(ctx, load)
	}
	return ClientError("unsupported_action", "unsupported navigation action")
}

// CheckViewport enforces the set.viewport contract for both hosts. The visible
// pane then renders that size into its cell box; a headless host applies it
// directly.
func CheckViewport(width, height int) error {
	if width < 1 || height < 1 || width > 8192 || height > 8192 {
		return ClientError("invalid_arguments", "viewport dimensions must be between 1 and 8192")
	}
	return nil
}

// SetViewport validates and applies set.viewport dimensions.
func SetViewport(sess *browser.Session, width, height int) error {
	if err := CheckViewport(width, height); err != nil {
		return err
	}
	return sess.SetViewport(width, height)
}

// ViewportDocument is the set.viewport result.
func ViewportDocument(width, height int) (json.RawMessage, error) {
	return json.Marshal(map[string]int{"width": width, "height": height})
}

// CacheTitle records one tab's title, bounded so a busy page cannot stall the
// caller. It is a cache: failures leave the previous title in place.
func CacheTitle(titles map[*browser.Session]string, sess *browser.Session) {
	ctx, cancel := context.WithTimeout(sess.Context(), time.Second)
	defer cancel()
	if title, err := sess.Title(ctx); err == nil {
		titles[sess] = title
	}
}

// PruneTitles drops cached titles for sessions that are no longer live. The
// cache is keyed by session pointer, so a session closed by an external client
// or a closed popup would otherwise stay in the map, and in the tab-strip
// document, for as long as the browser lives. Tab sets that reconcile against
// Chromium call this after every reconcile.
func PruneTitles(titles map[*browser.Session]string, sessions []*browser.Session) {
	if len(titles) == 0 {
		return
	}
	live := make(map[*browser.Session]struct{}, len(sessions))
	for _, sess := range sessions {
		if sess != nil {
			live[sess] = struct{}{}
		}
	}
	for sess := range titles {
		if _, ok := live[sess]; !ok {
			delete(titles, sess)
		}
	}
}

// Quit is the one-shot shutdown request shared by the /v1/quit endpoint, the
// typed "quit" action and a host's own context. Request is safe to call from any
// goroutine and any number of times.
type Quit struct {
	once sync.Once
	ch   chan struct{}
	flag atomic.Bool
}

// NewQuit creates an unrequested quit signal.
func NewQuit() *Quit { return &Quit{ch: make(chan struct{})} }

// Request asks the host to shut down. It never blocks: the caller owns the
// response that must be delivered before the socket disappears.
func (q *Quit) Request() {
	if q == nil {
		return
	}
	q.once.Do(func() {
		q.flag.Store(true)
		close(q.ch)
	})
}

// Requested reports whether a graceful shutdown has been asked for.
func (q *Quit) Requested() bool { return q != nil && q.flag.Load() }

// Done is closed after Request.
func (q *Quit) Done() <-chan struct{} { return q.ch }
