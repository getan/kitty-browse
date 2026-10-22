package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	cdptarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Tabs owns a set of user-visible tabs in one persistent Chromium browser.
// A private about:blank anchor keeps the browser alive independently of every
// user tab.
type Tabs struct {
	mu       sync.RWMutex
	anchor   *Session
	sessions []*Session
	index    int
	closed   bool
}

// ErrLastTab is returned when closing a tab would leave no user-visible tab.
var ErrLastTab = errors.New("cannot close the final tab")

// ErrTabsClosed is returned by operations on a closed Tabs collection.
var ErrTabsClosed = errors.New("tabs are closed")

// OpenTabs starts one shared Chromium browser and opens the initial user tab.
func OpenTabs(parent context.Context, target string, timeout time.Duration) (*Tabs, error) {
	anchor, err := Open(parent, "about:blank", timeout)
	if err != nil {
		return nil, fmt.Errorf("open browser anchor: %w", err)
	}

	tabs := &Tabs{anchor: anchor, index: -1}
	if err := tabs.New(parent, target, timeout); err != nil {
		tabs.Close()
		return nil, err
	}
	return tabs, nil
}

// Active returns the selected user tab, or nil after Close.
func (t *Tabs) Active() *Session {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.index < 0 || t.index >= len(t.sessions) {
		return nil
	}
	return t.sessions[t.index]
}

// Sessions returns a copy of the user-tab slice. The anchor is never exposed.
func (t *Tabs) Sessions() []*Session {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]*Session(nil), t.sessions...)
}

// Index returns the selected user-tab index, or -1 after Close.
func (t *Tabs) Index() int {
	if t == nil {
		return -1
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.index
}

// New creates, loads, and selects a user tab without replacing the browser.
func (t *Tabs) New(ctx context.Context, target string, timeout time.Duration) error {
	if t == nil {
		return ErrTabsClosed
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.anchor == nil {
		return ErrTabsClosed
	}

	session, err := open(t.anchor.Context(), ctx, target, timeout)
	if err != nil {
		return fmt.Errorf("open new tab: %w", err)
	}
	if err := bringToFront(session); err != nil {
		session.Close()
		return err
	}
	t.sessions = append(t.sessions, session)
	t.index = len(t.sessions) - 1
	return nil
}

// Select activates an existing tab without navigating or reloading it.
func (t *Tabs) Select(index int) error {
	if t == nil {
		return ErrTabsClosed
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ErrTabsClosed
	}
	if index < 0 || index >= len(t.sessions) {
		return fmt.Errorf("tab index %d out of range [0, %d)", index, len(t.sessions))
	}
	if err := bringToFront(t.sessions[index]); err != nil {
		return err
	}
	t.index = index
	return nil
}

// CloseTab closes one user tab. The final user tab cannot be closed.
func (t *Tabs) CloseTab(index int) error {
	if t == nil {
		return ErrTabsClosed
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ErrTabsClosed
	}
	if index < 0 || index >= len(t.sessions) {
		return fmt.Errorf("tab index %d out of range [0, %d)", index, len(t.sessions))
	}
	if len(t.sessions) == 1 {
		return ErrLastTab
	}

	newIndex := t.index
	if index < t.index {
		newIndex--
	} else if index == t.index && index == len(t.sessions)-1 {
		newIndex--
	}
	if index == t.index {
		candidate := t.sessions[newIndex]
		if newIndex >= index {
			candidate = t.sessions[newIndex+1]
		}
		if err := bringToFront(candidate); err != nil {
			return err
		}
	}

	closing := t.sessions[index]
	copy(t.sessions[index:], t.sessions[index+1:])
	t.sessions[len(t.sessions)-1] = nil
	t.sessions = t.sessions[:len(t.sessions)-1]
	t.index = newIndex
	closing.Close()
	return nil
}

// Screenshot safely captures the selected tab. Holding the read lock for the
// entire capture prevents Select, CloseTab, and Close from changing or closing
// the target midway through a control-server request.
func (t *Tabs) Screenshot(ctx context.Context, fullPage bool) ([]byte, error) {
	if t == nil {
		return nil, ErrTabsClosed
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed || t.index < 0 || t.index >= len(t.sessions) {
		return nil, ErrTabsClosed
	}
	return t.sessions[t.index].Screenshot(ctx, fullPage)
}

// Reconcile synchronizes user-visible tabs with Chromium's current page
// targets. It attaches window.open/automation-created pages without navigating
// them, removes targets closed by another CDP client, and never exposes the
// private browser-lifetime anchor. It returns whether the tab collection or
// selection changed.
//
// Reconcile is intended to run on the UI goroutine. It still takes the Tabs
// write lock so concurrent control-server screenshots cannot race an attach,
// removal, or active-tab change. Newly discovered pages are selected.
func (t *Tabs) Reconcile(ctx context.Context) (bool, error) {
	if t == nil {
		return false, ErrTabsClosed
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.anchor == nil {
		return false, ErrTabsClosed
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	queryCtx, cancel := context.WithTimeout(t.anchor.Context(), syncTimeout)
	stop := context.AfterFunc(ctx, cancel)
	infos, err := chromedp.Targets(queryCtx)
	stop()
	cancel()
	if err != nil {
		return false, fmt.Errorf("reconcile browser targets: %w", err)
	}

	anchorID := t.anchor.TargetID()
	pages := make([]*cdptarget.Info, 0, len(infos))
	live := make(map[string]bool, len(infos))
	for _, info := range infos {
		if !userPageTarget(info, anchorID) {
			continue
		}
		id := string(info.TargetID)
		live[id] = true
		pages = append(pages, info)
	}

	oldActive := t.activeLocked()
	known := make(map[string]bool, len(t.sessions))
	survivors := make([]*Session, 0, len(t.sessions)+len(pages))
	removed := make([]*Session, 0)
	for _, session := range t.sessions {
		id := session.TargetID()
		known[id] = true
		if id != "" && live[id] {
			survivors = append(survivors, session)
		} else {
			removed = append(removed, session)
		}
	}

	added := make([]*Session, 0)
	for _, info := range pages {
		if known[string(info.TargetID)] {
			continue
		}
		session, err := attachTarget(t.anchor.Context(), info)
		if err != nil {
			for _, attached := range added {
				attached.Close()
			}
			return false, fmt.Errorf("attach discovered tab %s: %w", info.TargetID, err)
		}
		added = append(added, session)
		survivors = append(survivors, session)
	}

	// If an external client closed every user page, immediately restore a real
	// user tab. The anchor stays private and exists only to keep Chromium alive.
	if len(survivors) == 0 {
		session, err := open(t.anchor.Context(), ctx, "about:blank", syncTimeout)
		if err != nil {
			return false, fmt.Errorf("restore blank tab: %w", err)
		}
		added = append(added, session)
		survivors = append(survivors, session)
	}

	changed := len(removed) > 0 || len(added) > 0
	newIndex := -1
	if len(added) > 0 {
		newIndex = len(survivors) - 1
		if err := bringToFront(survivors[newIndex]); err != nil {
			for _, attached := range added {
				attached.Close()
			}
			return false, err
		}
	} else {
		for i, session := range survivors {
			if session == oldActive {
				newIndex = i
				break
			}
		}
		if newIndex < 0 {
			newIndex = min(t.index, len(survivors)-1)
			changed = true
		}
	}

	t.sessions = survivors
	t.index = newIndex
	for _, session := range removed {
		session.Close()
	}
	return changed, nil
}

func (t *Tabs) activeLocked() *Session {
	if t.index < 0 || t.index >= len(t.sessions) {
		return nil
	}
	return t.sessions[t.index]
}

func userPageTarget(info *cdptarget.Info, anchorID string) bool {
	if info == nil || info.Type != "page" || string(info.TargetID) == anchorID {
		return false
	}
	// Extension and DevTools frontend pages are implementation UI, not browsing
	// tabs. Workers and iframes are already excluded by the exact type check.
	return !strings.HasPrefix(info.URL, "chrome-extension://") &&
		!strings.HasPrefix(info.URL, "devtools://")
}

func attachTarget(parent context.Context, info *cdptarget.Info) (*Session, error) {
	ctx, cancel := chromedp.NewContext(parent, chromedp.WithTargetID(info.TargetID))
	session := &Session{
		ctx:     ctx,
		cancel:  cancel,
		url:     info.URL,
		history: []string{info.URL},
	}
	// An empty action binds to the existing target. Deliberately do not Navigate,
	// Reload, or WaitReady: a popup's JS state and in-progress load must survive.
	if err := chromedp.Do(ctx); err != nil {
		session.Close()
		return nil, err
	}
	if err := applyIdentity(ctx); err != nil {
		session.Close()
		return nil, err
	}
	if err := session.StartObservation(); err != nil {
		session.Close()
		return nil, err
	}
	// TargetCreated can arrive before the popup's first URL is committed. Refresh
	// from Chromium after attach so the UI/API does not advertise an empty URL.
	_, _ = session.Sync(parent)
	return session, nil
}

// Close closes all user tabs and then the private browser anchor.
func (t *Tabs) Close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	for _, session := range t.sessions {
		session.Close()
	}
	t.sessions = nil
	t.index = -1
	if t.anchor != nil {
		t.anchor.Close()
		t.anchor = nil
	}
}

func bringToFront(session *Session) error {
	if _, err := chromedp.Call(session.Context(), page.BringToFront, cdp.Empty{}); err != nil {
		return fmt.Errorf("activate tab: %w", err)
	}
	return nil
}
