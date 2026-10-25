package browsepane

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestChromiumOutputIsQuotedAndChunked(t *testing.T) {
	var b bytes.Buffer
	w := chromiumLogWriter{logger: log.New(&b, "", 0)}
	input := []byte("GPU diagnostic\n\x1b[2J" + strings.Repeat("x", 5000))
	n, err := w.Write(input)
	if err != nil || n != len(input) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if strings.Contains(b.String(), "\x1b") || strings.Count(b.String(), "chromium output:") != 2 {
		t.Fatal("Chromium output must not inject terminal escapes or unbounded lines")
	}
	if !strings.Contains(b.String(), `\n\x1b[2J`) {
		t.Fatal("escaped diagnostic information was lost")
	}
}
