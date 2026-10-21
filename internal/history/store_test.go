package history

import (
	"path/filepath"
	"testing"
)

func TestRecordAndBookmarks(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"https://a", "https://b", "https://a"} {
		if err := s.Record(u); err != nil {
			t.Fatal(err)
		}
	}
	got := s.History()
	if len(got) != 2 || got[0] != "https://a" || got[1] != "https://b" {
		t.Fatalf("history = %v", got)
	}
	on, err := s.ToggleBookmark("https://a")
	if err != nil || !on || !s.IsBookmarked("https://a") {
		t.Fatalf("toggle = %v %v", on, err)
	}
	off, err := s.ToggleBookmark("https://a")
	if err != nil || off || s.IsBookmarked("https://a") {
		t.Fatalf("untoggle = %v %v", off, err)
	}
}

func TestReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	_ = s.Record("https://example.com")
	_, _ = s.ToggleBookmark("https://example.com")
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.History()) != 1 || len(s2.Bookmarks()) != 1 {
		t.Fatalf("reopen = %v %v", s2.History(), s2.Bookmarks())
	}
}
