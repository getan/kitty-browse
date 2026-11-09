package browserhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/kitty-browse/internal/host"
)

func fakeSessionHost(socket string) *Host {
	h := &Host{socket: socket, quit: host.NewQuit(), done: make(chan struct{}), cancel: func() {}}
	go func() {
		<-h.quit.Done()
		close(h.done)
	}()
	return h
}

func TestSessionsOpenIsScopedAndConcurrent(t *testing.T) {
	dir := t.TempDir()
	aPath, bPath := filepath.Join(dir, "a.sock"), filepath.Join(dir, "b.sock")
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	s := NewSessions(nil)
	s.opening = func(ctx context.Context, opts ViewOptions) (*Host, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return fakeSessionHost(opts.Socket), nil
		}
	}
	type result struct {
		view *Host
		err  error
	}
	first := make(chan result, 1)
	go func() {
		view, err := s.Open(context.Background(), "conversation-a", ViewOptions{Socket: aPath})
		first <- result{view, err}
	}()
	<-started
	if _, err := s.Open(context.Background(), "conversation-b", ViewOptions{Socket: aPath}); err == nil {
		t.Fatal("another conversation claimed an in-flight socket")
	}
	if _, err := s.Open(context.Background(), "conversation-a", ViewOptions{Socket: bPath}); err == nil {
		t.Fatal("one conversation changed its socket during startup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Open(ctx, "conversation-a", ViewOptions{Socket: aPath}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting caller = %v, want deadline", err)
	}
	close(release)
	a := <-first
	if a.err != nil || a.view == nil {
		t.Fatalf("first view = %+v", a)
	}
	t.Cleanup(func() { _ = a.view.Close() })
	again, err := s.Open(context.Background(), "conversation-a", ViewOptions{Socket: aPath, URL: "https://different.example/"})
	if err != nil || again != a.view || calls.Load() != 1 {
		t.Fatalf("same session did not reuse its view: view=%p err=%v calls=%d", again, err, calls.Load())
	}
	b, err := s.Open(context.Background(), "conversation-b", ViewOptions{Socket: bPath})
	if err != nil || b == a.view || calls.Load() != 2 {
		t.Fatalf("separate session did not get a separate view: view=%p err=%v calls=%d", b, err, calls.Load())
	}
	t.Cleanup(func() { _ = b.Close() })
	s.mu.Lock()
	removed := s.views["conversation-a"].removed
	s.mu.Unlock()
	if err := a.view.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-removed:
	case <-time.After(time.Second):
		t.Fatal("closed view remained registered")
	}
	select {
	case <-b.done:
		t.Fatal("closing one view closed the other")
	default:
	}
	if _, err := s.Open(context.Background(), "conversation-a", ViewOptions{Socket: aPath}); err != nil {
		t.Fatalf("closed conversation could not open a new view: %v", err)
	} else {
		s.mu.Lock()
		view := s.views["conversation-a"].view
		s.mu.Unlock()
		t.Cleanup(func() { _ = view.Close() })
	}
}

func TestSessionsRefuseSocketAliases(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	s := NewSessions(nil)
	s.opening = func(_ context.Context, opts ViewOptions) (*Host, error) { return fakeSessionHost(opts.Socket), nil }
	a, err := s.Open(context.Background(), "a", ViewOptions{Socket: filepath.Join(dir, "view.sock")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if _, err := s.Open(context.Background(), "b", ViewOptions{Socket: filepath.Join(alias, "view.sock")}); err == nil {
		t.Fatal("two session IDs claimed aliases of the same socket")
	}
}

func TestSessionsCleanUpFailedStartup(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "view.sock")
	view := fakeSessionHost(socket)
	s := NewSessions(nil)
	s.opening = func(context.Context, ViewOptions) (*Host, error) {
		return view, errors.New("startup failed")
	}
	if _, err := s.Open(context.Background(), "a", ViewOptions{Socket: socket}); err == nil {
		t.Fatal("accepted a failed startup")
	}
	select {
	case <-view.done:
	default:
		t.Fatal("partially started view was not closed")
	}
	s.mu.Lock()
	_, retained := s.views["a"]
	s.mu.Unlock()
	if retained {
		t.Fatal("failed startup remained registered")
	}
}

func TestSessionsRefuseInvalidIdentity(t *testing.T) {
	s := NewSessions(nil)
	for _, id := range []string{"", "  ", " a", "a\n", string([]byte{0xff})} {
		if _, err := s.Open(context.Background(), id, ViewOptions{Socket: filepath.Join(t.TempDir(), "view.sock")}); err == nil {
			t.Errorf("accepted invalid session ID %q", id)
		}
	}
	if _, err := s.Open(context.Background(), "valid", ViewOptions{Socket: "relative.sock"}); err == nil {
		t.Fatal("accepted a relative socket path")
	}
}
