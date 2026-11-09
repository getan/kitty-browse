package browserhost

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Sessions assigns at most one live view to each trusted conversation ID.
// It is an in-process registry: a cross-process broker must authenticate callers
// and derive IDs and socket paths before invoking it.
type Sessions struct {
	mu      sync.Mutex
	hub     *Hub
	views   map[string]*sessionView
	opening func(context.Context, ViewOptions) (*Host, error) // test seam
}

type sessionView struct {
	socket    string
	socketKey string
	ready     chan struct{}
	removed   chan struct{}
	view      *Host
	err       error
}

// NewSessions creates the conversation registry for one shared profile.
func NewSessions(hub *Hub) *Sessions {
	return &Sessions{hub: hub, views: make(map[string]*sessionView)}
}

// Open returns the existing view for the same conversation and socket, or
// creates one. The URL is only used on creation: attaching never navigates an
// existing page. A canceled waiter does not cancel another caller's launch.
func (s *Sessions) Open(ctx context.Context, id string, opts ViewOptions) (*Host, error) {
	if s == nil || ctx == nil {
		return nil, errors.New("browser sessions and context are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validSessionID(id) || !filepath.IsAbs(opts.Socket) || filepath.Clean(opts.Socket) != opts.Socket {
		return nil, errors.New("a valid conversation ID and control socket are required")
	}
	socketKey := sessionSocketKey(opts.Socket)
	s.mu.Lock()
	if s.views == nil {
		s.views = make(map[string]*sessionView)
	}
	if existing := s.views[id]; existing != nil {
		if existing.socket != opts.Socket {
			s.mu.Unlock()
			return nil, errors.New("conversation is already bound to a different browser socket")
		}
		s.mu.Unlock()
		return waitSessionView(ctx, existing)
	}
	for otherID, other := range s.views {
		if other.socketKey == socketKey && otherID != id {
			s.mu.Unlock()
			return nil, errors.New("browser socket is already bound to another conversation")
		}
	}
	entry := &sessionView{socket: opts.Socket, socketKey: socketKey, ready: make(chan struct{}), removed: make(chan struct{})}
	s.views[id] = entry
	s.mu.Unlock()

	open := s.opening
	if open == nil {
		if s.hub == nil {
			open = func(context.Context, ViewOptions) (*Host, error) {
				return nil, errors.New("browser hub is unavailable")
			}
		} else {
			open = s.hub.OpenView
		}
	}
	view, err := open(ctx, opts)
	if err != nil && view != nil {
		_ = view.Close()
		view = nil
	}
	if err == nil && ctx.Err() != nil {
		if view != nil {
			_ = view.Close()
		}
		view, err = nil, ctx.Err()
	}
	if err == nil && (view == nil || view.Socket() != opts.Socket) {
		if view != nil {
			_ = view.Close()
		}
		view, err = nil, errors.New("created browser view did not bind the requested socket")
	}
	s.mu.Lock()
	entry.view, entry.err = view, err
	if err != nil && s.views[id] == entry {
		delete(s.views, id)
		close(entry.removed)
	}
	close(entry.ready)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	go func() {
		_ = view.Wait()
		s.mu.Lock()
		if s.views[id] == entry {
			delete(s.views, id)
			close(entry.removed)
		}
		s.mu.Unlock()
	}()
	return view, nil
}

func waitSessionView(ctx context.Context, entry *sessionView) (*Host, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-entry.ready:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.err != nil {
			return nil, entry.err
		}
		select {
		case <-entry.view.done:
			return nil, errors.New("browser view closed; discover or open it again")
		default:
		}
		return entry.view, nil
	}
}

func validSessionID(id string) bool {
	if id == "" || len(id) > 256 || !utf8.ValidString(id) || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func sessionSocketKey(socket string) string {
	dir := filepath.Dir(socket)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return filepath.Join(dir, filepath.Base(socket))
}
