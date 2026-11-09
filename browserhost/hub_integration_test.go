//go:build unix

package browserhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/control"
)

// Opt in explicitly: this starts private Chromium, never a person's profile.
func TestHubIndependentViewsIntegration(t *testing.T) {
	if os.Getenv("KITTY_BROWSE_TEST_HUB") != "1" {
		t.Skip("set KITTY_BROWSE_TEST_HUB=1 to exercise private Chromium")
	}
	chrome := hostChrome(t)
	dir, err := os.MkdirTemp("", "kb-hub-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hub, err := StartHub(ctx, Options{Chrome: chrome, Private: true, LoadTimeout: hostRequestTimeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	aPath, bPath := filepath.Join(dir, "a.sock"), filepath.Join(dir, "b.sock")
	a, err := hub.OpenView(ctx, ViewOptions{URL: "about:blank", Socket: aPath})
	if err != nil {
		t.Fatal(err)
	}
	b, err := hub.OpenView(ctx, ViewOptions{URL: "about:blank", Socket: bPath})
	if err != nil {
		t.Fatal(err)
	}
	info := func(socket string) action.Info {
		t.Helper()
		raw, err := control.Info(ctx, socket)
		if err != nil {
			t.Fatal(err)
		}
		var result action.Info
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	aInfo, bInfo := info(aPath), info(bPath)
	if aInfo.BrowserID == bInfo.BrowserID || len(aInfo.Tabs) != 1 || len(bInfo.Tabs) != 1 || aInfo.Tabs[0].TargetID == bInfo.Tabs[0].TargetID {
		t.Fatalf("views were not isolated: A=%+v B=%+v", aInfo, bInfo)
	}
	hostAction(t, bPath, "tab.new", `{"url":"about:blank"}`)
	// The periodic reconciliation must not import the other view's pages.
	time.Sleep(1500 * time.Millisecond)
	afterA, afterB := info(aPath), info(bPath)
	if len(afterA.Tabs) != 1 || afterA.ActiveTabID != aInfo.ActiveTabID || len(afterB.Tabs) != 2 {
		t.Fatalf("another view changed A: A=%+v B=%+v", afterA, afterB)
	}
	request := fmt.Sprintf(`{"id":"wrong-view","action":"snapshot","target":{"browserId":%q,"tabId":%q,"targetId":%q},"params":{}}`, bInfo.BrowserID, aInfo.Tabs[0].ID, aInfo.Tabs[0].TargetID)
	raw, err := control.Action(ctx, bPath, []byte(request))
	if err != nil {
		t.Fatal(err)
	}
	var denied action.Response
	if err := json.Unmarshal(raw, &denied); err != nil || denied.OK || denied.Error == nil || denied.Error.Code != "target_not_found" {
		t.Fatalf("other view's tab was not refused: %s (%v)", raw, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if got := info(bPath); len(got.Tabs) != 2 {
		t.Fatalf("closing A affected B: %+v", got)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}
