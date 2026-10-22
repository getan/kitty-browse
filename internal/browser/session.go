// Package browser owns the Chromium lifecycle and page navigation. The
// terminal renderer is deliberately separate: termcast consumes the chromedp
// context, while this package stays usable by a future TUI or automation tool.
package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const syncTimeout = time.Second

// Session is one headless Chromium tab.
type Session struct {
	ctx          context.Context
	cancel       context.CancelFunc
	url          string
	history      []string
	index        int
	closeOnce    sync.Once
	automationMu sync.Mutex
	automation   *automationState
}

// Open starts Chromium, navigates to target and waits for the document body.
// target accepts a bare host as https, as well as the exact URL about:blank.
func Open(parent context.Context, target string, timeout time.Duration) (*Session, error) {
	return open(parent, parent, target, timeout)
}

// open separates the lifetime context from the context which may cancel the
// initial navigation. This matters for tabs: canceling a New call must not
// cancel the shared browser or any previously opened tab.
func open(parent, operation context.Context, target string, timeout time.Duration) (*Session, error) {
	target, err := Normalize(target)
	if err != nil {
		return nil, err
	}

	ctx, cancel := chromedp.NewContext(parent)
	s := &Session{ctx: ctx, cancel: cancel, url: target, history: []string{target}}

	// The first chromedp.Run binds Chromium's lifetime to its context. Start
	// the browser with the persistent session context; load() uses a child
	// context with a timeout for navigation only. Using loadCtx for the first
	// Run cancels Chromium as soon as the load timeout context is released.
	if err := chromedp.Do(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("start browser: %w", err)
	}
	if err := applyIdentity(ctx); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.StartObservation(); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.load(operation, target, timeout); err != nil {
		s.Close()
		return nil, err
	}
	if _, err := s.Sync(operation); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Context is the chromedp context to hand to termcast and future controls.
func (s *Session) Context() context.Context { return s.ctx }

// URL is the normalized URL the session opened.
func (s *Session) URL() string { return s.url }

// CanBack reports whether history has an earlier entry.
func (s *Session) CanBack() bool { return s.index > 0 }

// CanForward reports whether history has a later entry.
func (s *Session) CanForward() bool { return s.index+1 < len(s.history) }

// Navigate loads target and makes it the current history entry.
func (s *Session) Navigate(ctx context.Context, target string, timeout time.Duration) error {
	target, err := Normalize(target)
	if err != nil {
		return err
	}
	if err := s.load(ctx, target, timeout); err != nil {
		return err
	}
	_, err = s.Sync(ctx)
	return err
}

// Back moves one entry back. It is a no-op at the start of history.
func (s *Session) Back(ctx context.Context, timeout time.Duration) error {
	history, err := s.navigationHistory(ctx)
	if err != nil {
		return err
	}
	if history.CurrentIndex <= 0 || history.CurrentIndex >= int64(len(history.Entries)) {
		return ErrNoHistory
	}
	entry := history.Entries[history.CurrentIndex-1]
	if entry == nil {
		return fmt.Errorf("back: invalid navigation history entry")
	}
	if err := s.goHistory(ctx, timeout, entry.ID); err != nil {
		return fmt.Errorf("back: %w", err)
	}
	if _, err := s.Sync(ctx); err != nil {
		return err
	}
	return nil
}

// Forward moves one entry forward. It is a no-op at the end of history.
func (s *Session) Forward(ctx context.Context, timeout time.Duration) error {
	history, err := s.navigationHistory(ctx)
	if err != nil {
		return err
	}
	if history.CurrentIndex < 0 || history.CurrentIndex >= int64(len(history.Entries)-1) {
		return ErrNoHistory
	}
	entry := history.Entries[history.CurrentIndex+1]
	if entry == nil {
		return fmt.Errorf("forward: invalid navigation history entry")
	}
	if err := s.goHistory(ctx, timeout, entry.ID); err != nil {
		return fmt.Errorf("forward: %w", err)
	}
	if _, err := s.Sync(ctx); err != nil {
		return err
	}
	return nil
}

// A BFCache restoration does not necessarily emit a new load event. The
// chromedp high-level navigation action waits for that event and can time out
// after a successful Back. Wait for the actual selected history entry instead.
func (s *Session) goHistory(operation context.Context, timeout time.Duration, id int64) error {
	return s.run(operation, timeout, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		if _, err := cdp.Call(ctx, target, page.NavigateToHistoryEntry, page.NavigateToHistoryEntryParams{EntryID: id}); err != nil {
			return err
		}
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			history, err := cdp.Call(ctx, target, page.GetNavigationHistory, cdp.Empty{})
			if err != nil {
				return err
			}
			i := history.CurrentIndex
			if i >= 0 && i < int64(len(history.Entries)) && history.Entries[i] != nil && history.Entries[i].ID == id {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-tick.C:
			}
		}
	}))
}

// Reload reloads the current URL without adding a history entry.
func (s *Session) Reload(ctx context.Context, timeout time.Duration) error {
	if err := s.run(ctx, timeout, chromedp.Reload()); err != nil {
		return fmt.Errorf("reload %s: %w", s.url, err)
	}
	_, err := s.Sync(ctx)
	return err
}

// SetViewport matches the Chromium viewport to the page area's pixels, so
// terminal cells map to CSS pixels without a second scale factor.
func (s *Session) SetViewport(width, height int) error {
	if width <= 0 || height <= 0 {
		return nil
	}
	return chromedp.Do(s.ctx, chromedp.EmulateViewport(int64(width), int64(height), chromedp.EmulateScale(1)))
}

// dispatchMouseEvent sends one raw Input.dispatchMouseEvent. chromedp's
// MouseClickXY dispatches only a press and release, so the page's hover state
// never sees the pointer before the click lands.
var dispatchMouseEvent = cdp.Command[input.DispatchMouseEventParams, cdp.Empty]{Method: input.CommandDispatchMouseEvent}

// dispatchKeyEvent sends one raw Input.dispatchKeyEvent.
var dispatchKeyEvent = cdp.Command[input.DispatchKeyEventParams, cdp.Empty]{Method: input.CommandDispatchKeyEvent}

func mouseMoved(x, y float64, buttons int64) input.DispatchMouseEventParams {
	p := input.DispatchMouseEventParams{Type: input.DispatchMouseEventTypeMouseMoved, X: x, Y: y, Buttons: buttons}
	if buttons != 0 {
		p.Button = input.MouseButtonLeft
	}
	return p
}

func mouseButton(typ input.DispatchMouseEventType, x, y float64, buttons int64) input.DispatchMouseEventParams {
	return input.DispatchMouseEventParams{
		Type: typ, X: x, Y: y, Button: input.MouseButtonLeft, Buttons: buttons, ClickCount: 1,
	}
}

func (s *Session) sendMouse(events ...input.DispatchMouseEventParams) error {
	return chromedp.Do(s.ctx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		for _, event := range events {
			if _, err := cdp.Call(ctx, t, dispatchMouseEvent, event); err != nil {
				return err
			}
		}
		return nil
	}))
}

// Click sends a left click at viewport CSS coordinates: move, press, release.
// The move lets mouseover-bound menus and handlers activate before the press.
func (s *Session) Click(x, y float64) error {
	return s.sendMouse(
		mouseMoved(x, y, 0),
		mouseButton(input.DispatchMouseEventTypeMousePressed, x, y, 1),
		mouseButton(input.DispatchMouseEventTypeMouseReleased, x, y, 0),
	)
}

// MouseDown moves the pointer to x,y and presses the left button, starting a
// drag that MouseMove continues and MouseUp ends. Sliders and canvas widgets
// only respond to this press/move/release sequence; a plain click cannot move
// them.
func (s *Session) MouseDown(x, y float64) error {
	return s.sendMouse(
		mouseMoved(x, y, 0),
		mouseButton(input.DispatchMouseEventTypeMousePressed, x, y, 1),
	)
}

// MouseMove drags the left button to x,y.
func (s *Session) MouseMove(x, y float64) error {
	return s.sendMouse(mouseMoved(x, y, 1))
}

// MouseUp releases the left button at x,y.
func (s *Session) MouseUp(x, y float64) error {
	return s.sendMouse(
		mouseMoved(x, y, 1),
		mouseButton(input.DispatchMouseEventTypeMouseReleased, x, y, 0),
	)
}

// InsertText inserts text into the focused element as IME input would. The
// terminal delivers composed UTF-8 runes, so this is the only safe path for
// Chinese text: synthesizing one key event per character would require a
// keyboard layout that does not exist.
func (s *Session) InsertText(value string) error {
	if value == "" {
		return nil
	}
	return chromedp.Do(s.ctx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		_, err := cdp.Call(ctx, t, input.InsertText, input.InsertTextParams{Text: value})
		return err
	}))
}

// pageKey describes a non-printing key a focused page field can receive.
type pageKey struct {
	key, code string
	vk        int64
	text      string
}

var pageEditingKeys = map[string]pageKey{
	"Backspace":  {key: "Backspace", code: "Backspace", vk: 8},
	"Enter":      {key: "Enter", code: "Enter", vk: 13, text: "\r"},
	"ArrowLeft":  {key: "ArrowLeft", code: "ArrowLeft", vk: 37},
	"ArrowUp":    {key: "ArrowUp", code: "ArrowUp", vk: 38},
	"ArrowRight": {key: "ArrowRight", code: "ArrowRight", vk: 39},
	"ArrowDown":  {key: "ArrowDown", code: "ArrowDown", vk: 40},
}

// KeyPress sends one editing key to the page's focused element.
func (s *Session) KeyPress(name string) error {
	def, ok := pageEditingKeys[name]
	if !ok {
		return fmt.Errorf("unsupported page key %q", name)
	}
	typ := input.DispatchKeyEventTypeRawKeyDown
	if def.text != "" {
		typ = input.DispatchKeyEventTypeKeyDown
	}
	return chromedp.Do(s.ctx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		if _, err := cdp.Call(ctx, t, dispatchKeyEvent, input.DispatchKeyEventParams{
			Type: typ, Key: def.key, Code: def.code, Text: def.text,
			WindowsVirtualKeyCode: def.vk, NativeVirtualKeyCode: def.vk,
		}); err != nil {
			return err
		}
		_, err := cdp.Call(ctx, t, dispatchKeyEvent, input.DispatchKeyEventParams{
			Type: input.DispatchKeyEventTypeKeyUp, Key: def.key, Code: def.code,
			WindowsVirtualKeyCode: def.vk, NativeVirtualKeyCode: def.vk,
		})
		return err
	}))
}

// Scroll sends a wheel event at viewport CSS coordinates. Positive deltaY
// scrolls down.
func (s *Session) Scroll(x, y, deltaY float64) error {
	return chromedp.Do(s.ctx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		// DeltaX and DeltaY are required for a wheel event, but the generated
		// protocol struct tags omit numeric zero values. Use a command with
		// required JSON fields and the action's explicit target so a vertical
		// wheel event really includes deltaX: 0.
		_, err := cdp.Call(ctx, t, dispatchWheelEvent, wheelEventParams{
			Type: input.DispatchMouseEventTypeMouseWheel, X: x, Y: y, DeltaX: 0, DeltaY: deltaY,
		})
		return err
	}))
}

type wheelEventParams struct {
	Type   input.DispatchMouseEventType `json:"type"`
	X      float64                      `json:"x"`
	Y      float64                      `json:"y"`
	DeltaX float64                      `json:"deltaX"`
	DeltaY float64                      `json:"deltaY"`
}

var dispatchWheelEvent = cdp.Command[wheelEventParams, cdp.Empty]{Method: input.CommandDispatchMouseEvent}

// Text returns the rendered page's innerText. It is the readable fallback when
// a page is unreadable as pixels or the user needs selectable text.
func (s *Session) Text(ctx context.Context) (string, error) {
	out, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.body ? document.body.innerText : ""`))
	if err != nil {
		return "", fmt.Errorf("extract text: %w", err)
	}
	return out, nil
}

// Title returns document.title.
func (s *Session) Title(ctx context.Context) (string, error) {
	out, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.title || ""`))
	if err != nil {
		return "", fmt.Errorf("page title: %w", err)
	}
	return out, nil
}

// LayoutWidth reports the page's layout width in CSS pixels: the wider of the
// viewport and the document's scroll extent. Callers use it to give fixed-width
// desktop pages a viewport wide enough to show the whole layout instead of
// clipping it at the pane edge.
func (s *Session) LayoutWidth(ctx context.Context) (int, error) {
	width, err := chromedp.Run(ctx, chromedp.Evaluate[float64](
		`Math.max(window.innerWidth || 0, document.documentElement ? document.documentElement.scrollWidth : 0)`))
	if err != nil {
		return 0, fmt.Errorf("measure layout width: %w", err)
	}
	if width <= 0 {
		return 0, nil
	}
	if width > 8192 {
		width = 8192
	}
	return int(width + 0.5), nil
}

// editableFocusJS reports whether the page's active element accepts typed text.
// Buttons, links and select elements do not, so browser shortcuts stay active
// while they are focused.
const editableFocusJS = `(() => {
  const el = document.activeElement;
  if (!el) return "";
  if (el.isContentEditable) return "contenteditable";
  const tag = el.tagName;
  if (tag === "TEXTAREA") return "textarea";
  if (tag !== "INPUT") return "";
  const type = (el.getAttribute("type") || "text").toLowerCase();
  if (["button", "checkbox", "color", "file", "hidden", "image",
       "radio", "range", "reset", "submit"].includes(type)) return "";
  return "input";
})()`

// EditableFocus reports whether a page element that accepts text is focused.
// While it is true, printable keys must be typed into the page rather than
// interpreted as browser shortcuts.
func (s *Session) EditableFocus(ctx context.Context) (bool, error) {
	kind, err := chromedp.Run(ctx, chromedp.Evaluate[string](editableFocusJS))
	if err != nil {
		return false, fmt.Errorf("page focus: %w", err)
	}
	return kind != "", nil
}

// BlurFocused clears page focus so browser shortcuts take over again.
func (s *Session) BlurFocused(ctx context.Context) error {
	_, err := chromedp.Run(ctx, chromedp.Evaluate[any](`(() => {
		const el = document.activeElement;
		if (el && el.blur) el.blur();
	})()`))
	if err != nil {
		return fmt.Errorf("blur page focus: %w", err)
	}
	return nil
}

// History returns the owned history and the current index.
func (s *Session) History() ([]string, int) {
	return append([]string(nil), s.history...), s.index
}

// Sync refreshes the session URL and history from Chromium and returns the
// current history entry's title. It uses the session's lifetime context while
// respecting ctx cancellation, and is bounded to one second for UI polling.
func (s *Session) Sync(ctx context.Context) (title string, err error) {
	history, err := s.navigationHistory(ctx)
	if err != nil {
		return "", err
	}
	entry := history.Entries[history.CurrentIndex]
	return entry.Title, nil
}

// Close closes this tab. A root session closes Chromium gracefully so persistent
// cookies/storage can be flushed before the allocator releases the profile lock.
func (s *Session) Close() {
	if s != nil && s.cancel != nil {
		s.closeOnce.Do(func() {
			s.StopObservation()
			if s.ctx != nil && s.ctx.Err() == nil && chromedp.FromContext(s.ctx) != nil && chromedp.FromContext(s.ctx).Browser != nil {
				ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
				_ = chromedp.Cancel(ctx)
				cancel()
			}
			s.cancel()
		})
	}
}

func (s *Session) load(operation context.Context, target string, timeout time.Duration) error {
	if err := s.run(operation, timeout,
		chromedp.Navigate(target),
		chromedp.WaitReady("body"),
	); err != nil {
		return fmt.Errorf("load %s: %w", target, err)
	}
	return nil
}

func (s *Session) navigationHistory(operation context.Context) (page.GetNavigationHistoryResult, error) {
	var history page.GetNavigationHistoryResult
	err := s.run(operation, syncTimeout, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		var err error
		history, err = cdp.Call(ctx, target, page.GetNavigationHistory, cdp.Empty{})
		return err
	}))
	if err != nil {
		return history, fmt.Errorf("sync navigation history: %w", err)
	}
	if history.CurrentIndex < 0 || history.CurrentIndex >= int64(len(history.Entries)) || history.Entries[history.CurrentIndex] == nil {
		return history, fmt.Errorf("sync navigation history: invalid current entry")
	}

	s.history = make([]string, len(history.Entries))
	for i, entry := range history.Entries {
		if entry != nil {
			s.history[i] = entry.URL
		}
	}
	s.index = int(history.CurrentIndex)
	s.url = history.Entries[history.CurrentIndex].URL
	return history, nil
}

// run bridges a caller's operation context to a timeout derived from the
// persistent tab context, so finishing or canceling an operation never tears
// down Chromium itself.
func (s *Session) run(operation context.Context, timeout time.Duration, actions ...chromedp.Action[chromedp.Void]) error {
	if err := operation.Err(); err != nil {
		return err
	}
	opCtx, cancel := context.WithTimeout(s.ctx, timeout)
	stop := context.AfterFunc(operation, cancel)
	defer func() {
		stop()
		cancel()
	}()
	return chromedp.Do(opCtx, actions...)
}

// ErrNoHistory means Back/Forward had nowhere to go.
var ErrNoHistory = errors.New("no history entry")

// Normalize adds https:// to a bare host and accepts http, https, file, and
// exactly about:blank. Other about URLs remain unsupported.
func Normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty URL")
	}
	if raw == "about:blank" {
		return raw, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	switch u.Scheme {
	case "http", "https", "file":
	default:
		return "", fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if u.Scheme != "file" && u.Host == "" {
		return "", fmt.Errorf("URL has no host: %s", raw)
	}
	return u.String(), nil
}
