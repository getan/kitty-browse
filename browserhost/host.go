// Package browserhost runs one headless Chromium behind the same native control
// API the visible pane serves: /v1/info, /v1/actions, /v1/screenshot and
// /v1/quit on an owner-only Unix socket.
//
// It exists for hosts such as golder that want the browser without a terminal
// pane. Action semantics are not reimplemented: page actions run through
// internal/action and host-managed actions through internal/host, the same code
// paths the visible browsepane uses. The pane remains the primary integration.
package browserhost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	api "github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/browser"
	"github.com/getan/kitty-browse/internal/control"
	"github.com/getan/kitty-browse/internal/host"
)

// quitGrace keeps the control socket alive just long enough for the reply to a
// quit action to reach the client that asked for it.
const quitGrace = 200 * time.Millisecond

// Options configures one headless browser.
type Options struct {
	// URL is the first page to open. Empty opens https://example.com, the same
	// default the pane and the kitty-browse binary use.
	URL string
	// Socket is the owner-only Unix socket that serves the control API. Empty
	// runs the browser without one, which is useful only for a smoke test.
	Socket string
	// Chrome is the Chromium executable path. Empty searches PATH.
	Chrome string
	// ProfileDir is a dedicated persistent profile. Empty uses kitty-browse's
	// own configuration directory.
	ProfileDir string
	// DownloadDir is where downloads are written. Empty uses the profile's
	// Downloads directory, or a temporary directory in private mode.
	DownloadDir string
	// Private uses a temporary profile and writes no state.
	Private bool
	// Stealth removes Chromium's headless UA token and automation markers. The
	// pane defaults this on, so DefaultOptions sets it; the zero value keeps
	// Chromium's native headless identity.
	Stealth bool
	// UserAgent overrides the per-target User-Agent. Empty derives it from the
	// running Chromium build, exactly like the pane.
	UserAgent string
	// ViewportWidth and ViewportHeight are the emulated page size. Zero uses
	// 1280x800. The pane always emulates the pane's own size; a headless host
	// needs one explicitly, because Chromium's default window is not a page the
	// control API can screenshot predictably.
	ViewportWidth  int
	ViewportHeight int
	// LoadTimeout bounds one page load. Zero uses 30s.
	LoadTimeout time.Duration
	// BrowserOutput receives Chromium's diagnostic output. Private mode ignores
	// it, like the pane's debug log.
	BrowserOutput io.Writer
	// Controlled embedders may require the allocator to retain Chromium's
	// native sandbox even when running as root (otherwise startup must fail).
	RequireSandbox bool
}

// DefaultOptions returns the pane's defaults, so a headless host started with
// them behaves like a visible one.
func DefaultOptions() Options {
	return Options{URL: "https://example.com", Stealth: true, ViewportWidth: 1280, ViewportHeight: 800, LoadTimeout: 30 * time.Second}
}

const (
	defaultViewportWidth  = 1280
	defaultViewportHeight = 800
)

// Host is one running headless browser.
type Host struct {
	bridge *host.Bridge
	tabs   *browser.Tabs
	titles map[*browser.Session]string
	socket string
	quit   *host.Quit
	server *control.Server
	cancel context.CancelFunc
	done   chan struct{}

	viewportWidth  int
	viewportHeight int

	closeOnce sync.Once
	closeErr  error
}

// Start launches Chromium, opens the first page and starts serving the control
// API. It returns once the socket is listening; page load failures leave
// Chromium's error page and the socket alive, exactly like the pane.
func Start(ctx context.Context, opts Options) (*Host, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.LoadTimeout <= 0 {
		opts.LoadTimeout = 30 * time.Second
	}
	if opts.ViewportWidth <= 0 {
		opts.ViewportWidth = defaultViewportWidth
	}
	if opts.ViewportHeight <= 0 {
		opts.ViewportHeight = defaultViewportHeight
	}
	url := opts.URL
	if url == "" {
		url = "https://example.com"
	}
	browser.PruneStalePrivateProfiles(time.Now())

	browserCtx, closeAlloc, _, err := browser.NewBrowserContext(ctx, browser.LaunchOptions{
		ExecutablePath: opts.Chrome, ProfileDir: opts.ProfileDir, DownloadDir: opts.DownloadDir,
		Private: opts.Private, Stealth: opts.Stealth, UserAgent: opts.UserAgent,
		BrowserOutput:  opts.BrowserOutput,
		RequireSandbox: opts.RequireSandbox,
	})
	if err != nil {
		return nil, err
	}
	tabs, err := browser.OpenTabs(browserCtx, url, opts.LoadTimeout)
	if err != nil {
		closeAlloc()
		return nil, err
	}

	h := &Host{
		bridge: host.NewBridge(host.NewBrowserID()),
		tabs:   tabs,
		titles: make(map[*browser.Session]string),
		socket: opts.Socket,
		quit:   host.NewQuit(),
		cancel: closeAlloc,
		done:   make(chan struct{}),

		viewportWidth:  opts.ViewportWidth,
		viewportHeight: opts.ViewportHeight,
	}
	for _, sess := range tabs.Sessions() {
		host.CacheTitle(h.titles, sess)
	}
	h.applyViewport(tabs.Active())
	h.bridge.Update(tabs, h.titles)

	if opts.Socket != "" {
		server, startErr := control.Start(opts.Socket, tabs.Screenshot,
			control.WithAPI(h.bridge.Information, h.bridge.Execute),
			control.WithQuit(func(context.Context) { h.quit.Request() }))
		if startErr != nil {
			tabs.Close()
			closeAlloc()
			return nil, startErr
		}
		h.server = server
	}
	go h.serve(browserCtx, opts.LoadTimeout)
	return h, nil
}

// Socket is the control socket path this host serves, or "".
func (h *Host) Socket() string { return h.socket }

// Wait blocks until the browser is asked to quit, ctx is canceled or Close is
// called, then releases Chromium and removes the socket. A graceful quit, and a
// cancelled ctx, are not errors.
func (h *Host) Wait() error {
	<-h.done
	return h.shutdown()
}

// Close asks the browser to quit and returns once it has.
func (h *Host) Close() error {
	h.quit.Request()
	<-h.done
	return h.shutdown()
}

func (h *Host) shutdown() error {
	h.closeOnce.Do(func() {
		var errs []error
		if h.server != nil {
			errs = append(errs, h.server.Close())
		}
		h.tabs.Close()
		h.cancel()
		h.closeErr = errors.Join(errs...)
	})
	return h.closeErr
}

// serve owns every mutation of the tab set, mirroring the pane's UI loop. Page
// actions never come here: the bridge executes those directly.
func (h *Host) serve(ctx context.Context, load time.Duration) {
	defer close(h.done)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.quit.Done():
			return
		case call := <-h.bridge.Calls:
			h.handle(ctx, call, load)
			if h.quit.Requested() {
				// The reply is queued for the HTTP handler that asked for quit.
				select {
				case <-time.After(quitGrace):
				case <-ctx.Done():
				}
				return
			}
		case <-tick.C:
			if h.bridge.Active.Load() > 0 {
				continue
			}
			if changed, err := h.tabs.Reconcile(ctx); err == nil && changed {
				// A page or popup an external client closed dies without a
				// tab.close action, so reconcile is where its cached title has
				// to go: the cache is keyed by session pointer and would
				// otherwise hold a dead session for the browser's lifetime.
				h.pruneTitles()
				for _, sess := range h.tabs.Sessions() {
					host.CacheTitle(h.titles, sess)
				}
				h.bridge.Update(h.tabs, h.titles)
			}
		}
	}
}

func (h *Host) handle(ctx context.Context, call host.Call, load time.Duration) {
	view := h.bridge.Load()
	target, targetErr := api.SelectTarget(view.Info, call.Req.Target)
	result, actionErr := func() (json.RawMessage, error) {
		if call.Ctx.Err() != nil {
			return nil, call.Ctx.Err()
		}
		var p struct {
			URL    string `json:"url"`
			TabID  string `json:"tabId"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		}
		if len(call.Req.Params) != 0 && json.Unmarshal(call.Req.Params, &p) != nil {
			return nil, host.ClientError("invalid_arguments", "invalid host action parameters")
		}
		if host.BrowserScopedAction(call.Req.Action) {
			switch call.Req.Action {
			case "info", "tab.list":
				return json.Marshal(view.Info)
			case "capabilities":
				return json.Marshal(api.Capabilities())
			case "done":
				return host.ResultDone, nil
			case "quit":
				h.quit.Request()
				return host.ResultQuitting, nil
			case "tab.new":
				if call.Ctx.Err() != nil {
					return nil, call.Ctx.Err()
				}
				if err := host.NewTab(call.Ctx, h.tabs, p.URL, load); err != nil {
					return nil, err
				}
				h.applyViewport(h.tabs.Active())
				host.CacheTitle(h.titles, h.tabs.Active())
			}
			h.refresh()
			target, _ = api.SelectTarget(h.bridge.Load().Info, api.Target{})
			return json.Marshal(h.bridge.Load().Info)
		}
		if targetErr != nil {
			return nil, targetErr
		}
		index := target.Index - 1
		selected, sessionErr := host.SessionFor(h.tabs, target)
		if sessionErr != nil {
			return nil, sessionErr
		}
		switch call.Req.Action {
		case "tab.activate":
			if call.Ctx.Err() != nil {
				return nil, call.Ctx.Err()
			}
			if err := host.SelectTab(h.tabs, index); err != nil {
				return nil, err
			}
			h.applyViewport(h.tabs.Active())
			host.CacheTitle(h.titles, h.tabs.Active())
		case "tab.close":
			if call.Ctx.Err() != nil {
				return nil, call.Ctx.Err()
			}
			if err := host.CloseTab(h.tabs, index); err != nil {
				return nil, err
			}
			delete(h.titles, selected)
			h.applyViewport(h.tabs.Active())
			host.CacheTitle(h.titles, h.tabs.Active())
		case "set.viewport":
			if call.Ctx.Err() != nil {
				return nil, call.Ctx.Err()
			}
			if index != h.tabs.Index() {
				return nil, host.ClientError("target_not_active", "activate the selected tab before changing its viewport")
			}
			if err := host.SetViewport(selected, p.Width, p.Height); err != nil {
				return nil, err
			}
			h.viewportWidth, h.viewportHeight = p.Width, p.Height
			h.refresh()
			return host.ViewportDocument(p.Width, p.Height)
		case "navigate", "back", "forward", "reload":
			if err := host.Navigate(call.Ctx, selected, call.Req.Action, p.URL, load); err != nil {
				return nil, err
			}
			host.CacheTitle(h.titles, selected)
		default:
			return nil, host.ClientError("unsupported_action", "unsupported host action")
		}
		h.refresh()
		if updated, err := api.SelectTarget(h.bridge.Load().Info, api.Target{TargetID: target.TargetID}); err == nil {
			target = updated
		}
		return json.Marshal(h.bridge.Load().Info)
	}()
	h.refresh()
	call.Reply <- api.Respond(call.Req, h.bridge.Load().Info, target, result, actionErr)
}

func (h *Host) refresh() { h.bridge.Update(h.tabs, h.titles) }

// pruneTitles drops cached titles for sessions the tab set no longer has.
func (h *Host) pruneTitles() { host.PruneTitles(h.titles, h.tabs.Sessions()) }

// applyViewport keeps every newly active tab at the host's emulated page size,
// so screenshots and coordinates mean the same thing on any tab.
func (h *Host) applyViewport(sess *browser.Session) {
	if sess == nil {
		return
	}
	_ = sess.SetViewport(h.viewportWidth, h.viewportHeight)
}
