package browser

import (
	"context"
	"testing"

	"github.com/chromedp/cdproto/emulation"
)

func TestPureUA(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "headless desktop token",
			in:   "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/143.0.7499.40 Safari/537.36",
			want: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.7499.40 Safari/537.36",
		},
		{
			name: "normal chromium unchanged",
			in:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.7444.175 Safari/537.36",
			want: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.7444.175 Safari/537.36",
		},
		{name: "empty unchanged", in: "", want: ""},
		{name: "unrelated word unchanged", in: "Headless browser", want: "Headless browser"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pureUA(tt.in); got != tt.want {
				t.Fatalf("pureUA(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIdentityOptionsContextInheritance(t *testing.T) {
	opts := IdentityOptions{Stealth: true, UserAgent: "example/1"}
	parent := withIdentityOptions(context.Background(), opts)
	child, cancel := context.WithCancel(parent)
	defer cancel()

	got, ok := child.Value(identityOptionsKey{}).(IdentityOptions)
	if !ok || got != opts {
		t.Fatalf("inherited identity options = %#v, %v; want %#v", got, ok, opts)
	}
}

func TestApplyIdentityDefaultIsNoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := applyIdentity(ctx); err != nil {
		t.Fatalf("empty identity options touched the canceled browser context: %v", err)
	}
}

func TestDesktopBrandsPreservesRealVersionsWithoutMutation(t *testing.T) {
	native := []*emulation.UserAgentBrandVersion{
		{Brand: "Not A Brand", Version: "99"},
		{Brand: "Chromium", Version: "158.0.8117.0"},
		{Brand: "HeadlessChrome", Version: "158.0.8117.0"},
	}
	got := desktopBrands(native)
	if len(got) != 2 || got[0].Brand != "Not A Brand" || got[1].Brand != "Chromium" || got[1].Version != "158.0.8117.0" {
		t.Fatalf("desktop brands = %+v", got)
	}
	if native[2].Brand != "HeadlessChrome" {
		t.Fatal("native metadata was mutated")
	}
}
