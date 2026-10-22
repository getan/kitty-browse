package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

type identityRequest struct {
	path     string
	ua       string
	chUA     string
	platform string
	mobile   string
}

type identityNavigator struct {
	UserAgent  string `json:"userAgent"`
	Webdriver  bool   `json:"webdriver"`
	HasData    bool   `json:"hasData"`
	BrandCount int    `json:"brandCount"`
	Platform   string `json:"platform"`
	Mobile     bool   `json:"mobile"`
}

func TestIdentityStealthAndTabInheritanceIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to a Chromium executable to run this integration test")
	}

	var mu sync.Mutex
	requests := make(map[string]identityRequest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.URL.Path] = identityRequest{
			path: r.URL.Path, ua: r.UserAgent(), chUA: r.Header.Get("Sec-CH-UA"),
			platform: r.Header.Get("Sec-CH-UA-Platform"), mobile: r.Header.Get("Sec-CH-UA-Mobile"),
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>identity</title><body>identity</body>`)
	}))
	defer server.Close()

	allocCtx, allocCancel := identityTestAllocator(t, chrome)
	defer allocCancel()
	allocCtx = withIdentityOptions(allocCtx, IdentityOptions{Stealth: true})

	first := newIdentityTarget(t, allocCtx, server.URL+"/first")
	defer first.cancel()
	assertStealthIdentity(t, first.ctx, requestForPath(t, &mu, requests, "/first"))

	// Derive the second target exactly as Tabs.New derives a new Session from
	// its anchor. The context value must survive and apply independently to it.
	second := newIdentityTarget(t, first.ctx, server.URL+"/second")
	defer second.cancel()
	assertStealthIdentity(t, second.ctx, requestForPath(t, &mu, requests, "/second"))
}

func TestIdentityCustomUserAgentDoesNotForgeMetadataIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to a Chromium executable to run this integration test")
	}
	const customUA = "KittyBrowse-Identity-Test/1.0"

	requestCh := make(chan identityRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			select {
			case requestCh <- identityRequest{
				ua: r.UserAgent(), chUA: r.Header.Get("Sec-CH-UA"),
				platform: r.Header.Get("Sec-CH-UA-Platform"), mobile: r.Header.Get("Sec-CH-UA-Mobile"),
			}:
			default:
			}
		}
		_, _ = fmt.Fprint(w, `<!doctype html><body>custom</body>`)
	}))
	defer server.Close()

	// Mirror LaunchOptions.UserAgent: the process flag may alter
	// Browser.getVersion, so the custom branch must not rely on that value.
	allocCtx, allocCancel := identityTestAllocator(t, chrome, chromedp.UserAgent(customUA))
	defer allocCancel()
	allocCtx = withIdentityOptions(allocCtx, IdentityOptions{Stealth: true, UserAgent: customUA})
	target := newIdentityTarget(t, allocCtx, server.URL)
	defer target.cancel()

	request := <-requestCh
	navigator := readIdentityNavigator(t, target.ctx)
	if request.ua != customUA || navigator.UserAgent != customUA {
		t.Fatalf("custom UA mismatch: HTTP=%q navigator=%q want=%q", request.ua, navigator.UserAgent, customUA)
	}
	if request.chUA != "" || request.platform != "" || request.mobile != "" {
		t.Fatalf("custom UA unexpectedly forged Client Hints: %+v", request)
	}
}

type identityTarget struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func newIdentityTarget(t *testing.T, parent context.Context, url string) identityTarget {
	t.Helper()
	ctx, cancel := chromedp.NewContext(parent)
	if err := chromedp.Do(ctx); err != nil {
		cancel()
		t.Fatalf("start Chromium target: %v", err)
	}
	if err := applyIdentity(ctx); err != nil {
		cancel()
		t.Fatalf("applyIdentity: %v", err)
	}
	loadCtx, loadCancel := context.WithTimeout(ctx, 15*time.Second)
	defer loadCancel()
	if err := chromedp.Do(loadCtx, chromedp.Navigate(url), chromedp.WaitReady("body")); err != nil {
		cancel()
		t.Fatalf("navigate after applyIdentity: %v", err)
	}
	return identityTarget{ctx: ctx, cancel: cancel}
}

func identityTestAllocator(t *testing.T, chrome string, extra ...chromedp.ExecAllocatorOption) (context.Context, context.CancelFunc) {
	t.Helper()
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(chrome),
		chromedp.Headless,
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
	}
	opts = append(opts, extra...)
	return chromedp.NewExecAllocator(context.Background(), opts...)
}

func readIdentityNavigator(t *testing.T, ctx context.Context) identityNavigator {
	t.Helper()
	value, err := chromedp.Run(ctx, chromedp.Evaluate[identityNavigator](`(() => {
  const data = navigator.userAgentData;
  return {
    userAgent: navigator.userAgent,
    webdriver: navigator.webdriver === true,
    hasData: !!data,
	    brandCount: data ? data.brands.length : 0,
    platform: data ? data.platform : "",
    mobile: data ? data.mobile : false
  };
})()`))
	if err != nil {
		t.Fatalf("read navigator identity: %v", err)
	}
	return value
}

func assertStealthIdentity(t *testing.T, ctx context.Context, request identityRequest) {
	t.Helper()
	navigator := readIdentityNavigator(t, ctx)
	if request.ua != navigator.UserAgent {
		t.Fatalf("HTTP UA %q != navigator.userAgent %q", request.ua, navigator.UserAgent)
	}
	if strings.Contains(request.ua, "HeadlessChrome/") || !strings.Contains(request.ua, "Chrome/") {
		t.Fatalf("stealth UA did not preserve Chrome while removing HeadlessChrome: %q", request.ua)
	}
	if navigator.Webdriver {
		t.Fatal("navigator.webdriver is true with the test allocator's automation flags")
	}
	if navigator.HasData && navigator.BrandCount > 0 {
		if request.chUA == "" {
			t.Fatal("navigator.userAgentData exists but Sec-CH-UA was not sent")
		}
		if strings.Trim(request.platform, `"`) != navigator.Platform {
			t.Fatalf("Client Hint platform=%q, navigator.userAgentData.platform=%q", request.platform, navigator.Platform)
		}
		wantMobile := "?0"
		if navigator.Mobile {
			wantMobile = "?1"
		}
		if request.mobile != wantMobile {
			t.Fatalf("Sec-CH-UA-Mobile=%q, want %q", request.mobile, wantMobile)
		}
	} else if request.chUA != "" || request.platform != "" || request.mobile != "" {
		t.Fatalf("Client Hint headers sent without navigator.userAgentData: %+v", request)
	}
}

func requestForPath(t *testing.T, mu *sync.Mutex, requests map[string]identityRequest, path string) identityRequest {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	request, ok := requests[path]
	if !ok {
		t.Fatalf("server did not receive %s", path)
	}
	return request
}
