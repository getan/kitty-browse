package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

const launchIntegrationTimeout = 20 * time.Second

type launchStorageState struct {
	Cookie       string `json:"cookie"`
	LocalStorage string `json:"localStorage"`
}

func TestLaunchStoragePersistenceAndPrivateIsolationIntegration(t *testing.T) {
	chrome := launchIntegrationChrome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>launch storage</title><body>storage</body>`)
	}))
	defer server.Close()

	profileDir := t.TempDir()
	persistent, persistentCancel := launchIntegrationTabs(t, LaunchOptions{
		ExecutablePath: chrome,
		ProfileDir:     profileDir,
	}, server.URL)
	setStorage := `(() => {
		document.cookie = "persistentCookie=persistent-value; Max-Age=86400; Expires=Wed, 01 Jan 2038 00:00:00 GMT; Path=/; SameSite=Lax";
		localStorage.setItem("persistentStorage", "persistent-local-value");
		return {cookie: document.cookie, localStorage: localStorage.getItem("persistentStorage") || ""};
	})()`
	state := evaluateLaunchStorage(t, persistent.Active(), setStorage)
	assertLaunchStorage(t, state, "persistentCookie=persistent-value", "persistent-local-value")
	persistent.Close()
	persistentCancel()

	// Reopen through the public launch pipeline, rather than reading Chromium's
	// cookie database. The same origin must see both kinds of persisted storage.
	persistent, persistentCancel = launchIntegrationTabs(t, LaunchOptions{
		ExecutablePath: chrome,
		ProfileDir:     profileDir,
	}, server.URL)
	defer func() {
		persistent.Close()
		persistentCancel()
	}()
	state = evaluateLaunchStorage(t, persistent.Active(), launchReadStorageJS)
	assertLaunchStorage(t, state, "persistentCookie=persistent-value", "persistent-local-value")

	privateFirst, privateFirstCancel := launchIntegrationTabs(t, LaunchOptions{
		ExecutablePath: chrome,
		Private:        true,
	}, server.URL)
	state = evaluateLaunchStorage(t, privateFirst.Active(), launchReadStorageJS)
	assertLaunchStorageEmpty(t, state, "first private context")

	privateDir := launchTemporaryUserDataDir(t, privateFirst.Active().Context())
	if info, err := os.Stat(privateDir); err != nil || !info.IsDir() {
		privateFirst.Close()
		privateFirstCancel()
		t.Fatalf("temporary user-data-dir %q is not a live directory: info=%v err=%v", privateDir, info, err)
	}
	state = evaluateLaunchStorage(t, privateFirst.Active(), `(() => {
		document.cookie = "privateCookie=private-value; Max-Age=86400; Expires=Wed, 01 Jan 2038 00:00:00 GMT; Path=/; SameSite=Lax";
		localStorage.setItem("privateStorage", "private-local-value");
		return {cookie: document.cookie, localStorage: localStorage.getItem("privateStorage") || ""};
	})()`)
	assertLaunchStorage(t, state, "privateCookie=private-value", "private-local-value")
	privateFirst.Close()
	privateFirstCancel()
	waitForLaunchPathRemoval(t, privateDir)

	privateSecond, privateSecondCancel := launchIntegrationTabs(t, LaunchOptions{
		ExecutablePath: chrome,
		Private:        true,
	}, server.URL)
	state = evaluateLaunchStorage(t, privateSecond.Active(), `(() => ({
		cookie: document.cookie,
		localStorage: (localStorage.getItem("persistentStorage") || "") + (localStorage.getItem("privateStorage") || "")
	}))()`)
	assertLaunchStorageEmpty(t, state, "second private context after closing the first")
	privateSecond.Close()
	privateSecondCancel()

	// The reopened persistent browser remains isolated from both private
	// launches and still owns its original durable state.
	state = evaluateLaunchStorage(t, persistent.Active(), launchReadStorageJS)
	assertLaunchStorage(t, state, "persistentCookie=persistent-value", "persistent-local-value")
	if strings.Contains(state.Cookie, "privateCookie=") {
		t.Fatalf("persistent profile received private cookie: %q", state.Cookie)
	}
}

const launchReadStorageJS = `(() => ({
	cookie: document.cookie,
	localStorage: localStorage.getItem("persistentStorage") || ""
}))()`

type launchIdentityRequest struct {
	UserAgent string
}

type launchNavigatorIdentity struct {
	UserAgent string `json:"userAgent"`
	Webdriver bool   `json:"webdriver"`
}

func TestLaunchIdentityPipelineIntegration(t *testing.T) {
	chrome := launchIntegrationChrome(t)
	var mu sync.Mutex
	requests := make(map[string]launchIdentityRequest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.URL.Path] = launchIdentityRequest{UserAgent: r.UserAgent()}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>launch identity</title><body>identity</body>`)
	}))
	defer server.Close()

	stealth, stealthCancel := launchIntegrationTabs(t, LaunchOptions{
		ExecutablePath: chrome,
		Private:        true,
		Stealth:        true,
	}, server.URL+"/first")
	assertLaunchIdentity(t, stealth.Active(), launchRequestUA(t, &mu, requests, "/first"), "")
	if err := stealth.New(context.Background(), server.URL+"/second", launchIntegrationTimeout); err != nil {
		stealth.Close()
		stealthCancel()
		t.Fatalf("open inherited-identity tab: %v", err)
	}
	assertLaunchIdentity(t, stealth.Active(), launchRequestUA(t, &mu, requests, "/second"), "")
	stealth.Close()
	stealthCancel()

	const customUA = "KittyBrowse-Launch-Test/1.0 Chrome/123.0"
	custom, customCancel := launchIntegrationTabs(t, LaunchOptions{
		ExecutablePath: chrome,
		Private:        true,
		Stealth:        true,
		UserAgent:      customUA,
	}, server.URL+"/custom")
	defer func() {
		custom.Close()
		customCancel()
	}()
	assertLaunchIdentity(t, custom.Active(), launchRequestUA(t, &mu, requests, "/custom"), customUA)
}

// Opt in with an actual proxy and external target when validating a user's
// network route, for example:
//
//	KITTY_BROWSE_CHROME=/path/to/Chromium \
//	KITTY_BROWSE_TEST_PROXY=http://127.0.0.1:7897 \
//	KITTY_BROWSE_TEST_PROXY_TARGET=https://www.v2ex.com/ \
//	go test ./internal/browser -run TestExplicitProxyIntegration -v
func TestExplicitProxyIntegration(t *testing.T) {
	proxy := strings.TrimSpace(os.Getenv("KITTY_BROWSE_TEST_PROXY"))
	target := strings.TrimSpace(os.Getenv("KITTY_BROWSE_TEST_PROXY_TARGET"))
	if proxy == "" || target == "" {
		t.Skip("set KITTY_BROWSE_TEST_PROXY and KITTY_BROWSE_TEST_PROXY_TARGET")
	}
	tabs, _ := launchIntegrationTabs(t, LaunchOptions{
		ExecutablePath: launchIntegrationChrome(t),
		Private:        true,
		ProxyServer:    proxy,
	}, target)
	if err := tabs.Active().LoadError(); err != nil {
		t.Fatalf("proxied page load: %v", err)
	}
	if got := tabs.Active().URL(); got == "" || strings.HasPrefix(got, "chrome-error:") {
		t.Fatalf("proxied page URL = %q", got)
	}
}

func launchIntegrationChrome(t *testing.T) string {
	t.Helper()
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to a Chromium executable to run this integration test")
	}
	return chrome
}

func launchIntegrationTabs(t *testing.T, opts LaunchOptions, target string) (*Tabs, context.CancelFunc) {
	t.Helper()
	allocator, cancel, _, err := NewBrowserContext(context.Background(), opts)
	if err != nil {
		t.Fatalf("NewBrowserContext(%+v): %v", opts, err)
	}
	tabs, err := OpenTabs(allocator, target, launchIntegrationTimeout)
	if err != nil {
		cancel()
		t.Fatalf("OpenTabs(%q): %v", target, err)
	}
	t.Cleanup(func() {
		tabs.Close()
		cancel()
	})
	return tabs, cancel
}

func evaluateLaunchStorage(t *testing.T, session *Session, expression string) launchStorageState {
	t.Helper()
	state, err := chromedp.Run(session.Context(), chromedp.Evaluate[launchStorageState](expression))
	if err != nil {
		t.Fatalf("evaluate browser storage: %v", err)
	}
	return state
}

func assertLaunchStorage(t *testing.T, state launchStorageState, wantCookie, wantLocal string) {
	t.Helper()
	if !strings.Contains(state.Cookie, wantCookie) || state.LocalStorage != wantLocal {
		t.Fatalf("storage = {cookie:%q localStorage:%q}, want cookie containing %q and localStorage %q",
			state.Cookie, state.LocalStorage, wantCookie, wantLocal)
	}
}

func assertLaunchStorageEmpty(t *testing.T, state launchStorageState, where string) {
	t.Helper()
	if state.Cookie != "" || state.LocalStorage != "" {
		t.Fatalf("%s inherited storage: cookie=%q localStorage=%q", where, state.Cookie, state.LocalStorage)
	}
}

func launchTemporaryUserDataDir(t *testing.T, ctx context.Context) string {
	t.Helper()
	result, err := chromedp.Call(ctx, cdpbrowser.GetBrowserCommandLine, cdp.Empty{})
	if err != nil {
		t.Fatalf("Browser.getBrowserCommandLine: %v", err)
	}
	for i, argument := range result.Arguments {
		if value, ok := strings.CutPrefix(argument, "--user-data-dir="); ok && value != "" {
			return filepath.Clean(value)
		}
		if argument == "--user-data-dir" && i+1 < len(result.Arguments) && result.Arguments[i+1] != "" {
			return filepath.Clean(result.Arguments[i+1])
		}
	}
	t.Fatalf("browser command line has no user-data-dir: %q", result.Arguments)
	return ""
}

func waitForLaunchPathRemoval(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatalf("stat temporary user-data-dir %q: %v", path, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("temporary user-data-dir still exists after browser cancellation: %q", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func launchRequestUA(t *testing.T, mu *sync.Mutex, requests map[string]launchIdentityRequest, path string) string {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	request, ok := requests[path]
	if !ok {
		t.Fatalf("HTTP server did not receive request for %s", path)
	}
	return request.UserAgent
}

func assertLaunchIdentity(t *testing.T, session *Session, httpUA, wantUA string) {
	t.Helper()
	navigator, err := chromedp.Run(session.Context(), chromedp.Evaluate[launchNavigatorIdentity](`({
		userAgent: navigator.userAgent,
		webdriver: navigator.webdriver
	})`))
	if err != nil {
		t.Fatalf("read navigator identity: %v", err)
	}
	if httpUA != navigator.UserAgent {
		t.Fatalf("HTTP User-Agent %q != navigator.userAgent %q", httpUA, navigator.UserAgent)
	}
	if strings.Contains(httpUA, "HeadlessChrome/") {
		t.Fatalf("stealth launch exposed HeadlessChrome: %q", httpUA)
	}
	if navigator.Webdriver {
		t.Fatal("stealth launch exposed navigator.webdriver=true")
	}
	if wantUA != "" && (httpUA != wantUA || navigator.UserAgent != wantUA) {
		t.Fatalf("custom UserAgent did not take priority: HTTP=%q navigator=%q want=%q", httpUA, navigator.UserAgent, wantUA)
	}
}
