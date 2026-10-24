package browsepane

import (
	"bytes"
	"context"
	"flag"
	"io"
	"strings"
	"testing"
	"time"
)

// The library must answer -version exactly like the binary, and the shape of
// that line is a contract golder parses.
func TestVersionLineShape(t *testing.T) {
	line := VersionLine()
	if !strings.HasPrefix(line, "kitty-browse ") {
		t.Fatalf("version line = %q, want the kitty-browse prefix", line)
	}
	if !strings.Contains(line, "(commit ") || !strings.Contains(line, ", built ") {
		t.Fatalf("version line = %q, want commit and build time", line)
	}
}

func TestRunAnswersTheThreeVersionSpellings(t *testing.T) {
	for _, arg := range []string{"-version", "--version", "version"} {
		var out bytes.Buffer
		if err := Run(context.Background(), Options{Args: []string{arg}, Stdout: &out, Stderr: io.Discard, Version: "kitty-browse test (commit abc, built now)"}); err != nil {
			t.Fatalf("%q: %v", arg, err)
		}
		if got := strings.TrimRight(out.String(), "\n"); got != "kitty-browse test (commit abc, built now)" || strings.Count(got, "\n") != 0 {
			t.Fatalf("%q printed %q, want one line", arg, out.String())
		}
	}
}

// Asking for help is a success, and the library must print its usage to the
// writer it was given instead of os.Stderr.
func TestRunHelpReturnsNilAndWritesUsage(t *testing.T) {
	var stderr bytes.Buffer
	if err := Run(context.Background(), Options{Args: []string{"-h"}, Stdout: io.Discard, Stderr: &stderr}); err != nil {
		t.Fatalf("help error = %v, want nil", err)
	}
	if !strings.Contains(stderr.String(), "usage:") || !strings.Contains(stderr.String(), "-control-socket") {
		t.Fatalf("usage output = %q", stderr.String())
	}
}

// The pane's subcommands keep their historical help behaviour too.
func TestRunSubcommandHelpReturnsNil(t *testing.T) {
	for _, args := range [][]string{{"action", "-h"}, {"info", "-h"}, {"screenshot", "-h"}} {
		if err := Run(context.Background(), Options{Args: args, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

// Every rejection returns an error before a browser is started: no os.Exit, no
// process, no terminal switching.
func TestRunRejectsBadInvocationWithoutExiting(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"-nope"}, "flag provided but not defined"},
		{"invalid fps", []string{"-fps", "0"}, "fps must be finite"},
		{"invalid scroll", []string{"-fps", "24", "-scroll-lines", "0"}, "scroll-lines must be finite"},
		{"two urls", []string{"https://example.com", "https://example.org"}, "pass flags before the single optional URL"},
		{"conflicting sockets", []string{"-socket", "/tmp/a", "-control-socket", "/tmp/b"}, "must name the same path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			err := Run(context.Background(), Options{Args: tt.args, Stdout: io.Discard, Stderr: &stderr})
			if err == nil || !strings.Contains(err.Error()+stderr.String(), tt.want) {
				t.Fatalf("error = %v, stderr = %q, want %q", err, stderr.String(), tt.want)
			}
		})
	}
}

// A host application's own flag set must survive a pane run untouched.
func TestRunDoesNotTouchFlagCommandLine(t *testing.T) {
	count := func() int {
		entries := 0
		flag.VisitAll(func(*flag.Flag) { entries++ })
		return entries + flag.NFlag()
	}
	before := count()
	for _, arg := range []string{"fps", "control-socket", "socket", "profile-dir", "private", "chrome"} {
		if err := Run(context.Background(), Options{Args: []string{"-h"}, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
			t.Fatal(err)
		}
		if flag.Lookup(arg) != nil {
			t.Fatalf("Run registered -%s on flag.CommandLine", arg)
		}
	}
	if after := count(); after != before {
		t.Fatalf("flag.CommandLine changed from %d to %d entries", before, after)
	}
}

// The client subcommands still refuse to start a browser implicitly.
func TestRunClientCommandsNeedASocket(t *testing.T) {
	t.Setenv("KITTY_BROWSE_SOCKET", "")
	for _, args := range [][]string{{"info"}, {"screenshot"}, {"action", "--", "snapshot"}} {
		err := Run(context.Background(), Options{Args: args, Stdout: io.Discard, Stderr: io.Discard})
		if err == nil {
			t.Fatalf("%v started something instead of requiring a socket", args)
		}
	}
}

// A cancelled parent context must stop startup instead of launching anything,
// and Run must return rather than block forever.
func TestRunStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Args: []string{"-private", "about:blank"}, Stdout: io.Discard, Stderr: io.Discard})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled run reported success")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a cancelled run did not return")
	}
}
