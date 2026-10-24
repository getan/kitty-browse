// Package host owns the parts of kitty-browse's native control API that every
// host shares: the typed action bridge, target pinning and the host-managed
// operations a visible pane and a headless host must perform identically.
//
// Page actions are executed by internal/action exactly once for both hosts, so
// a headless browser cannot drift from the visible pane's semantics.
package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/browser"
)

// Snapshot is one immutable discovery view. Published snapshots are never
// mutated after Update stores them.
type Snapshot struct {
	Info     action.Info
	Sessions map[string]*browser.Session
}

// Call is one host-managed action posted to the goroutine that owns the
// browser's UI state.
type Call struct {
	Ctx   context.Context
	Req   action.Request
	Reply chan action.Response
}

// Bridge publishes immutable discovery snapshots. Native page operations run
// outside the host loop; tab/navigation/viewport changes are posted back to that
// loop, which remains the sole owner of models and screencast selection.
type Bridge struct {
	ID     string
	view   atomic.Pointer[Snapshot]
	Calls  chan Call
	Active atomic.Int64
	limit  chan struct{}
	gates  sync.Map // target id (or browser scope) -> chan struct{}
}

// NewBridge creates a bridge for one browser identity.
func NewBridge(id string) *Bridge {
	return &Bridge{ID: id, Calls: make(chan Call, 16), limit: make(chan struct{}, 8)}
}

// Load returns the current snapshot, or nil before the first Update.
func (b *Bridge) Load() *Snapshot { return b.view.Load() }

// Update is called only by the host loop. Published values are never mutated.
func (b *Bridge) Update(tabs *browser.Tabs, titles map[*browser.Session]string) {
	view := &Snapshot{
		Info: action.Info{APIVersion: action.APIVersion, BrowserID: b.ID, Backend: action.Backend,
			Capabilities: action.Capabilities(), Tabs: []action.TabInfo{}},
		Sessions: make(map[string]*browser.Session),
	}
	for i, sess := range tabs.Sessions() {
		id := sess.TargetID()
		item := action.TabInfo{ID: id, TargetID: id, Index: i + 1, URL: sess.URL(), Title: titles[sess], Active: i == tabs.Index()}
		view.Info.Tabs = append(view.Info.Tabs, item)
		view.Sessions[id] = sess
		if item.Active {
			view.Info.ActiveTabID = id
		}
	}
	b.view.Store(view)
}

// Information serves /v1/info.
func (b *Bridge) Information(context.Context) (json.RawMessage, error) {
	view := b.view.Load()
	if view == nil {
		return nil, fmt.Errorf("browser is starting")
	}
	return json.Marshal(view.Info)
}

// Execute serves /v1/actions.
func (b *Bridge) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var req action.Request
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return json.Marshal(action.Respond(req, action.Info{BrowserID: b.ID}, action.TabInfo{}, nil,
			ClientError("invalid_arguments", "invalid typed action request")))
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return json.Marshal(action.Respond(req, action.Info{BrowserID: b.ID}, action.TabInfo{}, nil,
			ClientError("invalid_arguments", "request must contain a single JSON object")))
	}
	view := b.view.Load()
	if view == nil {
		return json.Marshal(action.Respond(req, action.Info{BrowserID: b.ID}, action.TabInfo{}, nil,
			ClientError("browser_unavailable", "browser is not ready")))
	}
	respond := func(target action.TabInfo, result json.RawMessage, err error) (json.RawMessage, error) {
		return json.Marshal(action.Respond(req, view.Info, target, result, err))
	}
	if err := action.ValidateRequest(req); err != nil {
		return respond(action.TabInfo{}, nil, err)
	}
	if req.Action == "" {
		return respond(action.TabInfo{}, nil, ClientError("invalid_arguments", "action is required"))
	}
	if len(req.Params) != 0 && !bytes.Equal(bytes.TrimSpace(req.Params), []byte("null")) &&
		(len(bytes.TrimSpace(req.Params)) == 0 || bytes.TrimSpace(req.Params)[0] != '{') {
		return respond(action.TabInfo{}, nil, ClientError("invalid_arguments", "params must be a JSON object"))
	}
	duration, err := action.RequestTimeout(req)
	if err != nil {
		return respond(action.TabInfo{}, nil, err)
	}
	op, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	select {
	case b.limit <- struct{}{}:
		defer func() { <-b.limit }()
	default:
		return respond(action.TabInfo{}, nil, ClientError("target_busy", "too many action requests are already active"))
	}
	var target action.TabInfo
	if BrowserScopedAction(req.Action) {
		if req.Target.BrowserID != "" && req.Target.BrowserID != view.Info.BrowserID {
			return respond(target, nil, ClientError("target_not_found", "browser does not match the active browser"))
		}
		target, _ = action.SelectTarget(view.Info, action.Target{}) // Metadata only.
		req.Target = action.Target{BrowserID: view.Info.BrowserID}
	} else {
		if err := ResolveTabID(view.Info, &req); err != nil {
			return respond(target, nil, err)
		}
		target, err = action.SelectTarget(view.Info, req.Target)
		if err != nil {
			return respond(target, nil, err)
		}
		// Even a raw JSON request without selectors is pinned at acceptance time.
		req.Target = action.Target{BrowserID: view.Info.BrowserID, TabID: target.ID, TargetID: target.TargetID}
	}
	releaseBrowser := func() {}
	if HostManagedAction(req.Action) {
		var acquireErr error
		releaseBrowser, acquireErr = b.acquire(op, "__browser__")
		if acquireErr != nil {
			return respond(target, nil, acquireErr)
		}
		defer releaseBrowser()
	}
	if target.ID != "" {
		releaseTarget, acquireErr := b.acquire(op, target.ID)
		if acquireErr != nil {
			return respond(target, nil, acquireErr)
		}
		defer releaseTarget()
	}
	b.Active.Add(1)
	defer b.Active.Add(-1)
	if HostManagedAction(req.Action) {
		call := Call{Ctx: op, Req: req, Reply: make(chan action.Response, 1)}
		select {
		case b.Calls <- call:
		case <-op.Done():
			return respond(target, nil, op.Err())
		}
		select {
		case response := <-call.Reply:
			return json.Marshal(response)
		case <-op.Done():
			return respond(target, nil, op.Err())
		}
	}
	sess := view.Sessions[target.ID]
	if sess == nil || sess.Context().Err() != nil {
		return respond(target, nil, ClientError("target_not_found", "selected tab is closed"))
	}
	result, err := action.Execute(op, req, sess)
	return respond(target, result, err)
}

func (b *Bridge) acquire(ctx context.Context, key string) (func(), error) {
	created := make(chan struct{}, 1)
	actual, _ := b.gates.LoadOrStore(key, created)
	gate := actual.(chan struct{})
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// BrowserScopedAction reports whether an action addresses the browser rather
// than one tab.
func BrowserScopedAction(name string) bool {
	switch name {
	case "info", "capabilities", "done", "quit", "tab.list", "tab.new":
		return true
	}
	return false
}

// HostManagedAction reports whether an action must run on the goroutine that
// owns the browser's UI state.
func HostManagedAction(name string) bool {
	switch name {
	case "info", "capabilities", "done", "quit", "tab.list", "tab.new", "tab.activate", "tab.close",
		"navigate", "back", "forward", "reload", "set.viewport":
		return true
	}
	return false
}

// ResolveTabID turns the tabId convenience parameter into the stable target ID
// before the request is queued. The typed target stays authoritative.
func ResolveTabID(info action.Info, req *action.Request) error {
	if req.Action != "tab.activate" && req.Action != "tab.close" {
		return nil
	}
	var p struct {
		TabID string `json:"tabId"`
	}
	if len(req.Params) != 0 && json.Unmarshal(req.Params, &p) != nil {
		return ClientError("invalid_arguments", "invalid tab parameters")
	}
	if p.TabID == "" {
		return nil
	}
	id, err := DisplayedTabID(info, p.TabID)
	if err != nil {
		return err
	}
	if req.Target.TabID != "" && req.Target.TabID != id {
		return ClientError("invalid_arguments", "tabId parameter conflicts with target.tabId")
	}
	req.Target.TabID = id
	return nil
}
