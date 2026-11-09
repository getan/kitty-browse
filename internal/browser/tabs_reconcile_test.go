package browser

import (
	"testing"

	cdptarget "github.com/chromedp/cdproto/target"
)

func TestOnlyKnownPagePopupsCanStealFocus(t *testing.T) {
	known := map[string]bool{"opener": true}
	live := map[string]bool{"opener": true}
	for _, tc := range []struct {
		name, url, opener string
		want              bool
	}{
		{"business popup", "https://example.test/popup", "opener", true},
		{"restored business", "https://example.test/restored", "", false},
		{"internal with opener", "chrome://settings/help", "opener", false},
		{"internal restored", "chrome://settings/help", "", false},
		{"local file", "file:///etc/passwd", "opener", false},
		{"credentials", "https://user:pass@example.test/", "opener", false},
		{"blank", "about:blank", "opener", false},
		{"unknown opener", "https://example.test/", "other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &cdptarget.Info{URL: tc.url, OpenerID: cdptarget.ID(tc.opener)}
			if got := autoActivatePopup(info, known, live); got != tc.want {
				t.Fatalf("autoActivatePopup(%+v) = %v, want %v", info, got, tc.want)
			}
		})
	}
	if autoActivatePopup(&cdptarget.Info{URL: "https://example.test", OpenerID: "opener"}, known, nil) {
		t.Fatal("a closed opener selected its popup")
	}
}

func TestScopedTabsAdoptOnlyTheirOwnPopups(t *testing.T) {
	owned := map[string]bool{"a": true, "a-popup": true, "closed": true}
	live := map[string]bool{"a": true, "a-popup": true}
	for _, tc := range []struct {
		name string
		info *cdptarget.Info
		want bool
	}{
		{"own popup", &cdptarget.Info{OpenerID: "a"}, true},
		{"own descendant", &cdptarget.Info{OpenerID: "a-popup"}, true},
		{"another view's popup", &cdptarget.Info{OpenerID: "b"}, false},
		{"closed opener", &cdptarget.Info{OpenerID: "closed"}, false},
		{"top-level page", &cdptarget.Info{}, false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownedPopup(tc.info, owned, live); got != tc.want {
				t.Fatalf("ownedPopup(%+v) = %v, want %v", tc.info, got, tc.want)
			}
		})
	}
}
