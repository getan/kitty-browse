package term

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseKittyGraphicsReply(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  bool
	}{
		{"\x1b_Gi=31;OK\x1b\\", true},
		{"\x1b_GOK\x1b\\", true},
		{"\x1b_Gi=31;ENOTSUPPORTED:no\x1b\\", false},
		{"", false},
	} {
		if got := ParseKittyGraphicsReply(tc.reply); got != tc.want {
			t.Errorf("ParseKittyGraphicsReply(%q) = %v, want %v", tc.reply, got, tc.want)
		}
	}
}

func TestQueryKittyGraphicsReadsTheTerminalAnswer(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	defer outR.Close()
	defer outW.Close()
	seen := make(chan string, 1)
	go func() {
		buf := make([]byte, 256)
		n, _ := outR.Read(buf)
		seen <- string(buf[:n])
		_, _ = inW.Write([]byte("\x1b_Gi=31;OK\x1b\\"))
	}()
	start := time.Now()
	if !QueryKittyGraphics(inR, outW) {
		t.Fatal("a terminal that answered OK was reported as unsupported")
	}
	if got := <-seen; !strings.Contains(got, "a=q") || !strings.Contains(got, "i=31") {
		t.Fatalf("query = %q", got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("probe took %s", time.Since(start))
	}
}

func TestQueryKittyGraphicsTreatsSilenceAsUnsupported(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	defer outR.Close()
	defer outW.Close()
	go func() { _, _ = outR.Read(make([]byte, 256)) }()
	if QueryKittyGraphics(inR, outW) {
		t.Fatal("silence was reported as graphics support")
	}
}

func TestLateGraphicsReplyIsNotTyped(t *testing.T) {
	events, err := Parse([]byte("\x1b_Gi=31;OK\x1b\\A"))
	if err != nil {
		t.Fatal(err)
	}
	var typed []rune
	for _, ev := range events {
		if ev.Kind == KeyText {
			typed = append(typed, ev.Rune)
		}
	}
	if string(typed) != "A" {
		t.Fatalf("typed = %q, want only the A key (APC reply must not become text)", string(typed))
	}
}
