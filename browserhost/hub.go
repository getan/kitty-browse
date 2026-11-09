package browserhost

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/getan/kitty-browse/internal/browser"
)

// Hub owns one Chromium and one profile. Each view opened on it has its own
// browser identity, socket, tab collection, selection and operator surface.
// Closing a View never closes another View or the Hub's profile.
type Hub struct {
	mu             sync.Mutex
	anchor         *browser.Session
	views          map[*Host]struct{}
	starting       sync.WaitGroup
	startingCtx    context.Context
	cancelStarting context.CancelFunc
	closed         bool
	stop           context.CancelFunc
	stopCtx        func() bool
	profile        string
	closeOnce      sync.Once
	closeErr       error
}

// ViewOptions contains only per-view options. Chromium, profile and proxy
// settings belong to the Hub and cannot differ between views.
type ViewOptions struct {
	URL, Socket                   string
	ViewportWidth, ViewportHeight int
	LoadTimeout                   time.Duration
}

// StartHub starts a private anchor in one Chromium using opts' profile and
// launch policy. The caller's context owns the hub after startup. No view or
// browser control socket is exposed until OpenView succeeds.
func StartHub(ctx context.Context, opts Options) (*Hub, error) {
	return startHub(ctx, ctx, opts)
}

func startHub(ctx, lifetime context.Context, opts Options) (*Hub, error) {
	if ctx == nil {
		return nil, errors.New("browser hub requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.Socket != "" {
		return nil, errors.New("browser hub has no shared control socket; provide one per view")
	}
	if opts.LoadTimeout <= 0 {
		opts.LoadTimeout = 30 * time.Second
	}
	browser.PruneStalePrivateProfiles(time.Now())
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	browserCtx, closeAlloc, _, err := browser.NewBrowserContext(life, launchOptions(opts))
	if err != nil {
		cancel()
		return nil, err
	}
	type result struct {
		anchor *browser.Session
		err    error
	}
	opened := make(chan result, 1)
	go func() {
		anchor, err := browser.Open(browserCtx, "about:blank", opts.LoadTimeout)
		opened <- result{anchor, err}
	}()
	var anchor *browser.Session
	select {
	case r := <-opened:
		anchor, err = r.anchor, r.err
	case <-ctx.Done():
		cancel()
		closeAlloc()
		go func() {
			if r := <-opened; r.anchor != nil {
				r.anchor.Close()
			}
		}()
		return nil, ctx.Err()
	}
	if err != nil {
		cancel()
		closeAlloc()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		anchor.Close()
		cancel()
		closeAlloc()
		return nil, err
	}
	startingCtx, cancelStarting := context.WithCancel(context.Background())
	h := &Hub{
		anchor: anchor, views: make(map[*Host]struct{}), profile: opts.LoginProfile,
		startingCtx: startingCtx, cancelStarting: cancelStarting,
		stop: func() { cancel(); anchor.Close(); closeAlloc() },
	}
	h.mu.Lock()
	h.stopCtx = context.AfterFunc(lifetime, func() { _ = h.Close() })
	h.mu.Unlock()
	return h, nil
}

// OpenView starts a session-local browser surface. Startup cancellation only
// destroys this view; it never cancels the hub or any other view.
func (h *Hub) OpenView(ctx context.Context, opts ViewOptions) (*Host, error) {
	if h == nil || ctx == nil {
		return nil, errors.New("browser hub and context are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.Socket == "" {
		return nil, errors.New("browser view requires its own control socket")
	}
	if opts.URL == "" {
		opts.URL = "https://example.com"
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
	h.mu.Lock()
	if h.closed || h.anchor == nil {
		h.mu.Unlock()
		return nil, errors.New("browser hub is closed")
	}
	h.starting.Add(1)
	anchor := h.anchor
	profile := h.profile
	h.mu.Unlock()
	defer h.starting.Done()
	viewCtx, cancel := context.WithCancel(anchor.Context())
	stopStartup := context.AfterFunc(ctx, cancel)
	defer stopStartup()
	stopHub := context.AfterFunc(h.startingCtx, cancel)
	defer stopHub()
	tabs, err := browser.OpenScopedTabs(viewCtx, opts.URL, opts.LoadTimeout)
	if err != nil {
		cancel()
		return nil, err
	}
	if ctx.Err() != nil {
		tabs.Close()
		cancel()
		return nil, ctx.Err()
	}
	view, err := startHostView(viewCtx, Options{
		Socket: opts.Socket, LoginProfile: profile, LoadTimeout: opts.LoadTimeout,
		ViewportWidth: opts.ViewportWidth, ViewportHeight: opts.ViewportHeight,
	}, tabs, cancel)
	if err != nil {
		return nil, err
	}
	if !stopStartup() || ctx.Err() != nil {
		_ = view.Close()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, context.Canceled
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = view.Close()
		return nil, errors.New("browser hub is closed")
	}
	h.views[view] = struct{}{}
	h.mu.Unlock()
	go func() {
		_ = view.Wait()
		h.mu.Lock()
		delete(h.views, view)
		h.mu.Unlock()
	}()
	return view, nil
}

// Close terminates all views before releasing Chromium and its profile.
func (h *Hub) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		views := make([]*Host, 0, len(h.views))
		for view := range h.views {
			views = append(views, view)
		}
		h.mu.Unlock()
		if h.stopCtx != nil {
			h.stopCtx()
		}
		if h.cancelStarting != nil {
			h.cancelStarting()
		}
		var errs []error
		for _, view := range views {
			errs = append(errs, view.Close())
		}
		h.starting.Wait()
		if h.stop != nil {
			h.stop()
		}
		h.closeErr = errors.Join(errs...)
	})
	return h.closeErr
}
