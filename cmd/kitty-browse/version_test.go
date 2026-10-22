package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// golder parses this line, so the shape is a contract, not cosmetics.
func TestVersionLineShape(t *testing.T) {
	line := versionLine()
	if !strings.HasPrefix(line, "kitty-browse ") {
		t.Fatalf("version line = %q, want the kitty-browse prefix", line)
	}
	if !strings.Contains(line, "(commit ") || !strings.Contains(line, ", built ") {
		t.Fatalf("version line = %q, want commit and build time", line)
	}
}

func TestHandleVersionAcceptsTheThreeSpellings(t *testing.T) {
	for _, arg := range []string{"-version", "--version", "version"} {
		if !handleVersion([]string{arg}) {
			t.Errorf("%q was not handled", arg)
		}
	}
	for _, args := range [][]string{nil, {"action"}, {"-h"}, {"https://example.com"}} {
		if handleVersion(args) {
			t.Errorf("%v was treated as a version request", args)
		}
	}
}

// The output is one line on stdout, because that is what a caller captures.
func TestHandleVersionPrintsOneLine(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = write
	ok := handleVersion([]string{"-version"})
	os.Stdout = old
	write.Close()
	if !ok {
		t.Fatal("version was not handled")
	}
	out, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimRight(string(out), "\n"), "\n") != 0 {
		t.Errorf("output = %q, want a single line", out)
	}
}
