// Package testchromium adapts a real Chromium for integration tests that also
// run on a disposable Linux CI runner. It deliberately does not change how the
// browser is launched in production.
package testchromium

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Path returns chrome unchanged on a normal machine. On Linux CI it returns a
// wrapper that starts Chromium with --no-sandbox: the runners disable
// unprivileged user namespaces, so Chromium's own sandbox aborts before any
// page is loaded. That is a property of this disposable test runner, not a
// reason to weaken the production launcher.
func Path(t testing.TB, chrome string) string {
	t.Helper()
	if runtime.GOOS != "linux" || !onCI() {
		return chrome
	}
	return Wrap(t, chrome, "--no-sandbox")
}

// Wrap returns a shell wrapper that execs program with args and then the
// caller's arguments. It is exported for tests that need the same adaptation
// but launch the browser through a different seam.
func Wrap(t testing.TB, program string, args ...string) string {
	t.Helper()
	wrapper := filepath.Join(t.TempDir(), "chromium-wrapper")
	command := quote(program)
	for _, arg := range args {
		command += " " + quote(arg)
	}
	body := "#!/bin/sh\nexec " + command + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

func onCI() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CI"))) {
	case "", "0", "false":
		return false
	default:
		return true
	}
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
