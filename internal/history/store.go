// Package history persists recently visited URLs and bookmarks.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Entry is one visited or bookmarked URL.
type Entry struct {
	URL     string    `json:"url"`
	Visited time.Time `json:"visited,omitempty"`
}

// State is the JSON document on disk.
type State struct {
	History   []Entry `json:"history"`
	Bookmarks []Entry `json:"bookmarks"`
}

// Store is a loaded history/bookmark document.
type Store struct {
	path  string
	state State
}

// DefaultPath returns $XDG_CONFIG_HOME/kitty-browse/state.json on Unix-like
// systems, falling back to the user config directory.
func DefaultPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "kitty-browse", "state.json"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kitty-browse", "state.json"), nil
}

// Open loads the store. A missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &s.state); err != nil {
			return nil, fmt.Errorf("decode history: %w", err)
		}
	}
	return s, nil
}

// Path returns the file backing the store.
func (s *Store) Path() string { return s.path }

// Record moves url to the front of history, removes duplicates and caps the
// list.
func (s *Store) Record(url string) error {
	if url == "" {
		return nil
	}
	next := []Entry{{URL: url, Visited: time.Now()}}
	for _, e := range s.state.History {
		if e.URL != url {
			next = append(next, e)
		}
	}
	if len(next) > 500 {
		next = next[:500]
	}
	s.state.History = next
	return s.save()
}

// ToggleBookmark adds or removes url and reports the new state.
func (s *Store) ToggleBookmark(url string) (bool, error) {
	if url == "" {
		return false, nil
	}
	for i, e := range s.state.Bookmarks {
		if e.URL == url {
			s.state.Bookmarks = append(s.state.Bookmarks[:i], s.state.Bookmarks[i+1:]...)
			return false, s.save()
		}
	}
	s.state.Bookmarks = append([]Entry{{URL: url, Visited: time.Now()}}, s.state.Bookmarks...)
	return true, s.save()
}

// IsBookmarked reports whether url is in the bookmark list.
func (s *Store) IsBookmarked(url string) bool {
	for _, e := range s.state.Bookmarks {
		if e.URL == url {
			return true
		}
	}
	return false
}

// History returns a copy of the recent URLs.
func (s *Store) History() []string {
	out := make([]string, 0, len(s.state.History))
	for _, e := range s.state.History {
		out = append(out, e.URL)
	}
	return out
}

// Bookmarks returns a copy of the bookmark URLs.
func (s *Store) Bookmarks() []string {
	out := make([]string, 0, len(s.state.Bookmarks))
	for _, e := range s.state.Bookmarks {
		out = append(out, e.URL)
	}
	return out
}

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create history dir: %w", err)
	}
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}
