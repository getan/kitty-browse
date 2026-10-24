//go:build unix

package browsepane

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// paneIntegrationChrome returns a Chromium executable, or skips. The env
// override keeps the repository's integration convention; the PATH lookup lets
// an ordinary developer machine run this without extra setup.
func paneIntegrationChrome(t *testing.T) string {
	t.Helper()
	if chrome := strings.TrimSpace(os.Getenv("KITTY_BROWSE_CHROME")); chrome != "" {
		return chrome
	}
	for _, name := range []string{"chromium", "chrome-headless-shell", "chromium-browser", "google-chrome", "chrome"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Skip("no Chromium found; set KITTY_BROWSE_CHROME to run this integration test")
	return ""
}

// privateProfiles counts kitty-browse's temporary private profiles, so the test
// can prove the pane released the browser it started.
func privateProfiles(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		// Keep in step with internal/browser's privateProfilePrefix.
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "kitty-browse-private-") {
			count++
		}
	}
	return count
}

// A real Chromium is started and then abandoned when the terminal cannot be
// opened. Run must report why, return promptly, and leave no browser behind:
// this is the teardown the embedding golder binary relies on.
func TestPaneWithoutTerminalReleasesBrowserIntegration(t *testing.T) {
	chrome := paneIntegrationChrome(t)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = read.Close()
		_ = write.Close()
	}()

	before := privateProfiles(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	started := time.Now()
	err = Run(ctx, Options{
		Args:   []string{"-private", "-chrome", chrome, "about:blank"},
		Stdin:  read,
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "stdin is not a terminal") {
		t.Fatalf("error = %v, want the non-terminal rejection", err)
	}
	if elapsed := time.Since(started); elapsed > 55*time.Second {
		t.Fatalf("startup plus teardown took %v", elapsed)
	}
	if after := privateProfiles(t); after > before {
		t.Fatalf("private profile directories leaked: %d before, %d after", before, after)
	}
}
