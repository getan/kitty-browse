package main

import (
	"fmt"
	"os"
	"strings"
)

// Build metadata, injected at release time by goreleaser. A `go build` from
// source keeps the defaults, so `kitty-browse -version` still answers.
//
// golder reads this line to enforce its minimum-version contract; keep the
// format stable: "kitty-browse <version> (commit <sha>, built <time>)".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// versionLine is the one line every caller parses.
func versionLine() string {
	return fmt.Sprintf("kitty-browse %s (commit %s, built %s)", version, commit, date)
}

// handleVersion answers -version, --version and the version subcommand before
// any browser work starts. It returns true when the caller should stop.
func handleVersion(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch strings.TrimSpace(args[0]) {
	case "-version", "--version", "version":
		fmt.Fprintln(os.Stdout, versionLine())
		return true
	}
	return false
}
