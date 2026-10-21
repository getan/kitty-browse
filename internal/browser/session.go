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
	"time"

	"github.com/chromedp/chromedp"
)

// Session is one headless Chromium tab.
type Session struct {
	ctx     context.Context
	cancel  context.CancelFunc
	url     string
	history []string
	index   int
}

// Open starts Chromium, navigates to target and waits for the document body.
// target accepts a bare host as https.
func Open(parent context.Context, target string, timeout time.Duration) (*Session, error) {
	target, err := Normalize(target)
	if err != nil {
		return nil, err
	}

	ctx, cancel := chromedp.NewContext(parent)
	s := &Session{ctx: ctx, cancel: cancel, url: target, history: []string{target}}

	if err := s.load(target, timeout); err != nil {
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
	if err := s.load(target, timeout); err != nil {
		return err
	}
	if s.index < len(s.history)-1 {
		s.history = s.history[:s.index+1]
	}
	s.history = append(s.history, target)
	s.index = len(s.history) - 1
	s.url = target
	return nil
}

// Back moves one entry back. It is a no-op at the start of history.
func (s *Session) Back(ctx context.Context, timeout time.Duration) error {
	if !s.CanBack() {
		return ErrNoHistory
	}
	target := s.history[s.index-1]
	if err := s.load(target, timeout); err != nil {
		return err
	}
	s.index--
	s.url = target
	return nil
}

// Forward moves one entry forward. It is a no-op at the end of history.
func (s *Session) Forward(ctx context.Context, timeout time.Duration) error {
	if !s.CanForward() {
		return ErrNoHistory
	}
	target := s.history[s.index+1]
	if err := s.load(target, timeout); err != nil {
		return err
	}
	s.index++
	s.url = target
	return nil
}

// Reload reloads the current URL without adding a history entry.
func (s *Session) Reload(ctx context.Context, timeout time.Duration) error {
	return s.load(s.url, timeout)
}

// History returns the owned history and the current index.
func (s *Session) History() ([]string, int) {
	return append([]string(nil), s.history...), s.index
}

// Close stops Chromium and its child processes.
func (s *Session) Close() {
	if s != nil && s.cancel != nil {
		s.cancel()
	}
}

func (s *Session) load(target string, timeout time.Duration) error {
	loadCtx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()
	if err := chromedp.Do(loadCtx,
		chromedp.Navigate(target),
		chromedp.WaitReady("body"),
	); err != nil {
		return fmt.Errorf("load %s: %w", target, err)
	}
	return nil
}

// ErrNoHistory means Back/Forward had nowhere to go.
var ErrNoHistory = errors.New("no history entry")

// Normalize adds https:// to a bare host and accepts http, https and file.
func Normalize(raw string) (string, error) {
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
