package render

import "testing"

func TestForcedKittyEncoderIgnoresTheEnvironment(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("KITTY_PID", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "")
	if !(&kittyEncoder{force: true}).Available() {
		t.Fatal("a terminal that answered the kitty query was still unavailable")
	}
}
