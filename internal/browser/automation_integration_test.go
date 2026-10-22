package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestAutomationNativeCDPIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run automation integration")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/frame" {
			_, _ = fmt.Fprint(w, `<!doctype html><html><head><title>子页面</title></head><body><button id=fb>框内按钮</button><div id=ft>框内文本</div></body></html>`)
			return
		}
		_, _ = fmt.Fprint(w, `<!doctype html><html><head><title>自动化</title></head><body>
<button id=b onclick="this.textContent='已点击'">点击我</button><input id=q form=f aria-label="查询">
	<input id=p type=password value="DO-NOT-LEAK-SECRET"><input id=c type=checkbox><input id=cd type=checkbox disabled><input id=cf type=checkbox onclick="event.preventDefault()"><input id=r type=radio checked><select id=s><option value=a>甲</option><option value=b>乙</option></select>
	<form id=f onsubmit="event.preventDefault();this.dataset.sent=q.value"><button id=submit>提交</button></form>
		<input id=ro readonly value=locked><input id=disabled disabled value=locked><div id=plain>普通块</div><div id=edit contenteditable>旧文本</div>
	<div style="position:relative;width:120px;height:40px"><button id=covered style="width:100%;height:100%" onclick="this.dataset.hit=1">遮挡按钮</button><div id=overlay style="position:absolute;inset:0;z-index:2"></div></div>
		<div style="position:relative;width:180px;height:30px"><input id=coveredinput value=safe style="width:100%;height:100%"><div style="position:absolute;inset:0;z-index:2"></div></div>
		<div style="position:relative;width:30px;height:30px"><input id=coveredcheck type=checkbox><div style="position:absolute;inset:0;z-index:2"></div></div>
	<iframe id=child src="/frame#inside"></iframe><div style="height:1600px"></div><div id=bottom>底部</div></body></html>`)
	}))
	defer server.Close()
	alloc, cancel := integrationAllocator(t, chrome)
	defer cancel()
	s, err := Open(alloc, server.URL, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auto := func(name, params string) json.RawMessage {
		t.Helper()
		r, e := s.Automate(context.Background(), name, json.RawMessage(params))
		if e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		return r
	}
	snap := auto("snapshot", `{"interactive":true}`)
	if !strings.Contains(string(snap), "点击我") || !strings.Contains(string(snap), "查询") {
		t.Fatalf("bad AX snapshot: %s", snap)
	}
	if strings.Contains(string(snap), "DO-NOT-LEAK-SECRET") {
		t.Fatalf("snapshot leaked password: %s", snap)
	}
	var found struct {
		Refs []string `json:"refs"`
	}
	if err := json.Unmarshal(auto("find", `{"role":"button","name":"点击我","exact":true}`), &found); err != nil || len(found.Refs) != 1 {
		t.Fatalf("find: %s %v", found.Refs, err)
	}
	auto("click", `{"ref":`+strconvQuote(found.Refs[0])+`}`)
	if got := string(auto("get", `{"kind":"text","selector":"#b"}`)); got != `"已点击"` {
		t.Fatalf("click result %s", got)
	}
	auto("fill", `{"selector":"#q","text":"中文输入"}`)
	if got := string(auto("get", `{"kind":"value","selector":"#q"}`)); got != `"中文输入"` {
		t.Fatalf("fill result %s", got)
	}
	if _, e := s.Automate(context.Background(), "fill", json.RawMessage(`{"selector":"#q"}`)); automationErrorCode(e) != "invalid_params" {
		t.Fatalf("fill without text must fail: %v", e)
	}
	if got := string(auto("get", `{"kind":"value","selector":"#q"}`)); got != `"中文输入"` {
		t.Fatalf("missing fill text cleared existing value: %s", got)
	}
	auto("press", `{"selector":"#q","key":"Control+A"}`)
	auto("press", `{"key":"Backspace"}`)
	auto("keyboard.insert", `{"text":"新的中文"}`)
	if got := string(auto("get", `{"kind":"value","selector":"#q"}`)); got != `"新的中文"` {
		t.Fatalf("native keyboard result %s", got)
	}
	auto("press", `{"selector":"#q","key":"Meta+A"}`)
	auto("press", `{"key":"Backspace"}`)
	auto("keyboard.insert", `{"text":"提交值"}`)
	auto("press", `{"selector":"#q","key":"Enter"}`)
	if got := string(auto("get", `{"kind":"attr","selector":"#f","name":"data-sent"}`)); got != `"提交值"` {
		t.Fatalf("Enter did not submit form: %s", got)
	}
	auto("fill", `{"selector":"#edit","text":"可编辑中文"}`)
	if got := string(auto("get", `{"kind":"text","selector":"#edit"}`)); got != `"可编辑中文"` {
		t.Fatalf("contenteditable fill: %s", got)
	}
	for _, selector := range []string{"#ro", "#disabled", "#plain", "#coveredinput"} {
		_, e := s.Automate(context.Background(), "fill", json.RawMessage(`{"selector":"`+selector+`","text":"bad"}`))
		var ae *AutomationError
		if !errors.As(e, &ae) || (ae.Code != "not_interactable" && ae.Code != "element_obscured") {
			t.Fatalf("fill %s should fail structurally: %v", selector, e)
		}
	}
	if got := string(auto("get", `{"kind":"value","selector":"#coveredinput"}`)); got != `"safe"` {
		t.Fatalf("covered input was modified: %s", got)
	}
	if _, e := s.Automate(context.Background(), "wait", json.RawMessage(`{"selector":"[","state":"hidden","timeoutMs":10}`)); e == nil {
		t.Fatal("invalid selector must not satisfy hidden wait")
	}
	_, err = s.Automate(context.Background(), "click", json.RawMessage(`{"selector":"#covered"}`))
	var blocked *AutomationError
	if !errors.As(err, &blocked) || blocked.Code != "element_obscured" {
		t.Fatalf("covered click: %v", err)
	}
	if got := string(auto("get", `{"kind":"attr","selector":"#covered","name":"data-hit"}`)); got != "null" {
		t.Fatalf("covered button was clicked: %s", got)
	}
	// Frame selection affects all DOM-backed operations and preserves URL hash.
	auto("frame", `{"selector":"#child"}`)
	fs := auto("snapshot", `{}`)
	if !strings.Contains(string(fs), "框内文本") || !strings.Contains(string(fs), `#inside`) {
		t.Fatalf("frame snapshot: %s", fs)
	}
	if got := string(auto("get", `{"kind":"text"}`)); !strings.Contains(got, "框内文本") {
		t.Fatalf("frame get text: %s", got)
	}
	if got := string(auto("eval", `{"script":"location.hash"}`)); got != `"#inside"` {
		t.Fatalf("frame eval: %s", got)
	}
	if _, e := s.Automate(context.Background(), "click", json.RawMessage(`{"selector":"#fb"}`)); automationErrorCode(e) != "unsupported_frame" {
		t.Fatalf("child-frame pointer must fail closed: %v", e)
	}
	auto("frame", `{"main":true}`)
	auto("check", `{"selector":"#c"}`)
	for _, tc := range []struct {
		name, selector, code string
	}{
		{"non-checkable", "#q", "not_interactable"},
		{"disabled", "#cd", "not_interactable"},
		{"covered", "#coveredcheck", "element_obscured"},
		{"prevented", "#cf", "action_failed"},
	} {
		if _, e := s.Automate(context.Background(), "check", json.RawMessage(`{"selector":"`+tc.selector+`"}`)); automationErrorCode(e) != tc.code {
			t.Fatalf("check %s: got %v, want %s", tc.name, e, tc.code)
		}
	}
	if _, e := s.Automate(context.Background(), "uncheck", json.RawMessage(`{"selector":"#r"}`)); automationErrorCode(e) != "not_interactable" {
		t.Fatalf("radio uncheck must be rejected: %v", e)
	}
	auto("select", `{"selector":"#s","value":"b"}`)
	auto("scrollintoview", `{"selector":"#bottom"}`)
	if got := string(auto("eval", `{"script":"Promise.resolve({ok:true,n:3})"}`)); got != `{"n":3,"ok":true}` && got != `{"ok":true,"n":3}` {
		t.Fatalf("async eval %s", got)
	}
	var limited struct {
		Truncated    bool  `json:"truncated"`
		EmittedNodes int   `json:"emittedNodes"`
		Nodes        []any `json:"nodes"`
	}
	if err := json.Unmarshal(auto("snapshot", `{"maxNodes":2}`), &limited); err != nil || !limited.Truncated || limited.EmittedNodes != 2 || len(limited.Nodes) != 2 {
		t.Fatalf("snapshot truncation metadata: %#v %v", limited, err)
	}
	ctx, cancelWait := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelWait()
	_, err = s.Automate(ctx, "wait", json.RawMessage(`{"selector":"#never","timeoutMs":5000}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel wait: %v", err)
	}
	auto("eval", `{"script":"document.body.textContent='x'.repeat(4194305)"}`)
	if _, e := s.Automate(context.Background(), "get", json.RawMessage(`{"kind":"text"}`)); automationErrorCode(e) != "result_too_large" {
		t.Fatalf("oversized text result: %v", e)
	}
	old := found.Refs[0]
	if err = s.Navigate(context.Background(), server.URL+"?new=1", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	_, err = s.Automate(context.Background(), "click", json.RawMessage(`{"ref":`+strconvQuote(old)+`}`))
	var ae *AutomationError
	if !errors.As(err, &ae) || ae.Code != "stale_ref" {
		t.Fatalf("old ref: %v", err)
	}
}

func TestAutomationOOPIFNativeCDPIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run automation integration")
	}
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>OOPIF child</title><body>
<input id=q aria-label="Remote query"><button id=b onclick="this.textContent='remote clicked';document.body.dataset.clicked='yes'">remote button</button><p id=t>remote text</p>`)
	}))
	defer child.Close()
	// httptest listens on 127.0.0.1. Use localhost for the iframe so Chromium's
	// default site isolation puts it in a different renderer process.
	childURL := strings.Replace(child.URL, "127.0.0.1", "localhost", 1) + "/"
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/land" {
			_, _ = fmt.Fprint(w, `<!doctype html><title>landed</title><body>same-site child landed</body>`)
			return
		}
		_, _ = fmt.Fprintf(w, `<!doctype html><title>parent</title><body><div id=parent-only>parent</div><iframe id=remote src=%q></iframe>`, childURL)
	}))
	defer parent.Close()

	alloc, cancel := chromedp.NewExecAllocator(context.Background(),
		chromedp.ExecPath(chrome), chromedp.Headless, chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck, chromedp.Flag("site-per-process", true),
	)
	defer cancel()
	s, err := Open(alloc, parent.URL, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deadline := time.Now().Add(5 * time.Second)
	var seenTargets string
	for {
		infos, e := chromedp.Targets(s.ctx)
		if e == nil {
			var seen []string
			for _, info := range infos {
				seen = append(seen, info.Type+":"+info.URL)
				if info.Type == "iframe" && info.URL == childURL {
					goto oopifReady
				}
			}
			seenTargets = strings.Join(seen, ", ")
		}
		if time.Now().After(deadline) {
			t.Skipf("this Chromium did not expose the cross-site iframe as an OOPIF target; targets: %s", seenTargets)
		}
		time.Sleep(25 * time.Millisecond)
	}

oopifReady:
	auto := func(name, params string) json.RawMessage {
		t.Helper()
		r, e := s.Automate(context.Background(), name, json.RawMessage(params))
		if e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		return r
	}
	auto("frame", `{"selector":"#remote"}`)
	if snap := string(auto("snapshot", `{"interactive":true}`)); !strings.Contains(snap, "Remote query") || !strings.Contains(snap, "remote button") || strings.Contains(snap, "parent-only") {
		t.Fatalf("OOPIF snapshot: %s", snap)
	}
	if got := string(auto("get", `{"kind":"text","selector":"#t"}`)); got != `"remote text"` {
		t.Fatalf("OOPIF get: %s", got)
	}
	if got := string(auto("eval", `{"script":"location.hostname"}`)); got != `"localhost"` {
		t.Fatalf("OOPIF eval ran in the wrong target: %s", got)
	}
	auto("fill", `{"selector":"#q","text":"跨进程"}`)
	if got := string(auto("get", `{"kind":"value","selector":"#q"}`)); got != `"跨进程"` {
		t.Fatalf("OOPIF fill: %s", got)
	}
	if _, e := s.Automate(context.Background(), "click", json.RawMessage(`{"selector":"#b"}`)); e != nil {
		if automationErrorCode(e) == "unsupported_frame" {
			t.Logf("OOPIF Input dispatch is unavailable and failed closed: %v", e)
		} else {
			t.Fatalf("OOPIF click: %v", e)
		}
	} else if got := string(auto("get", `{"kind":"attr","selector":"body","name":"data-clicked"}`)); got != `"yes"` {
		t.Fatalf("OOPIF click reported success without hitting child: %s", got)
	}
	// Navigating the child back into the parent's site normally destroys the
	// OOPIF target. The selected session must then fail closed, never execute a
	// pending selector against the top-level page.
	land := parent.URL + "/land"
	_, _ = s.Automate(context.Background(), "eval", json.RawMessage(`{"script":`+strconvQuote(`setTimeout(()=>location.href=`+strconvQuote(land)+`,0);true`)+`}`))
	deadline = time.Now().Add(5 * time.Second)
	for {
		attempt, stop := context.WithTimeout(context.Background(), time.Second)
		_, e := s.Automate(attempt, "get", json.RawMessage(`{"kind":"text","selector":"#parent-only"}`))
		stop()
		if automationErrorCode(e) == "unsupported_frame" {
			break
		}
		if e != nil && automationErrorCode(e) != "not_found" && !errors.Is(e, context.DeadlineExceeded) {
			t.Fatalf("destroyed OOPIF returned unsafe error: %v", e)
		}
		if time.Now().After(deadline) {
			t.Fatalf("destroyed OOPIF session did not become unsupported; last error: %v", e)
		}
		time.Sleep(25 * time.Millisecond)
	}
	auto("frame", `{"main":true}`)
	if got := string(auto("get", `{"kind":"text","selector":"#parent-only"}`)); got != `"parent"` {
		t.Fatalf("return to main frame: %s", got)
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
