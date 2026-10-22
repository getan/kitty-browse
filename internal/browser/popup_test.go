package browser

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	cdptarget "github.com/chromedp/cdproto/target"
)

func TestPopupObserverFiltersBoundsTokensAndConsumption(t *testing.T) {
	o := newPopupObserver()
	opener := "opener"
	o.targetCreated(opener, &cdptarget.Info{TargetID: "worker", Type: "worker", OpenerID: cdptarget.ID(opener)}, time.Now())
	o.targetCreated(opener, &cdptarget.Info{TargetID: "other", Type: "page", OpenerID: "another"}, time.Now())
	o.targetCreated(opener, &cdptarget.Info{TargetID: "extension", Type: "page", OpenerID: cdptarget.ID(opener), URL: "chrome-extension://id/page.html"}, time.Now())
	if len(o.popups) != 0 {
		t.Fatalf("non-popup targets retained: %#v", o.popups)
	}

	for i := 0; i < maxObservedPopups+5; i++ {
		id := cdptarget.ID(string(rune('a' + i)))
		o.targetCreated(opener, &cdptarget.Info{TargetID: id, Type: "page", OpenerID: cdptarget.ID(opener), URL: "https://example.test/" + string(id)}, time.Now())
	}
	if len(o.popups) != maxObservedPopups || len(o.byID) != maxObservedPopups {
		t.Fatalf("bounds = popups %d, index %d", len(o.popups), len(o.byID))
	}
	token := o.sequence
	o.targetCreated(opener, &cdptarget.Info{TargetID: "latest", Type: "page", OpenerID: cdptarget.ID(opener), URL: "https://example.test/receipt", Title: "Receipt"}, time.Now())
	candidates := o.candidates(popupWaitParams{SinceToken: &token}, time.Now())
	if len(candidates) != 1 || candidates[0].targetID != "latest" {
		t.Fatalf("since-token candidates = %#v", candidates)
	}
	result, ok := o.consume(candidates[0], &cdptarget.Info{URL: candidates[0].url, Title: candidates[0].title})
	if !ok || result.OpenerTargetID != opener || len(o.candidates(popupWaitParams{SinceToken: &token}, time.Now())) != 0 {
		t.Fatalf("popup was not consumed exactly once: %#v, %v", result, ok)
	}
}

func TestPopupPatternAndRecentWindow(t *testing.T) {
	for _, test := range []struct {
		pattern string
		value   string
		want    bool
	}{
		{"receipt", "https://example.test/receipt/1", true},
		{"*/receipt/?", "https://example.test/receipt/1", true},
		{"*/other", "https://example.test/receipt/1", false},
	} {
		if got := popupPatternMatch(test.pattern, test.value); got != test.want {
			t.Errorf("popupPatternMatch(%q, %q) = %v", test.pattern, test.value, got)
		}
	}
	o := newPopupObserver()
	o.targetCreated("parent", &cdptarget.Info{TargetID: "old", Type: "page", OpenerID: "parent"}, time.Now().Add(-recentPopupAge-time.Millisecond))
	if got := o.candidates(popupWaitParams{}, time.Now()); len(got) != 0 {
		t.Fatalf("default wait included stale popup: %#v", got)
	}
}

func TestWaitForPopupCancellationAndInvalidParams(t *testing.T) {
	s := &Session{ctx: context.Background()}
	popupObservers.Store(s, newPopupObserver())
	t.Cleanup(func() { stopPopupObservation(s) })
	if _, err := s.WaitForPopup(context.Background(), json.RawMessage(`{"unknown":true}`)); automationErrorCode(err) != "invalid_params" {
		t.Fatalf("invalid params error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := s.WaitForPopup(ctx, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v, want deadline exceeded", err)
	}
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := s.WaitForPopup(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait error = %v, want context canceled", err)
	}
}
