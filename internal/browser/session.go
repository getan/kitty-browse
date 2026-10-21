// Package browser owns the Chromium lifecycle and page navigation. The
// terminal renderer is deliberately separate: termcast consumes the chromedp
// context, while this package stays usable by a future TUI or automation tool.
package browser

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// Session is one headless Chromium tab.
type Session struct {
	ctx     context.Context
	cancel  context.CancelFunc
	url     string
}

// Open starts Chromium, navigates to target and waits for the document body.
// target accepts a bare host as https.
func Open(parent context.Context, target string, timeout time.Duration) (*Session, error) {
	target, err := normalize(target)
	if err != nil {
		return nil, err
	}

	ctx, cancel := chromedp.NewContext(parent)
	s := &Session{ctx: ctx, cancel: cancel, url: target}

	loadCtx, loadCancel := context.WithTimeout(ctx, timeout)
	defer loadCancel()
	if err := chromedp.Do(loadCtx,
		chromedp.Navigate(target),
		chromedp.WaitReady("body"),
	); err != nil {
		s.Close()
		return nil, fmt.Errorf("load %s: %w", target, err)
	}
	return s, nil
}

// Context is the chromedp context to hand to termcast and future controls.
func (s *Session) Context() context.Context { return s.ctx }

// URL is the normalized URL the session opened.
func (s *Session) URL() string { return s.url }

// Close stops Chromium and its child processes.
func (s *Session) Close() {
	if s != nil && s.cancel != nil {
		s.cancel()
	}
}

func normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty URL")
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
