package browsepane

import (
	"bytes"
	"testing"
)

func TestStartupWriterNeverLogsAfterStop(t *testing.T) {
	var out bytes.Buffer
	w := &startupOnlyWriter{writer: &out}
	_, _ = w.Write([]byte("startup diagnostic"))
	w.stop()
	_, _ = w.Write([]byte("private page/keyboard data"))
	if got := out.String(); got != "startup diagnostic" {
		t.Fatalf("startup output = %q", got)
	}
}
