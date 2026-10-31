package browsepane

import (
	"testing"
	"time"
)

func TestIdleStillCapturedOnlyOncePerChangedFrame(t *testing.T) {
	now := time.Now()
	last := now.Add(-time.Second)
	if !shouldCaptureStill(now, last, time.Time{}, time.Time{}, false, true) {
		t.Fatal("first idle frame must get a PNG upgrade")
	}
	if shouldCaptureStill(now.Add(time.Minute), last, now, last, true, true) {
		t.Fatal("unchanged idle page must not redraw periodically")
	}
	if !shouldCaptureStill(now.Add(time.Minute), now.Add(time.Second), now, last, true, true) {
		t.Fatal("new page content must get a new PNG upgrade")
	}
	if shouldCaptureStill(now, now.Add(-time.Millisecond), time.Time{}, last, true, true) {
		t.Fatal("moving page must not be captured as idle")
	}
	if shouldCaptureStill(now, last, now.Add(-time.Second), time.Time{}, false, true) {
		t.Fatal("failed screenshot attempts must be throttled too")
	}
	if !shouldCaptureStill(now, time.Time{}, time.Time{}, time.Time{}, true, true) {
		t.Fatal("silent screencast must retain the screenshot fallback")
	}
	if !shouldCaptureStill(now, now, time.Time{}, time.Time{}, false, false) {
		t.Fatal("a pane with no visible image must seed a PNG without waiting for stream idleness")
	}
	if shouldCaptureStill(now, now, now, time.Time{}, false, false) {
		t.Fatal("a failed initial PNG must return to the normal retry throttle")
	}
}
