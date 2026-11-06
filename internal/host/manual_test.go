package host

import (
	"context"
	"errors"
	"testing"

	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/browser"
)

func TestManualInputRequiresPinnedTargetAndNeverQueuesBehindBusyWork(t *testing.T) {
	b := testBridge([]action.TabInfo{{ID: "tab", TargetID: "target", Active: true}})
	target := action.Target{BrowserID: "browser-test", TabID: "tab", TargetID: "target"}
	called := false
	input := func(*browser.Session) error { called = true; return nil }
	if err := b.ManualTarget(context.Background(), action.Target{}, input); err == nil {
		t.Fatal("manual input accepted an unpinned target")
	}
	b.InputBlocked.Add(1)
	var ae *action.Error
	if err := b.ManualTarget(context.Background(), target, input); !errors.As(err, &ae) || ae.Code != "target_busy" {
		t.Fatalf("active mutation: %v", err)
	}
	b.InputBlocked.Add(-1)
	release, err := acquireGate(context.Background(), &b.operatorGates, "tab")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.ManualTarget(context.Background(), target, input); !errors.As(err, &ae) || ae.Code != "target_busy" {
		t.Fatalf("occupied tab gate: %v", err)
	}
	release()
	readRelease, err := b.acquire(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	// The fake snapshot has no session, but the error must get past input
	// arbitration: a screenshot or other observation must not block typing.
	if err := b.ManualTarget(context.Background(), target, input); !errors.As(err, &ae) || ae.Code != "target_not_found" {
		t.Fatalf("read-only observation blocked manual input: %v", err)
	}
	readRelease()
	if called {
		t.Fatal("a rejected manual event was dispatched")
	}
	if !b.Interaction().InputConflict {
		t.Fatal("manual input conflict was not reported")
	}
}
