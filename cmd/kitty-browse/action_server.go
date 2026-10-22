package main

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

type actionView struct {
	info     action.Info
	sessions map[string]*browser.Session
}

type hostActionCall struct {
	ctx   context.Context
	req   action.Request
	reply chan action.Response
}

// actionBridge publishes immutable discovery snapshots. Native page operations
// run outside the UI loop; tab/navigation/viewport changes are posted back to
// that loop, which remains the sole owner of models and screencast selection.
type actionBridge struct {
	id     string
	view   atomic.Pointer[actionView]
	calls  chan hostActionCall
	active atomic.Int64
	limit  chan struct{}
	gates  sync.Map // target id (or browser scope) -> chan struct{}
}

func newActionBridge(id string) *actionBridge {
	return &actionBridge{id: id, calls: make(chan hostActionCall, 16), limit: make(chan struct{}, 8)}
}

// update is called only by the UI goroutine. Published values are never mutated.
func (b *actionBridge) update(tabs *browser.Tabs, titles map[*browser.Session]string) {
	view := &actionView{
		info: action.Info{APIVersion: action.APIVersion, BrowserID: b.id, Backend: action.Backend,
			Capabilities: action.Capabilities(), Tabs: []action.TabInfo{}},
		sessions: make(map[string]*browser.Session),
	}
	for i, sess := range tabs.Sessions() {
		id := sess.TargetID()
		item := action.TabInfo{ID: id, TargetID: id, Index: i + 1, URL: sess.URL(), Title: titles[sess], Active: i == tabs.Index()}
		view.info.Tabs = append(view.info.Tabs, item)
		view.sessions[id] = sess
		if item.Active {
			view.info.ActiveTabID = id
		}
	}
	b.view.Store(view)
}

func (b *actionBridge) information(context.Context) (json.RawMessage, error) {
	view := b.view.Load()
	if view == nil {
		return nil, fmt.Errorf("browser is starting")
	}
	return json.Marshal(view.info)
}

func (b *actionBridge) execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var req action.Request
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return json.Marshal(action.Respond(req, action.Info{BrowserID: b.id}, action.TabInfo{}, nil,
			clientError("invalid_arguments", "invalid typed action request")))
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return json.Marshal(action.Respond(req, action.Info{BrowserID: b.id}, action.TabInfo{}, nil,
			clientError("invalid_arguments", "request must contain a single JSON object")))
	}
	view := b.view.Load()
	if view == nil {
		return json.Marshal(action.Respond(req, action.Info{BrowserID: b.id}, action.TabInfo{}, nil,
			clientError("browser_unavailable", "browser is not ready")))
	}
	respond := func(target action.TabInfo, result json.RawMessage, err error) (json.RawMessage, error) {
		return json.Marshal(action.Respond(req, view.info, target, result, err))
	}
	if err := action.ValidateRequest(req); err != nil {
		return respond(action.TabInfo{}, nil, err)
	}
	if req.Action == "" {
		return respond(action.TabInfo{}, nil, clientError("invalid_arguments", "action is required"))
	}
	if len(req.Params) != 0 && !bytes.Equal(bytes.TrimSpace(req.Params), []byte("null")) &&
		(len(bytes.TrimSpace(req.Params)) == 0 || bytes.TrimSpace(req.Params)[0] != '{') {
		return respond(action.TabInfo{}, nil, clientError("invalid_arguments", "params must be a JSON object"))
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
		return respond(action.TabInfo{}, nil, clientError("target_busy", "too many action requests are already active"))
	}
	var target action.TabInfo
	if browserScopedAction(req.Action) {
		if req.Target.BrowserID != "" && req.Target.BrowserID != view.info.BrowserID {
			return respond(target, nil, clientError("target_not_found", "browser does not match the active browser"))
		}
		target, _ = action.SelectTarget(view.info, action.Target{}) // Metadata only.
		req.Target = action.Target{BrowserID: view.info.BrowserID}
	} else {
		if err := resolveHostTabID(view.info, &req); err != nil {
			return respond(target, nil, err)
		}
		target, err = action.SelectTarget(view.info, req.Target)
		if err != nil {
			return respond(target, nil, err)
		}
		// Even a raw JSON request without selectors is pinned at acceptance time.
		req.Target = action.Target{BrowserID: view.info.BrowserID, TabID: target.ID, TargetID: target.TargetID}
	}
	releaseBrowser := func() {}
	if hostManagedAction(req.Action) {
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
	b.active.Add(1)
	defer b.active.Add(-1)
	if hostManagedAction(req.Action) {
		call := hostActionCall{ctx: op, req: req, reply: make(chan action.Response, 1)}
		select {
		case b.calls <- call:
		case <-op.Done():
			return respond(target, nil, op.Err())
		}
		select {
		case response := <-call.reply:
			return json.Marshal(response)
		case <-op.Done():
			return respond(target, nil, op.Err())
		}
	}
	sess := view.sessions[target.ID]
	if sess == nil || sess.Context().Err() != nil {
		return respond(target, nil, clientError("target_not_found", "selected tab is closed"))
	}
	result, err := action.Execute(op, req, sess)
	return respond(target, result, err)
}

func (b *actionBridge) acquire(ctx context.Context, key string) (func(), error) {
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

func browserScopedAction(name string) bool {
	switch name {
	case "info", "capabilities", "done", "tab.list", "tab.new":
		return true
	}
	return false
}

func hostManagedAction(name string) bool {
	switch name {
	case "info", "capabilities", "done", "tab.list", "tab.new", "tab.activate", "tab.close",
		"navigate", "back", "forward", "reload", "set.viewport":
		return true
	}
	return false
}

// The typed target is authoritative. A tabId parameter is a CLI convenience,
// resolved to the same stable ID before queuing a UI mutation.
func resolveHostTabID(info action.Info, req *action.Request) error {
	if req.Action != "tab.activate" && req.Action != "tab.close" {
		return nil
	}
	var p struct {
		TabID string `json:"tabId"`
	}
	if len(req.Params) != 0 && json.Unmarshal(req.Params, &p) != nil {
		return clientError("invalid_arguments", "invalid tab parameters")
	}
	if p.TabID == "" {
		return nil
	}
	id, err := displayedTabID(info, p.TabID)
	if err != nil {
		return err
	}
	if req.Target.TabID != "" && req.Target.TabID != id {
		return clientError("invalid_arguments", "tabId parameter conflicts with target.tabId")
	}
	req.Target.TabID = id
	return nil
}
