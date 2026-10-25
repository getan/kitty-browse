package testchromium

import (
	"os/exec"
	"strings"
	"testing"
)

func TestWrapForwardsTheCallersArgumentsAfterTheFlags(t *testing.T) {
	wrapper := Wrap(t, "/bin/echo", "--no-sandbox")
	out, err := exec.Command(wrapper, "hello").CombinedOutput()
	if err != nil {
		t.Fatalf("run wrapper: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "--no-sandbox hello" {
		t.Fatalf("wrapper output = %q", got)
	}
}

func TestPathDoesNotWrapOutsideLinuxCI(t *testing.T) {
	t.Setenv("CI", "")
	if got := Path(t, "/bin/echo"); got != "/bin/echo" {
		t.Fatalf("Path changed the program outside CI: %q", got)
	}
}
