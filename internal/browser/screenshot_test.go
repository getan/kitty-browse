package browser

import (
	"context"
	"errors"
	"testing"
)

func TestScreenshotCanceledCallerDoesNotNeedBrowser(t *testing.T) {
	caller, cancel := context.WithCancel(context.Background())
	cancel()

	session := &Session{ctx: context.Background()}
	data, err := session.Screenshot(caller, false)
	if data != nil {
		t.Fatalf("Screenshot returned %d bytes, want nil", len(data))
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Screenshot error = %v, want context.Canceled", err)
	}
}

func TestValidScreenshotDimension(t *testing.T) {
	for _, value := range []float64{0, -1} {
		if validScreenshotDimension(value) {
			t.Errorf("validScreenshotDimension(%v) = true", value)
		}
	}
	if !validScreenshotDimension(1) {
		t.Error("validScreenshotDimension(1) = false")
	}
}
