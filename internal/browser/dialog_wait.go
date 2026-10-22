package browser

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// DialogStatus is a deliberately small projection of the current JavaScript
// dialog. Prompt input and the result of a closed dialog are never retained.
type DialogStatus struct {
	Open          bool   `json:"open"`
	Type          string `json:"type"`
	Message       string `json:"message"`
	DefaultPrompt string `json:"defaultPrompt"`
}

type dialogObserver struct {
	mu     sync.RWMutex
	status DialogStatus
}

var dialogObservers sync.Map // map[*Session]*dialogObserver

func startDialogObservation(s *Session, ctx context.Context) {
	o := &dialogObserver{}
	dialogObservers.Store(s, o)
	opening := chromedp.Events(ctx, page.JavascriptDialogOpening)
	closed := chromedp.Events(ctx, page.JavascriptDialogClosed)
	go func() {
		for e, err := range opening {
			if err != nil {
				return
			}
			o.mu.Lock()
			o.status = DialogStatus{Open: true, Type: string(e.Type), Message: e.Message, DefaultPrompt: e.DefaultPrompt}
			o.mu.Unlock()
		}
	}()
	go func() {
		for range closed {
			o.mu.Lock()
			o.status = DialogStatus{}
			o.mu.Unlock()
		}
	}()
}

func stopDialogObservation(s *Session) { dialogObservers.Delete(s) }

// JavaScriptDialogStatus reports the currently blocking dialog, if any.
func (s *Session) JavaScriptDialogStatus() (DialogStatus, error) {
	if s == nil || s.ctx == nil || s.ctx.Err() != nil {
		return DialogStatus{}, autoErr("session_closed", "browser session is unavailable")
	}
	v, ok := dialogObservers.Load(s)
	if !ok {
		return DialogStatus{}, autoErr("session_closed", "dialog observation is unavailable")
	}
	o := v.(*dialogObserver)
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.status, nil
}

// HandleJavaScriptDialog accepts or dismisses the currently open dialog. The
// prompt text is passed directly to CDP and is neither retained nor logged.
func (s *Session) HandleJavaScriptDialog(ctx context.Context, accept bool, promptText string) error {
	status, err := s.JavaScriptDialogStatus()
	if err != nil {
		return err
	}
	if !status.Open {
		return autoErr("dialog_not_open", "no JavaScript dialog is open")
	}
	op, cancel, err := s.operationContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	_, err = chromedp.Call(op, page.HandleJavaScriptDialog, page.HandleJavaScriptDialogParams{Accept: accept, PromptText: promptText})
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return autoErr("cdp_error", "could not handle JavaScript dialog")
	}
	return nil
}

func (s *Session) operationContext(caller context.Context) (context.Context, context.CancelFunc, error) {
	if s == nil || s.ctx == nil || s.ctx.Err() != nil {
		return nil, nil, autoErr("session_closed", "browser session is unavailable")
	}
	if caller == nil {
		caller = context.Background()
	}
	if err := caller.Err(); err != nil {
		return nil, nil, err
	}
	op, cancel := context.WithCancel(s.ctx)
	stop := context.AfterFunc(caller, cancel)
	return op, func() { stop(); cancel() }, nil
}

// WaitURL waits for the top-frame URL. A non-exact pattern containing '*' or
// '?' is a whole-URL glob (wildcards include '/'); other patterns are substrings.
func (s *Session) WaitURL(ctx context.Context, pattern string, exact bool) (string, error) {
	if pattern == "" {
		return "", autoErr("invalid_params", "pattern must not be empty")
	}
	return s.poll(ctx, func(c context.Context) (bool, string, error) {
		truth := true
		r, err := chromedp.Call(c, cdpruntime.Evaluate, cdpruntime.EvaluateParams{Expression: "location.href", ReturnByValue: &truth})
		if err != nil || r.Result == nil {
			return false, "", err
		}
		var u string
		if err := json.Unmarshal(r.Result.Value, &u); err != nil {
			return false, "", err
		}
		if exact {
			return u == pattern, u, nil
		}
		if strings.ContainsAny(pattern, "*?") {
			glob := regexp.QuoteMeta(pattern)
			glob = strings.ReplaceAll(glob, `\*`, `.*`)
			glob = strings.ReplaceAll(glob, `\?`, `.`)
			return regexp.MustCompile("^(?:" + glob + ")$").MatchString(u), u, nil
		}
		return strings.Contains(u, pattern), u, nil
	})
}

// WaitFunction repeatedly evaluates an expression and waits for a truthy
// result. Promise results are awaited by CDP before truthiness is tested.
func (s *Session) WaitFunction(ctx context.Context, script string) error {
	if strings.TrimSpace(script) == "" {
		return autoErr("invalid_params", "script must not be empty")
	}
	_, err := s.poll(ctx, func(c context.Context) (bool, string, error) {
		expression := "Promise.resolve((" + script + ")).then(v => typeof v === 'function' ? v() : v).then(Boolean)"
		truth := true
		r, err := chromedp.Call(c, cdpruntime.Evaluate, cdpruntime.EvaluateParams{Expression: expression, AwaitPromise: &truth, ReturnByValue: &truth})
		if err != nil {
			return false, "", err
		}
		if r.ExceptionDetails != nil {
			return false, "", autoErr("javascript_error", "wait function evaluation failed")
		}
		var value bool
		if r.Result != nil && len(r.Result.Value) != 0 {
			_ = json.Unmarshal(r.Result.Value, &value)
		}
		return value, "", nil
	})
	return err
}

// WaitLoadState waits for the current top document to reach a native document
// ready state. networkidle is intentionally not supported.
func (s *Session) WaitLoadState(ctx context.Context, state string) error {
	state = strings.ToLower(state)
	if state != "domcontentloaded" && state != "load" {
		return autoErr("invalid_params", "loadState must be domcontentloaded or load; networkidle is not supported")
	}
	_, err := s.poll(ctx, func(c context.Context) (bool, string, error) {
		truth := true
		r, err := chromedp.Call(c, cdpruntime.Evaluate, cdpruntime.EvaluateParams{Expression: "document.readyState", ReturnByValue: &truth})
		if err != nil {
			return false, "", err
		}
		var ready string
		if r.Result != nil {
			_ = json.Unmarshal(r.Result.Value, &ready)
		}
		if state == "load" {
			return ready == "complete", "", nil
		}
		return ready == "interactive" || ready == "complete", "", nil
	})
	return err
}

func (s *Session) poll(ctx context.Context, check func(context.Context) (bool, string, error)) (string, error) {
	op, cancel, err := s.operationContext(ctx)
	if err != nil {
		return "", err
	}
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		ok, value, checkErr := check(op)
		if checkErr != nil {
			if ctx != nil && ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", checkErr
		}
		if ok {
			return value, nil
		}
		select {
		case <-op.Done():
			if ctx != nil && ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", op.Err()
		case <-ticker.C:
		}
	}
}
