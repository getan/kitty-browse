package browser

// This file intentionally implements automation directly on the tab's CDP
// connection.  In particular it does not start (or communicate with) a helper
// process.  Keeping it here also makes references private to a Session.

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

type AutomationError struct{ Code, Message string }

func (e *AutomationError) Error() string     { return e.Code + ": " + e.Message }
func (e *AutomationError) ErrorCode() string { return e.Code }

func autoErr(code, format string, a ...any) error {
	return &AutomationError{Code: code, Message: fmt.Sprintf(format, a...)}
}

type automationRef struct {
	Backend int64
	Loader  string
	Frame   string
	Target  string
}
type automationState struct {
	gate      chan struct{}
	next      int
	refs      map[string]automationRef
	back      map[int64]string
	frame     string // CDP FrameId; empty means the top frame
	context   int64  // isolated-world execution context for frame
	frameName string
	loader    string
	base      context.Context
	childCtx  context.Context
	childStop context.CancelFunc
	target    string // OOPIF target id; empty for the page target
}

func (s *Session) automationState() *automationState {
	s.automationMu.Lock()
	defer s.automationMu.Unlock()
	if s.automation == nil {
		s.automation = &automationState{gate: make(chan struct{}, 1), next: 1, refs: map[string]automationRef{}, back: map[int64]string{}, base: s.ctx}
		s.automation.gate <- struct{}{}
	}
	return s.automation
}

// Automate executes one native CDP automation command.  The browser lifetime
// always remains bound to s.ctx; ctx only shortens this operation.
func (s *Session) Automate(ctx context.Context, name string, params json.RawMessage) (json.RawMessage, error) {
	if s == nil || s.ctx == nil {
		return nil, autoErr("session_closed", "browser session is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var p map[string]json.RawMessage
	if len(params) == 0 {
		params = []byte("{}")
	}
	if err := json.Unmarshal(params, &p); err != nil || p == nil {
		return nil, autoErr("invalid_params", "params must be a JSON object")
	}
	st := s.automationState()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case <-st.gate:
	}
	defer func() { st.gate <- struct{}{} }()
	op, cancel := context.WithCancel(s.ctx)
	defer cancel()
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-op.Done():
		}
	}()

	// Frame selection is always rooted at the page target. All other commands
	// stay on the selected OOPIF session. In particular, a dead child session
	// must never make an operation accidentally fall back to the page.
	actionName := strings.ToLower(name)
	runCtx := op
	selectedChild := st.childCtx != nil && actionName != "frame"
	if selectedChild {
		if st.childCtx.Err() != nil {
			return nil, autoErr("unsupported_frame", "selected out-of-process frame target was destroyed or detached")
		}
		infos, targetErr := chromedp.Targets(op)
		alive := false
		if targetErr == nil {
			for _, info := range infos {
				if info != nil && info.Type == "iframe" && string(info.TargetID) == st.target {
					alive = true
					break
				}
			}
		}
		if !alive {
			return nil, autoErr("unsupported_frame", "selected out-of-process frame target was destroyed or detached: %v", targetErr)
		}
		var stop context.CancelFunc
		runCtx, stop = context.WithCancel(st.childCtx)
		defer stop()
		go func() {
			select {
			case <-ctx.Done():
				stop()
			case <-runCtx.Done():
			}
		}()
	}
	var out any
	err := chromedp.Do(runCtx, chromedp.Func(func(c context.Context, t *chromedp.Target) error {
		var err error
		switch actionName {
		case "snapshot":
			out, err = st.snapshot(c, t, p)
		case "eval":
			out, err = st.eval(c, t, p)
		case "click", "dblclick", "hover", "focus":
			out, err = st.pointer(c, t, name, p)
		case "fill", "type":
			out, err = st.fill(c, t, name, p)
		case "press", "keydown", "keyup", "keyboard.insert":
			out, err = st.keyboard(c, t, name, p)
		case "check", "uncheck":
			out, err = st.check(c, t, name == "check", p)
		case "select":
			out, err = st.selectOption(c, t, p)
		case "scroll", "scrollintoview":
			out, err = st.scroll(c, t, name, p)
		case "wait":
			out, err = st.wait(c, t, p)
		case "get":
			out, err = st.get(c, t, p)
		case "is":
			out, err = st.is(c, t, p)
		case "find":
			out, err = st.find(c, t, p)
		case "frame":
			out, err = st.frameAction(c, t, p)
		case "upload":
			out, err = st.upload(c, t, p)
		default:
			err = autoErr("unsupported_action", "unsupported automation action %q", name)
		}
		return err
	}))
	if err != nil {
		if _, ok := err.(*AutomationError); ok {
			return nil, err
		}
		if selectedChild {
			if st.childCtx.Err() != nil {
				return nil, autoErr("unsupported_frame", "selected out-of-process frame session is no longer usable")
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, autoErr("unsupported_frame", "selected out-of-process frame session is no longer usable: %v", err)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, autoErr("cdp_error", "%v", err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return b, nil
}

type rawCommand[P, R any] struct{ method string }

func (x rawCommand[P, R]) Method() string { return x.method }

// cdp.Command is used directly because it keeps temporary remote objects and
// protocol error handling explicit.
func call[P, R any](ctx context.Context, t cdp.Session, method string, p P) (R, error) {
	return cdp.Call(ctx, t, cdp.Command[P, R]{Method: method}, p)
}

func documentInfo(ctx context.Context, t cdp.Session) (loader, url string, err error) {
	r, err := cdp.Call(ctx, t, page.GetFrameTree, cdp.Empty{})
	if err != nil {
		return "", "", err
	}
	if r.FrameTree == nil || r.FrameTree.Frame == nil {
		return "", "", autoErr("cdp_error", "main frame is unavailable")
	}
	return string(r.FrameTree.Frame.LoaderID), r.FrameTree.Frame.URL, nil
}

type frameDesc struct {
	ID, Loader, URL string
}

func frameTree(ctx context.Context, t cdp.Session) ([]frameDesc, error) {
	type frame struct {
		ID     string `json:"id"`
		Loader string `json:"loaderId"`
		URL    string `json:"url"`
	}
	type tree struct {
		Frame frame   `json:"frame"`
		Child []*tree `json:"childFrames"`
	}
	r, err := call[cdp.Empty, struct {
		FrameTree *tree `json:"frameTree"`
	}](ctx, t, "Page.getFrameTree", cdp.Empty{})
	if err != nil || r.FrameTree == nil {
		return nil, err
	}
	var out []frameDesc
	var walk func(*tree)
	walk = func(n *tree) {
		if n == nil {
			return
		}
		out = append(out, frameDesc{n.Frame.ID, n.Frame.Loader, n.Frame.URL})
		for _, c := range n.Child {
			walk(c)
		}
	}
	walk(r.FrameTree)
	return out, nil
}

// selectedInfo also makes sure an iframe world is still alive. Worlds are
// deliberately created through Page rather than by reaching through
// contentWindow, which works for cross-origin frames in the same renderer.
func (st *automationState) selectedInfo(ctx context.Context, t cdp.Session) (frameDesc, error) {
	fs, err := frameTree(ctx, t)
	if err != nil {
		return frameDesc{}, err
	}
	for i, f := range fs {
		if (st.frame == "" && i == 0) || f.ID == st.frame {
			// An attached OOPIF target exposes the selected frame as its root
			// document. Its default execution context is already isolated from the
			// embedding page; Page.createIsolatedWorld(frameId) on the parent is the
			// mechanism used only for same-process child frames.
			if st.target != "" {
				if i != 0 || f.ID != st.frame {
					return frameDesc{}, autoErr("unsupported_frame", "out-of-process frame target no longer owns the selected frame")
				}
				return f, nil
			}
			if st.frame != "" && st.context == 0 {
				r, e := call[map[string]any, struct {
					ExecutionContextID int64 `json:"executionContextId"`
				}](ctx, t, "Page.createIsolatedWorld", map[string]any{"frameId": f.ID, "worldName": "kitty-browse", "grantUniveralAccess": false})
				if e != nil || r.ExecutionContextID == 0 {
					return frameDesc{}, autoErr("unsupported_frame", "frame is out-of-process or has no usable execution context")
				}
				st.context = r.ExecutionContextID
			}
			return f, nil
		}
	}
	return frameDesc{}, autoErr("unsupported_frame", "selected frame is no longer attached")
}

func (st *automationState) targetKey() string { return st.target }

type axValue struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}
type axNode struct {
	NodeID            string  `json:"nodeId"`
	Ignored           bool    `json:"ignored"`
	Role, Name, Value axValue `json:",omitempty"`
	BackendDOMNodeID  int64   `json:"backendDOMNodeId,omitempty"`
	ParentID          string  `json:"parentId,omitempty"`
	Properties        []struct {
		Name  string  `json:"name"`
		Value axValue `json:"value"`
	} `json:"properties,omitempty"`
}
type axTree struct {
	Nodes []axNode `json:"nodes"`
}

func decodeAXValue(v accessibility.Value) any {
	if len(v.Value) == 0 {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(v.Value), &out); err != nil {
		return nil
	}
	return out
}

type snapshotNode struct {
	Ref      string `json:"ref,omitempty"`
	Role     string `json:"role"`
	Name     string `json:"name,omitempty"`
	Value    any    `json:"value,omitempty"`
	Depth    int    `json:"depth"`
	Disabled bool   `json:"disabled,omitempty"`
	Checked  any    `json:"checked,omitempty"`
}

func strval(v axValue) string {
	if v.Value == nil {
		return ""
	}
	return fmt.Sprint(v.Value)
}
func boolParam(p map[string]json.RawMessage, k string, d bool) bool {
	if b := p[k]; len(b) > 0 {
		_ = json.Unmarshal(b, &d)
	}
	return d
}
func intParam(p map[string]json.RawMessage, k string, d int) int {
	if b := p[k]; len(b) > 0 {
		_ = json.Unmarshal(b, &d)
	}
	return d
}
func stringParam(p map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(p[k], &s)
	return s
}
func requiredStringParam(p map[string]json.RawMessage, keys ...string) (string, error) {
	for _, k := range keys {
		raw, ok := p[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", autoErr("invalid_params", "%s must be a string", k)
		}
		return s, nil
	}
	return "", autoErr("invalid_params", "%s is required", strings.Join(keys, " or "))
}
func interactiveRole(r string) bool {
	switch strings.ToLower(r) {
	case "button", "link", "textbox", "checkbox", "radio", "combobox", "listbox", "option", "menuitem", "menuitemcheckbox", "menuitemradio", "treeitem", "iframe", "tab", "switch", "slider", "spinbutton", "searchbox":
		return true
	}
	return false
}

func textInputRole(role string) bool {
	switch strings.ToLower(role) {
	case "textbox", "searchbox", "combobox", "spinbutton":
		return true
	}
	return strings.Contains(strings.ToLower(role), "textbox")
}

func snapshotValueAllowed(role string, value any, domTypeKnown, domPassword bool) bool {
	if value == nil {
		return false
	}
	lower := strings.ToLower(role)
	if lower == "password" || lower == "passwordfield" || domPassword {
		return false
	}
	return !textInputRole(lower) || domTypeKnown
}

func (st *automationState) snapshot(ctx context.Context, t cdp.Session, p map[string]json.RawMessage) (any, error) {
	info, err := st.selectedInfo(ctx, t)
	if err != nil {
		return nil, err
	}
	loader, url := info.Loader, info.URL
	// Backend node ids are only stable within one document. Drop the old maps
	// (unknown old names still produce stale_ref) and never recycle ref names.
	if st.loader != "" && st.loader != loader {
		st.back = map[int64]string{}
		st.refs = map[string]automationRef{}
		if st.frame != "" {
			st.context = 0
			if _, e := st.selectedInfo(ctx, t); e != nil {
				return nil, e
			}
		}
	}
	st.loader = loader
	r, err := call[map[string]any, struct {
		Nodes []*accessibility.Node `json:"nodes"`
	}](ctx, t, "Accessibility.getFullAXTree", map[string]any{"frameId": info.ID})
	if err != nil {
		return nil, err
	}
	depths := map[string]int{}
	interactive := boolParam(p, "interactive", false)
	compact := boolParam(p, "compact", false)
	includeText := boolParam(p, "includeText", !interactive && !compact)
	maxDepth := intParam(p, "depth", -1)
	max := intParam(p, "maxNodes", 5000)
	if max < 1 {
		max = 1
	}
	if max > 10000 {
		max = 10000
	}
	// Bound retained backend-node mappings independently from maxNodes. Ref
	// names remain monotonic, so clearing cannot make a stale ref point at a
	// different element.
	if len(st.refs)+max > 20000 || len(st.back)+max > 20000 {
		st.refs = map[string]automationRef{}
		st.back = map[int64]string{}
	}
	nodes := make([]snapshotNode, 0)
	truncated := false
	for _, raw := range r.Nodes {
		if raw == nil {
			continue
		}
		n := axNode{NodeID: string(raw.NodeID), Ignored: raw.Ignored, BackendDOMNodeID: int64(raw.BackendDOMNodeID), ParentID: string(raw.ParentID)}
		if raw.Role != nil {
			n.Role.Value = decodeAXValue(*raw.Role)
		}
		if raw.Name != nil {
			n.Name.Value = decodeAXValue(*raw.Name)
		}
		if raw.Value != nil {
			n.Value.Value = decodeAXValue(*raw.Value)
		}
		for _, property := range raw.Properties {
			if property == nil || property.Value == nil {
				continue
			}
			n.Properties = append(n.Properties, struct {
				Name  string  `json:"name"`
				Value axValue `json:"value"`
			}{Name: string(property.Name), Value: axValue{Value: decodeAXValue(*property.Value)}})
		}
		d := 0
		if n.ParentID != "" {
			d = depths[n.ParentID] + 1
		}
		depths[n.NodeID] = d
		role := strval(n.Role)
		if n.Ignored || n.BackendDOMNodeID == 0 || (interactive && !interactiveRole(role)) || (compact && (role == "generic" || role == "none" || role == "StaticText")) || (maxDepth >= 0 && d > maxDepth) {
			continue
		}
		if len(nodes) >= max {
			truncated = true
			continue
		}
		ref := st.back[n.BackendDOMNodeID]
		if ref == "" {
			ref = "e" + strconv.Itoa(st.next)
			st.next++
			st.back[n.BackendDOMNodeID] = ref
		}
		st.refs[ref] = automationRef{Backend: n.BackendDOMNodeID, Loader: loader, Frame: st.frame, Target: st.targetKey()}
		sn := snapshotNode{Ref: "@" + ref, Role: role, Name: strval(n.Name), Depth: d}
		domTypeKnown := !textInputRole(role)
		domPassword := false
		// Chromium normally masks password AX values. Verify the DOM input type
		// as well so a protocol/version change can never expose the real value.
		if n.Value.Value != nil && textInputRole(role) {
			resolved, resolveErr := call[map[string]any, struct {
				Object remoteObject `json:"object"`
			}](ctx, t, "DOM.resolveNode", map[string]any{"backendNodeId": n.BackendDOMNodeID})
			if resolveErr == nil && resolved.Object.ObjectID != "" {
				q, qerr := callFunction(ctx, t, resolved.Object.ObjectID, `function(){return {known:this instanceof HTMLInputElement||this instanceof HTMLTextAreaElement,password:this instanceof HTMLInputElement&&this.type==='password'}}`, nil, true)
				if qerr == nil {
					var check struct {
						Known      bool `json:"known"`
						IsPassword bool `json:"password"`
					}
					if json.Unmarshal(q.Value, &check) == nil && check.Known {
						domTypeKnown = true
						if check.IsPassword {
							domPassword = true
						}
					}
				}
				release(ctx, t, resolved.Object.ObjectID)
			}
		}
		if snapshotValueAllowed(role, n.Value.Value, domTypeKnown, domPassword) {
			sn.Value = n.Value.Value
		}
		for _, q := range n.Properties {
			if q.Name == "disabled" {
				sn.Disabled = q.Value.Value == true
			}
			if q.Name == "checked" {
				sn.Checked = q.Value.Value
			}
		}
		nodes = append(nodes, sn)
	}
	title, err := st.evaluateValue(ctx, t, "document.title || ''", false)
	if err != nil {
		return nil, err
	}
	if href, e := st.evaluateValue(ctx, t, "location.href", false); e == nil {
		if s, ok := href.(string); ok {
			url = s
		}
	}
	result := map[string]any{"snapshotId": loader, "url": url, "title": title, "nodes": nodes, "truncated": truncated, "emittedNodes": len(nodes)}
	if includeText {
		text, err := st.evaluateValue(ctx, t, "document.body ? document.body.innerText : ''", false)
		if err != nil {
			return nil, err
		}
		result["text"] = text
	}
	return result, nil
}

type remoteObject struct {
	Type, Subtype string
	Value         json.RawMessage `json:"value,omitempty"`
	ObjectID      string          `json:"objectId,omitempty"`
	Description   string          `json:"description,omitempty"`
}
type runtimeResult struct {
	Result           remoteObject    `json:"result"`
	ExceptionDetails json.RawMessage `json:"exceptionDetails,omitempty"`
}

func evaluateValueIn(ctx context.Context, t cdp.Session, expr string, await bool, executionContext int64) (any, error) {
	p := map[string]any{"expression": expr, "awaitPromise": await, "returnByValue": true, "userGesture": true}
	if executionContext != 0 {
		p["contextId"] = executionContext
	}
	r, err := call[map[string]any, runtimeResult](ctx, t, "Runtime.evaluate", p)
	if err != nil {
		return nil, err
	}
	if len(r.ExceptionDetails) > 0 {
		return nil, autoErr("javascript_error", "JavaScript evaluation failed: %s", string(r.ExceptionDetails))
	}
	if len(r.Result.Value) == 0 {
		return nil, nil
	}
	var v any
	if err = json.Unmarshal(r.Result.Value, &v); err != nil {
		return nil, err
	}
	return v, nil
}
func evaluateValue(ctx context.Context, t cdp.Session, expr string, await bool) (any, error) {
	return evaluateValueIn(ctx, t, expr, await, 0)
}
func (st *automationState) evaluateValue(ctx context.Context, t cdp.Session, expr string, await bool) (any, error) {
	if _, err := st.selectedInfo(ctx, t); err != nil {
		return nil, err
	}
	return evaluateValueIn(ctx, t, expr, await, st.context)
}
func (st *automationState) eval(ctx context.Context, t cdp.Session, p map[string]json.RawMessage) (any, error) {
	s := stringParam(p, "script")
	if s == "" {
		return nil, autoErr("invalid_params", "script is required")
	}
	return st.evaluateValue(ctx, t, s, true)
}

type callArg struct {
	Value    any    `json:"value,omitempty"`
	ObjectID string `json:"objectId,omitempty"`
}

func callFunction(ctx context.Context, t cdp.Session, obj, fn string, args []callArg, byValue bool) (remoteObject, error) {
	p := map[string]any{"objectId": obj, "functionDeclaration": fn, "arguments": args, "awaitPromise": true, "returnByValue": byValue, "userGesture": true}
	r, err := call[map[string]any, runtimeResult](ctx, t, "Runtime.callFunctionOn", p)
	if err != nil {
		return remoteObject{}, err
	}
	if len(r.ExceptionDetails) > 0 {
		return remoteObject{}, autoErr("javascript_error", "page function failed")
	}
	return r.Result, nil
}
func release(ctx context.Context, t cdp.Session, id string) {
	if id != "" {
		_, _ = call[map[string]any, struct{}](ctx, t, "Runtime.releaseObject", map[string]any{"objectId": id})
	}
}
func (st *automationState) documentObject(ctx context.Context, t cdp.Session) (string, error) {
	if _, err := st.selectedInfo(ctx, t); err != nil {
		return "", err
	}
	p := map[string]any{"expression": "document", "returnByValue": false}
	if st.context != 0 {
		p["contextId"] = st.context
	}
	r, err := call[map[string]any, runtimeResult](ctx, t, "Runtime.evaluate", p)
	if err != nil {
		return "", err
	}
	if r.Result.ObjectID == "" {
		return "", autoErr("not_found", "document unavailable")
	}
	return r.Result.ObjectID, nil
}
func (st *automationState) resolve(ctx context.Context, t cdp.Session, p map[string]json.RawMessage) (string, error) {
	ref := strings.TrimPrefix(stringParam(p, "ref"), "@")
	sel := stringParam(p, "selector")
	if ref != "" {
		e, ok := st.refs[ref]
		if !ok {
			return "", autoErr("stale_ref", "unknown element reference @%s", ref)
		}
		info, err := st.selectedInfo(ctx, t)
		if err != nil {
			return "", err
		}
		if info.Loader != e.Loader || e.Frame != st.frame || e.Target != st.targetKey() {
			return "", autoErr("stale_ref", "reference belongs to a previous document")
		}
		resolveParams := map[string]any{"backendNodeId": e.Backend}
		if st.context != 0 {
			resolveParams["executionContextId"] = st.context
		}
		r, err := call[map[string]any, struct {
			Object remoteObject `json:"object"`
		}](ctx, t, "DOM.resolveNode", resolveParams)
		if err != nil || r.Object.ObjectID == "" {
			return "", autoErr("stale_ref", "referenced element no longer exists")
		}
		connected, connErr := callFunction(ctx, t, r.Object.ObjectID, `function(){return this.isConnected!==false}`, nil, true)
		var alive bool
		if connErr != nil || json.Unmarshal(connected.Value, &alive) != nil || !alive {
			release(ctx, t, r.Object.ObjectID)
			return "", autoErr("stale_ref", "referenced element is detached")
		}
		return r.Object.ObjectID, nil
	}
	if sel == "" {
		// role/name/text locators are resolved through the same AX tree used by
		// snapshot. This preserves accessible-name semantics instead of trying
		// to approximate them with DOM attributes.
		if len(p["role"]) > 0 || len(p["name"]) > 0 || len(p["text"]) > 0 {
			found, err := st.find(ctx, t, p)
			if err != nil {
				return "", err
			}
			refs := found.(map[string]any)["refs"].([]string)
			if len(refs) == 0 {
				return "", autoErr("not_found", "accessible locator matched no elements")
			}
			if len(refs) > 1 {
				return "", autoErr("ambiguous_locator", "accessible locator matched %d elements", len(refs))
			}
			return st.resolve(ctx, t, map[string]json.RawMessage{"ref": json.RawMessage(strconv.Quote(refs[0]))})
		}
		return "", autoErr("invalid_params", "selector, ref, role, name, or text is required")
	}
	doc, err := st.documentObject(ctx, t)
	if err != nil {
		return "", err
	}
	defer release(ctx, t, doc)
	fn := `function(s){let a=this.querySelectorAll(s);return a.length===1?a[0]:{__locatorCount:a.length}}`
	r, err := callFunction(ctx, t, doc, fn, []callArg{{Value: sel}}, false)
	if err != nil {
		return "", err
	}
	if r.ObjectID == "" {
		return "", autoErr("not_found", "selector %q did not resolve uniquely", sel)
	}
	// Marker objects have a JSON description; inspect their count safely.
	q, err := callFunction(ctx, t, r.ObjectID, `function(){return this.__locatorCount===undefined?-1:this.__locatorCount}`, nil, true)
	if err != nil {
		release(ctx, t, r.ObjectID)
		return "", err
	}
	var n int = -1
	_ = json.Unmarshal(q.Value, &n)
	if n >= 0 {
		release(ctx, t, r.ObjectID)
		if n == 0 {
			return "", autoErr("not_found", "selector %q matched no elements", sel)
		}
		return "", autoErr("ambiguous_locator", "selector %q matched %d elements", sel, n)
	}
	return r.ObjectID, nil
}

func (st *automationState) geometry(ctx context.Context, t cdp.Session, obj string, scroll, actionable bool) (x, y float64, err error) {
	fn := `function(sc,act){
if(!this.isConnected)return {error:'stale'};if(sc)this.scrollIntoView({block:'center',inline:'center'});
let r=this.getBoundingClientRect(),s=getComputedStyle(this);if(r.width<=0||r.height<=0||s.visibility==='hidden'||s.display==='none'||s.pointerEvents==='none')return {error:'hidden'};
let x=r.left+r.width/2,y=r.top+r.height/2;if(x<0||y<0||x>=innerWidth||y>=innerHeight)return {error:'outside_viewport'};
if(act&&(this.matches(':disabled,[aria-disabled="true"]')||this.closest('[inert]')))return {error:'disabled'};
let hit=document.elementFromPoint(x,y);while(hit&&hit.shadowRoot){let n=hit.shadowRoot.elementFromPoint(x,y);if(!n||n===hit)break;hit=n}
if(!hit||!(hit===this||this.contains(hit)))return {error:'obscured'};return {x:x,y:y}}`
	r, e := callFunction(ctx, t, obj, fn, []callArg{{Value: scroll}, {Value: actionable}}, true)
	if e != nil {
		return 0, 0, e
	}
	var v struct {
		X, Y  float64
		Error string
	}
	if json.Unmarshal(r.Value, &v) != nil || v.Error != "" {
		code := map[string]string{"stale": "stale_ref", "obscured": "element_obscured", "disabled": "not_interactable", "outside_viewport": "not_interactable", "hidden": "not_interactable"}[v.Error]
		if code == "" {
			code = "not_interactable"
		}
		return 0, 0, autoErr(code, "element is %s", v.Error)
	}
	// DOM.getBoxModel uses top-level viewport coordinates, including for nodes
	// in same-process child frames. Prefer it when a frame is selected.
	if st.frame != "" {
		bm, e := call[map[string]any, struct {
			Model struct {
				Content []float64 `json:"content"`
			} `json:"model"`
		}](ctx, t, "DOM.getBoxModel", map[string]any{"objectId": obj})
		if e == nil && len(bm.Model.Content) >= 8 {
			var sx, sy float64
			for i := 0; i < 8; i += 2 {
				sx += bm.Model.Content[i]
				sy += bm.Model.Content[i+1]
			}
			return sx / 4, sy / 4, nil
		}
	}
	return v.X, v.Y, nil
}

func (st *automationState) verifyEpoch(ctx context.Context, t cdp.Session, expected frameDesc) error {
	current, err := st.selectedInfo(ctx, t)
	if err != nil {
		return autoErr("navigation_interrupted", "document changed before input dispatch")
	}
	if current.ID != expected.ID || current.Loader != expected.Loader {
		return autoErr("navigation_interrupted", "document changed before input dispatch")
	}
	return nil
}

func (st *automationState) dispatchClick(ctx context.Context, t cdp.Session, expected frameDesc, x, y float64, count int) error {
	for i := 1; i <= count; i++ {
		for _, kind := range []string{"mousePressed", "mouseReleased"} {
			if err := st.verifyEpoch(ctx, t, expected); err != nil {
				return err
			}
			if _, err := call[map[string]any, struct{}](ctx, t, "Input.dispatchMouseEvent", map[string]any{"type": kind, "x": x, "y": y, "button": "left", "clickCount": i}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (st *automationState) pointer(ctx context.Context, t cdp.Session, name string, p map[string]json.RawMessage) (any, error) {
	obj, err := st.resolve(ctx, t, p)
	if err != nil {
		return nil, err
	}
	defer release(ctx, t, obj)
	if name == "focus" {
		if _, _, e := st.geometry(ctx, t, obj, true, true); e != nil {
			return nil, e
		}
		_, err = callFunction(ctx, t, obj, `function(){this.focus()}`, nil, true)
		return map[string]any{"ok": err == nil}, err
	}
	// DOM.getBoxModel can translate child-frame coordinates, but it does not
	// prove that every embedding iframe is itself unobscured. Sending a pointer
	// event in that case would be unsafe.
	if st.frame != "" && st.target == "" {
		return nil, autoErr("unsupported_frame", "pointer actions in child frames require parent-frame hit testing")
	}
	if st.target != "" && name == "hover" {
		return nil, autoErr("unsupported_frame", "cannot verify pointer hover delivery in an out-of-process frame")
	}
	epoch, err := st.selectedInfo(ctx, t)
	if err != nil {
		return nil, err
	}
	x, y, err := st.geometry(ctx, t, obj, true, name != "hover")
	if err != nil {
		return nil, err
	}
	typ := "mouseMoved"
	if name == "hover" {
		if err = st.verifyEpoch(ctx, t, epoch); err != nil {
			return nil, err
		}
		_, err = call[map[string]any, struct{}](ctx, t, "Input.dispatchMouseEvent", map[string]any{"type": typ, "x": x, "y": y})
		return map[string]any{"ok": err == nil}, err
	}
	count := 1
	if name == "dblclick" {
		count = 2
	}
	probe := ""
	if st.target != "" {
		probe = fmt.Sprintf("__kittyBrowseClickProbe_%d", st.next)
		st.next++
		prepared, e := callFunction(ctx, t, obj, `function(k){let h=()=>this[k].seen++;this[k]={seen:0,h:h};this.addEventListener('click',h,{capture:true})}`, []callArg{{Value: probe}}, true)
		if e != nil {
			return nil, autoErr("unsupported_frame", "could not install OOPIF input-delivery probe: %v", e)
		}
		release(ctx, t, prepared.ObjectID)
	}
	if err = st.dispatchClick(ctx, t, epoch, x, y, count); err != nil {
		return nil, err
	}
	if probe != "" {
		seen, e := callFunction(ctx, t, obj, `function(k){let p=this[k],n=p?p.seen:0;if(p){this.removeEventListener('click',p.h,{capture:true});delete this[k]}return n}`, []callArg{{Value: probe}}, true)
		if e != nil {
			return nil, autoErr("unsupported_frame", "could not verify click delivery in the out-of-process frame: %v", e)
		}
		var n int
		if json.Unmarshal(seen.Value, &n) != nil || n < 1 {
			return nil, autoErr("unsupported_frame", "Input.dispatchMouseEvent on the child session did not hit the requested element")
		}
	}
	return map[string]any{"ok": true}, nil
}

func (st *automationState) fill(ctx context.Context, t cdp.Session, name string, p map[string]json.RawMessage) (any, error) {
	text, err := requiredStringParam(p, "text", "value")
	if err != nil {
		return nil, err
	}
	obj, err := st.resolve(ctx, t, p)
	if err != nil {
		return nil, err
	}
	defer release(ctx, t, obj)
	check, err := callFunction(ctx, t, obj, `function(){
if(!this.isConnected)return 'stale';if(this.matches(':disabled,[aria-disabled="true"]')||this.closest('[inert]'))return 'disabled';
if(this instanceof HTMLInputElement){if(this.readOnly)return 'readonly';let bad=['button','checkbox','color','file','hidden','image','radio','range','reset','submit'];return bad.includes(this.type)?'not_editable':'input'}
if(this instanceof HTMLTextAreaElement){return this.readOnly?'readonly':'input'}if(this.isContentEditable)return 'contenteditable';return 'not_editable'}`, nil, true)
	if err != nil {
		return nil, err
	}
	var edit string
	_ = json.Unmarshal(check.Value, &edit)
	if edit == "stale" {
		return nil, autoErr("stale_ref", "element is detached")
	}
	if edit != "input" && edit != "contenteditable" {
		return nil, autoErr("not_interactable", "element is %s", edit)
	}
	epoch, err := st.selectedInfo(ctx, t)
	if err != nil {
		return nil, err
	}
	if _, _, err = st.geometry(ctx, t, obj, true, true); err != nil {
		return nil, err
	}
	focus := `function(selectAll){
this.focus();let a=document.activeElement;while(a&&a.shadowRoot&&a.shadowRoot.activeElement)a=a.shadowRoot.activeElement;
if(!(a===this||this.contains(a)))return false;
if(selectAll){let r=document.createRange();r.selectNodeContents(this);let s=getSelection();s.removeAllRanges();s.addRange(r)}return true}`
	focused, err := callFunction(ctx, t, obj, focus, []callArg{{Value: edit == "contenteditable" && name == "fill"}}, true)
	if err != nil {
		return nil, err
	}
	var focusOK bool
	if json.Unmarshal(focused.Value, &focusOK) != nil || !focusOK {
		return nil, autoErr("not_interactable", "element did not become the active target")
	}
	if name == "type" {
		if err = st.verifyEpoch(ctx, t, epoch); err == nil {
			_, err = call[map[string]any, struct{}](ctx, t, "Input.insertText", map[string]any{"text": text})
		}
	} else if edit == "contenteditable" {
		if err = st.verifyEpoch(ctx, t, epoch); err == nil {
			_, err = call[map[string]any, struct{}](ctx, t, "Input.insertText", map[string]any{"text": text})
		}
		if err == nil {
			_, err = callFunction(ctx, t, obj, `function(){this.dispatchEvent(new Event('change',{bubbles:true}))}`, nil, true)
		}
	} else {
		fn := `function(v){let p=this instanceof HTMLTextAreaElement?HTMLTextAreaElement.prototype:HTMLInputElement.prototype,d=Object.getOwnPropertyDescriptor(p,'value');if(d&&d.set)d.set.call(this,v);else this.value=v;this.dispatchEvent(new Event('input',{bubbles:true}));this.dispatchEvent(new Event('change',{bubbles:true}))}`
		_, err = callFunction(ctx, t, obj, fn, []callArg{{Value: text}}, true)
	}
	return map[string]any{"ok": err == nil}, err
}

func keyInfo(k string) (key, code string, keyCode int) {
	switch strings.ToLower(k) {
	case "enter", "return":
		return "Enter", "Enter", 13
	case "tab":
		return "Tab", "Tab", 9
	case "escape", "esc":
		return "Escape", "Escape", 27
	case "backspace":
		return "Backspace", "Backspace", 8
	case "delete":
		return "Delete", "Delete", 46
	case "arrowup":
		return "ArrowUp", "ArrowUp", 38
	case "arrowdown":
		return "ArrowDown", "ArrowDown", 40
	case "arrowleft":
		return "ArrowLeft", "ArrowLeft", 37
	case "arrowright":
		return "ArrowRight", "ArrowRight", 39
	case "home":
		return "Home", "Home", 36
	case "end":
		return "End", "End", 35
	case "pageup":
		return "PageUp", "PageUp", 33
	case "pagedown":
		return "PageDown", "PageDown", 34
	case "space", "spacebar":
		return " ", "Space", 32
	case "control", "ctrl":
		return "Control", "ControlLeft", 17
	case "alt", "option":
		return "Alt", "AltLeft", 18
	case "shift":
		return "Shift", "ShiftLeft", 16
	case "meta", "cmd", "command":
		return "Meta", "MetaLeft", 91
	}
	r := []rune(k)
	if len(r) == 1 {
		if r[0] < 0x20 || r[0] > 0x7e {
			return "", "", 0
		}
		if r[0] >= 'a' && r[0] <= 'z' {
			return k, "Key" + strings.ToUpper(k), int(r[0] - 'a' + 'A')
		}
		if r[0] >= 'A' && r[0] <= 'Z' {
			return k, "Key" + strings.ToUpper(k), int(r[0])
		}
		if r[0] >= '0' && r[0] <= '9' {
			return k, "Digit" + k, int(r[0])
		}
		return k, "", int(r[0])
	}
	return "", "", 0
}

type parsedKey struct {
	key, code     string
	vk, modifiers int
	text          string
	mods          []struct {
		key, code string
		vk, bit   int
	}
}

func parseKey(input string) (parsedKey, error) {
	var out parsedKey
	parts := strings.Split(input, "+")
	if input == "+" {
		parts = []string{"+"}
	}
	if len(parts) == 0 {
		return out, autoErr("invalid_arguments", "key is required")
	}
	for _, raw := range parts[:len(parts)-1] {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "control", "ctrl":
			out.mods = append(out.mods, struct {
				key, code string
				vk, bit   int
			}{"Control", "ControlLeft", 17, 2})
		case "alt", "option":
			out.mods = append(out.mods, struct {
				key, code string
				vk, bit   int
			}{"Alt", "AltLeft", 18, 1})
		case "meta", "cmd", "command":
			out.mods = append(out.mods, struct {
				key, code string
				vk, bit   int
			}{"Meta", "MetaLeft", 91, 4})
		case "shift":
			out.mods = append(out.mods, struct {
				key, code string
				vk, bit   int
			}{"Shift", "ShiftLeft", 16, 8})
		default:
			return out, autoErr("invalid_arguments", "unsupported modifier %q", raw)
		}
	}
	for _, m := range out.mods {
		out.modifiers |= m.bit
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	out.key, out.code, out.vk = keyInfo(last)
	if out.key == "" || out.vk == 0 {
		return out, autoErr("invalid_arguments", "unsupported key %q", last)
	}
	if len([]rune(out.key)) == 1 && out.modifiers&(1|2|4) == 0 {
		out.text = out.key
		if out.modifiers&8 != 0 {
			out.text = strings.ToUpper(out.text)
			out.key = out.text
		}
	}
	if out.key == "Enter" && out.modifiers == 0 {
		out.text = "\r"
	}
	return out, nil
}

func nativeKeyCode(code string, windows int) int {
	if runtime.GOOS != "darwin" {
		return windows
	}
	if strings.HasPrefix(code, "Key") && len(code) == 4 {
		if n, ok := map[byte]int{'A': 0, 'B': 11, 'C': 8, 'D': 2, 'E': 14, 'F': 3, 'G': 5, 'H': 4, 'I': 34, 'J': 38, 'K': 40, 'L': 37, 'M': 46, 'N': 45, 'O': 31, 'P': 35, 'Q': 12, 'R': 15, 'S': 1, 'T': 17, 'U': 32, 'V': 9, 'W': 13, 'X': 7, 'Y': 16, 'Z': 6}[code[3]]; ok {
			return n
		}
	}
	if strings.HasPrefix(code, "Digit") && len(code) == 6 {
		return map[byte]int{'0': 29, '1': 18, '2': 19, '3': 20, '4': 21, '5': 23, '6': 22, '7': 26, '8': 28, '9': 25}[code[5]]
	}
	if n, ok := map[string]int{"Enter": 36, "Tab": 48, "Space": 49, "Backspace": 51, "Escape": 53, "MetaLeft": 55, "ShiftLeft": 56, "AltLeft": 58, "ControlLeft": 59, "Delete": 117, "Home": 115, "End": 119, "PageUp": 116, "PageDown": 121, "ArrowLeft": 123, "ArrowRight": 124, "ArrowDown": 125, "ArrowUp": 126}[code]; ok {
		return n
	}
	return windows
}
func (st *automationState) keyboard(ctx context.Context, t cdp.Session, name string, p map[string]json.RawMessage) (any, error) {
	if len(p["selector"]) > 0 || len(p["ref"]) > 0 || len(p["role"]) > 0 || len(p["name"]) > 0 {
		obj, err := st.resolve(ctx, t, p)
		if err != nil {
			return nil, err
		}
		if _, err = callFunction(ctx, t, obj, `function(){this.focus()}`, nil, true); err != nil {
			release(ctx, t, obj)
			return nil, err
		}
		release(ctx, t, obj)
	}
	text := stringParam(p, "text")
	if text == "" {
		text = stringParam(p, "key")
	}
	if name == "keyboard.insert" {
		_, err := call[map[string]any, struct{}](ctx, t, "Input.insertText", map[string]any{"text": text})
		return map[string]any{"ok": err == nil}, err
	}
	k, err := parseKey(text)
	if err != nil {
		return nil, err
	}
	dispatch := func(typ, key, code string, vk, mods int, text string) error {
		m := map[string]any{"type": typ, "key": key, "code": code, "modifiers": mods, "windowsVirtualKeyCode": vk, "nativeVirtualKeyCode": nativeKeyCode(code, vk)}
		if typ == "keyDown" && text != "" {
			m["text"] = text
			m["unmodifiedText"] = text
		}
		// Chromium on macOS does not derive editing accelerators from synthetic
		// native key codes. CDP's native editing command keeps Ctrl/Cmd+A
		// deterministic across host platforms without mutating the DOM directly.
		if (typ == "rawKeyDown" || typ == "keyDown") && strings.EqualFold(key, "a") && mods&(2|4) != 0 {
			m["commands"] = []string{"SelectAll"}
		}
		_, e := call[map[string]any, struct{}](ctx, t, "Input.dispatchKeyEvent", m)
		return e
	}
	if name != "keyup" {
		active := 0
		for _, m := range k.mods {
			active |= m.bit
			if err = dispatch("rawKeyDown", m.key, m.code, m.vk, active, ""); err != nil {
				return nil, err
			}
		}
		typ := "rawKeyDown"
		if k.text != "" {
			typ = "keyDown"
		}
		if err = dispatch(typ, k.key, k.code, k.vk, k.modifiers, k.text); err != nil {
			return nil, err
		}
	}
	if name != "keydown" {
		if err = dispatch("keyUp", k.key, k.code, k.vk, k.modifiers, ""); err != nil {
			return nil, err
		}
		for i := len(k.mods) - 1; i >= 0; i-- {
			m := k.mods[i]
			if err = dispatch("keyUp", m.key, m.code, m.vk, k.modifiers, ""); err != nil {
				return nil, err
			}
			k.modifiers &^= m.bit
		}
	}
	return map[string]any{"ok": true}, nil
}

func (st *automationState) check(ctx context.Context, t cdp.Session, want bool, p map[string]json.RawMessage) (any, error) {
	obj, err := st.resolve(ctx, t, p)
	if err != nil {
		return nil, err
	}
	defer release(ctx, t, obj)
	if st.frame != "" {
		return nil, autoErr("unsupported_frame", "pointer actions in child frames require parent-frame hit testing")
	}
	r, err := callFunction(ctx, t, obj, `function(){
if(!this.isConnected)return {error:'stale'};
if(!(this instanceof HTMLInputElement)||!['checkbox','radio'].includes(this.type))return {error:'not_checkable'};
return {type:this.type,checked:this.checked}}`, nil, true)
	if err != nil {
		return nil, err
	}
	var state struct {
		Type, Error string
		Checked     bool
	}
	if json.Unmarshal(r.Value, &state) != nil {
		return nil, autoErr("action_failed", "could not inspect checkable state")
	}
	if state.Error == "stale" {
		return nil, autoErr("stale_ref", "element is detached")
	}
	if state.Error != "" {
		return nil, autoErr("not_interactable", "element is not a checkbox or radio")
	}
	if !want && state.Type == "radio" {
		return nil, autoErr("not_interactable", "radio buttons cannot be unchecked")
	}
	epoch, err := st.selectedInfo(ctx, t)
	if err != nil {
		return nil, err
	}
	x, y, err := st.geometry(ctx, t, obj, true, true)
	if err != nil {
		return nil, err
	}
	if state.Checked != want {
		if err = st.dispatchClick(ctx, t, epoch, x, y, 1); err != nil {
			return nil, err
		}
	}
	r, err = callFunction(ctx, t, obj, `function(){return this.isConnected&&this.checked}`, nil, true)
	if err != nil {
		return nil, err
	}
	var got bool
	if json.Unmarshal(r.Value, &got) != nil || got != want {
		return nil, autoErr("action_failed", "checkable state did not become %t", want)
	}
	return map[string]any{"checked": got}, nil
}
func (st *automationState) selectOption(ctx context.Context, t cdp.Session, p map[string]json.RawMessage) (any, error) {
	obj, err := st.resolve(ctx, t, p)
	if err != nil {
		return nil, err
	}
	defer release(ctx, t, obj)
	v := stringParam(p, "value")
	if v == "" {
		v = stringParam(p, "text")
	}
	r, err := callFunction(ctx, t, obj, `function(v){let o=[...this.options].find(x=>x.value===v||x.text===v);if(!o)return false;this.value=o.value;this.dispatchEvent(new Event('input',{bubbles:true}));this.dispatchEvent(new Event('change',{bubbles:true}));return true}`, []callArg{{Value: v}}, true)
	if err != nil {
		return nil, err
	}
	var ok bool
	_ = json.Unmarshal(r.Value, &ok)
	if !ok {
		return nil, autoErr("not_found", "select option %q not found", v)
	}
	return map[string]any{"ok": true}, nil
}

func (st *automationState) scroll(ctx context.Context, t cdp.Session, name string, p map[string]json.RawMessage) (any, error) {
	if name == "scrollintoview" || len(p["selector"]) > 0 || len(p["ref"]) > 0 {
		obj, err := st.resolve(ctx, t, p)
		if err != nil {
			return nil, err
		}
		defer release(ctx, t, obj)
		_, _, err = st.geometry(ctx, t, obj, true, true)
		return map[string]any{"ok": err == nil}, err
	}
	dx := floatParam(p, "deltaX", 0)
	dy := floatParam(p, "deltaY", floatParam(p, "y", 0))
	_, err := call[map[string]any, struct{}](ctx, t, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": floatParam(p, "x", 1), "y": floatParam(p, "viewportY", 1), "deltaX": dx, "deltaY": dy})
	return map[string]any{"ok": err == nil}, err
}
func floatParam(p map[string]json.RawMessage, k string, d float64) float64 {
	if b := p[k]; len(b) > 0 {
		_ = json.Unmarshal(b, &d)
	}
	return d
}

func (st *automationState) wait(ctx context.Context, t cdp.Session, p map[string]json.RawMessage) (any, error) {
	ms := intParam(p, "timeoutMs", 30000)
	if ms < 0 {
		ms = 0
	}
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	state := stringParam(p, "state")
	if state == "" {
		state = "visible"
	}
	if state != "visible" && state != "hidden" {
		return nil, autoErr("invalid_params", "wait state must be visible or hidden")
	}
	hasLocator := len(p["selector"]) > 0 || len(p["ref"]) > 0 || len(p["role"]) > 0 || len(p["name"]) > 0 || len(p["text"]) > 0
	if !hasLocator {
		tm := time.NewTimer(time.Duration(ms) * time.Millisecond)
		defer tm.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tm.C:
			return map[string]any{"ok": true}, nil
		}
	}
	for {
		obj, err := st.resolve(ctx, t, p)
		visible := false
		if err == nil {
			r, e := callFunction(ctx, t, obj, `function(){if(!this.isConnected)return null;let r=this.getBoundingClientRect(),s=getComputedStyle(this);return r.width>0&&r.height>0&&s.display!=='none'&&s.visibility!=='hidden'}`, nil, true)
			release(ctx, t, obj)
			if e != nil {
				return nil, e
			}
			if string(r.Value) == "null" {
				err = autoErr("stale_ref", "element is detached")
			} else if json.Unmarshal(r.Value, &visible) != nil {
				return nil, autoErr("cdp_error", "could not determine element visibility")
			}
		} else {
			ae, ok := err.(*AutomationError)
			if !ok || (ae.Code != "not_found" && ae.Code != "stale_ref") {
				return nil, err
			}
		}
		done := (state == "hidden" && (err != nil || !visible)) || (state == "visible" && err == nil && visible)
		if done {
			return map[string]any{"ok": true}, nil
		}
		if time.Now().After(deadline) {
			return nil, autoErr("timeout", "wait timed out after %dms", ms)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

const maxAutomationTextResult = 4 * 1024 * 1024

func boundedTextResult(v any) (any, error) {
	if s, ok := v.(string); ok && len([]byte(s)) > maxAutomationTextResult {
		return nil, autoErr("result_too_large", "text/html result exceeds 4 MiB")
	}
	return v, nil
}

func (st *automationState) get(ctx context.Context, t cdp.Session, p map[string]json.RawMessage) (any, error) {
	kind := strings.ToLower(stringParam(p, "kind"))
	if kind == "title" {
		return st.evaluateValue(ctx, t, "document.title", false)
	}
	if kind == "url" {
		return st.evaluateValue(ctx, t, "location.href", false)
	}
	if len(p["selector"]) == 0 && len(p["ref"]) == 0 && len(p["role"]) == 0 && len(p["name"]) == 0 {
		switch kind {
		case "text":
			v, err := st.evaluateValue(ctx, t, "document.body ? document.body.innerText : ''", false)
			if err != nil {
				return nil, err
			}
			return boundedTextResult(v)
		case "html":
			v, err := st.evaluateValue(ctx, t, "document.documentElement ? document.documentElement.outerHTML : ''", false)
			if err != nil {
				return nil, err
			}
			return boundedTextResult(v)
		}
	}
	if kind == "count" {
		sel := stringParam(p, "selector")
		if sel == "" {
			return nil, autoErr("invalid_params", "count requires selector")
		}
		doc, err := st.documentObject(ctx, t)
		if err != nil {
			return nil, err
		}
		defer release(ctx, t, doc)
		r, err := callFunction(ctx, t, doc, `function(s){return this.querySelectorAll(s).length}`, []callArg{{Value: sel}}, true)
		if err != nil {
			return nil, err
		}
		var v any
		_ = json.Unmarshal(r.Value, &v)
		return v, nil
	}
	obj, err := st.resolve(ctx, t, p)
	if err != nil {
		return nil, err
	}
	defer release(ctx, t, obj)
	fn := map[string]string{"text": `function(){return this.innerText??this.textContent??''}`, "html": `function(){return this.outerHTML}`, "value": `function(){return this.value}`, "box": `function(){let r=this.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height}}`}[kind]
	args := []callArg{}
	if kind == "attr" {
		fn = `function(n){return this.getAttribute(n)}`
		args = []callArg{{Value: stringParam(p, "name")}}
	}
	if fn == "" {
		return nil, autoErr("invalid_params", "unknown get kind %q", kind)
	}
	r, err := callFunction(ctx, t, obj, fn, args, true)
	if err != nil {
		return nil, err
	}
	var v any
	if len(r.Value) > 0 {
		_ = json.Unmarshal(r.Value, &v)
	}
	if kind == "text" || kind == "html" {
		return boundedTextResult(v)
	}
	return v, nil
}
func (st *automationState) is(ctx context.Context, t cdp.Session, p map[string]json.RawMessage) (any, error) {
	obj, err := st.resolve(ctx, t, p)
	if err != nil {
		return nil, err
	}
	defer release(ctx, t, obj)
	kind := strings.ToLower(stringParam(p, "kind"))
	fn := map[string]string{"visible": `function(){let r=this.getBoundingClientRect(),s=getComputedStyle(this);return r.width>0&&r.height>0&&s.display!=='none'&&s.visibility!=='hidden'}`, "enabled": `function(){return !this.disabled&&!this.matches('[aria-disabled=true]')}`, "checked": `function(){return !!this.checked}`}[kind]
	if fn == "" {
		return nil, autoErr("invalid_params", "unknown is kind %q", kind)
	}
	r, err := callFunction(ctx, t, obj, fn, nil, true)
	if err != nil {
		return nil, err
	}
	var v bool
	_ = json.Unmarshal(r.Value, &v)
	return v, nil
}

func (st *automationState) find(ctx context.Context, t cdp.Session, p map[string]json.RawMessage) (any, error) {
	snap, err := st.snapshot(ctx, t, map[string]json.RawMessage{"maxNodes": json.RawMessage("10000")})
	if err != nil {
		return nil, err
	}
	m := snap.(map[string]any)
	nodes := m["nodes"].([]snapshotNode)
	role := strings.ToLower(stringParam(p, "role"))
	name := stringParam(p, "name")
	text := stringParam(p, "text")
	exact := boolParam(p, "exact", false)
	refs := []string{}
	match := func(hay, needle string) bool {
		if needle == "" {
			return true
		}
		if exact {
			return hay == needle
		}
		return strings.Contains(strings.ToLower(hay), strings.ToLower(needle))
	}
	for _, n := range nodes {
		if role != "" && strings.ToLower(n.Role) != role {
			continue
		}
		if !match(n.Name, name) {
			continue
		}
		v := fmt.Sprint(n.Value)
		if !match(n.Name+" "+v, text) {
			continue
		}
		refs = append(refs, n.Ref)
	}
	return map[string]any{"refs": refs, "count": len(refs)}, nil
}
func (st *automationState) leaveChild() {
	if st.childStop != nil {
		st.childStop() // WithDetachOnCancel: detach the session, never close target.
	}
	st.childCtx, st.childStop, st.target = nil, nil, ""
}

func defaultDocumentObject(ctx context.Context, t cdp.Session) (string, error) {
	r, err := call[map[string]any, runtimeResult](ctx, t, "Runtime.evaluate", map[string]any{"expression": "document", "returnByValue": false})
	if err != nil {
		return "", err
	}
	if r.Result.ObjectID == "" {
		return "", autoErr("not_found", "document unavailable")
	}
	return r.Result.ObjectID, nil
}

func (st *automationState) frameAction(ctx context.Context, t *chromedp.Target, p map[string]json.RawMessage) (any, error) {
	if boolParam(p, "main", false) || stringParam(p, "selector") == "main" || stringParam(p, "selector") == "" {
		st.leaveChild()
		st.frame = ""
		st.context = 0
		st.frameName = ""
		st.refs = map[string]automationRef{}
		st.back = map[int64]string{}
		if info, e := st.selectedInfo(ctx, t); e == nil {
			st.loader = info.Loader
		}
		return map[string]any{"ok": true, "frame": "main"}, nil
	}
	sel := stringParam(p, "selector")
	// Frame selectors deliberately resolve in the page document. This also
	// makes selecting another frame deterministic while an OOPIF is selected.
	doc, err := defaultDocumentObject(ctx, t)
	if err != nil {
		return nil, err
	}
	defer release(ctx, t, doc)
	r, err := callFunction(ctx, t, doc, `function(s){let a=this.querySelectorAll(s);return a.length===1?a[0]:{__frameCount:a.length}}`, []callArg{{Value: sel}}, false)
	if err != nil {
		return nil, err
	}
	if r.ObjectID == "" {
		return nil, autoErr("not_found", "frame %q not found", sel)
	}
	defer release(ctx, t, r.ObjectID)
	q, e := callFunction(ctx, t, r.ObjectID, `function(){return this.__frameCount===undefined?-1:this.__frameCount}`, nil, true)
	if e != nil {
		return nil, e
	}
	var count int = -1
	_ = json.Unmarshal(q.Value, &count)
	if count >= 0 {
		if count == 0 {
			return nil, autoErr("not_found", "frame %q not found", sel)
		}
		return nil, autoErr("ambiguous_locator", "frame %q matched %d elements", sel, count)
	}
	d, err := call[map[string]any, struct {
		Node struct {
			FrameID   string `json:"frameId"`
			LocalName string `json:"localName"`
		} `json:"node"`
	}](ctx, t, "DOM.describeNode", map[string]any{"objectId": r.ObjectID, "depth": 0})
	if err != nil {
		return nil, err
	}
	if d.Node.FrameID == "" {
		return nil, autoErr("invalid_arguments", "selector %q is not a frame element", sel)
	}
	frames, err := frameTree(ctx, t)
	if err != nil {
		return nil, err
	}
	var wanted frameDesc
	for _, f := range frames {
		if f.ID == d.Node.FrameID {
			wanted = f
			break
		}
	}
	if wanted.ID == "" {
		// Some Chromium versions omit a detached OOPIF subtree from the parent
		// Page.getFrameTree response. DOM.describeNode still supplies the browser
		// frame id; bind its URL to the selected owner element before consulting
		// browser-level targets.
		src, srcErr := callFunction(ctx, t, r.ObjectID, `function(){return this instanceof HTMLIFrameElement||this instanceof HTMLFrameElement?this.src:''}`, nil, true)
		var ownerURL string
		if srcErr != nil || json.Unmarshal(src.Value, &ownerURL) != nil || ownerURL == "" {
			return nil, autoErr("unsupported_frame", "frame %q disappeared before it could be selected", sel)
		}
		wanted = frameDesc{ID: d.Node.FrameID, URL: ownerURL}
	}

	oldFrame, oldContext, oldName, oldLoader, oldTarget := st.frame, st.context, st.frameName, st.loader, st.target
	st.frame = d.Node.FrameID
	st.context = 0
	st.frameName = sel
	st.target = ""
	info, e := st.selectedInfo(ctx, t)
	if e == nil {
		_, e = st.evaluateValue(ctx, t, "location.href", false)
	}
	if e == nil {
		st.leaveChild()
		st.loader = info.Loader
		st.refs = map[string]automationRef{}
		st.back = map[int64]string{}
		return map[string]any{"ok": true, "frame": sel}, nil
	}

	// Page.createIsolatedWorld failed, so only an exact browser-level iframe
	// target association may be used. URL-only matching is unsafe when a page
	// embeds the same URL more than once; Chromium's OOPIF target id is the
	// frame id, and ParentID ties it to this page target.
	targets, targetErr := chromedp.Targets(ctx)
	var candidate *target.Info
	if targetErr == nil {
		for _, ti := range targets {
			if ti == nil || ti.Type != "iframe" || string(ti.TargetID) != wanted.ID || ti.URL != wanted.URL {
				continue
			}
			if ti.ParentID != "" && ti.ParentID != t.TargetID {
				continue
			}
			candidate = ti
			break
		}
	}
	if candidate == nil {
		st.frame, st.context, st.frameName, st.loader, st.target = oldFrame, oldContext, oldName, oldLoader, oldTarget
		return nil, autoErr("unsupported_frame", "frame %q has no unambiguous browser-level iframe target (frameId=%s url=%q): %v", sel, wanted.ID, wanted.URL, targetErr)
	}
	childCtx, cancelChild := chromedp.NewContext(st.base, chromedp.WithTargetID(candidate.TargetID), chromedp.WithDetachOnCancel())
	var childInfo frameDesc
	attachErr := chromedp.Do(childCtx, chromedp.Func(func(c context.Context, child *chromedp.Target) error {
		fs, err := frameTree(c, child)
		if err != nil {
			return err
		}
		if len(fs) == 0 || fs[0].ID != wanted.ID || fs[0].URL != wanted.URL {
			return autoErr("unsupported_frame", "iframe target identity changed while attaching")
		}
		childInfo = fs[0]
		return nil
	}))
	if attachErr != nil {
		_ = chromedp.Cancel(childCtx)
		cancelChild()
		st.frame, st.context, st.frameName, st.loader, st.target = oldFrame, oldContext, oldName, oldLoader, oldTarget
		return nil, autoErr("unsupported_frame", "could not attach to frame %q target safely: %v", sel, attachErr)
	}
	st.leaveChild()
	st.childCtx = childCtx
	st.childStop = func() { _ = chromedp.Cancel(childCtx); cancelChild() }
	st.target = string(candidate.TargetID)
	st.loader = childInfo.Loader
	st.refs = map[string]automationRef{}
	st.back = map[int64]string{}
	return map[string]any{"ok": true, "frame": sel}, nil
}
func (st *automationState) upload(ctx context.Context, t cdp.Session, p map[string]json.RawMessage) (any, error) {
	obj, err := st.resolve(ctx, t, p)
	if err != nil {
		return nil, err
	}
	defer release(ctx, t, obj)
	var files []string
	if err = json.Unmarshal(p["files"], &files); err != nil || len(files) == 0 {
		return nil, autoErr("invalid_params", "files must be a non-empty string array")
	}
	_, err = call[map[string]any, struct{}](ctx, t, "DOM.setFileInputFiles", map[string]any{"files": files, "objectId": obj})
	return map[string]any{"ok": err == nil}, err
}
