//go:build unix

package browsepane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/control"
)

func TestActionDefaultsToAndPinsActiveTarget(t *testing.T) {
	info := testActionInfo()
	var requests []action.Request
	server, socket := startActionCLIServer(t, info, func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var req action.Request
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, req)
		return mustJSON(t, action.Respond(req, info, info.Tabs[0], json.RawMessage(`{"value":7}`), nil)), nil
	})
	defer server.Close()

	var out bytes.Buffer
	if err := runAction([]string{"-socket", socket, "--", "eval", "6+1"}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	req := requests[0]
	if req.ID == "" || req.TimeoutMS != 30000 {
		t.Fatalf("request identity/timeout = %#v", req)
	}
	want := (action.Target{BrowserID: "browser-1", TabID: "tab-active", TargetID: "target-active"})
	if req.Target != want {
		t.Fatalf("target = %#v, want %#v", req.Target, want)
	}
	var response action.Response
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || !response.OK {
		t.Fatalf("response = %#v, %v", response, err)
	}
}

func TestActionTabIndexTargetAndFollow(t *testing.T) {
	info := testActionInfo()
	var mu sync.Mutex
	var requests []action.Request
	server, socket := startActionCLIServer(t, info, func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var req action.Request
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		return mustJSON(t, action.Respond(req, info, info.Tabs[1], json.RawMessage(`null`), nil)), nil
	})
	defer server.Close()

	var out bytes.Buffer
	if err := runAction([]string{"-socket", socket, "--tab", "2", "--follow", "--", "click", "#go"}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	firstRequests := append([]action.Request(nil), requests...)
	requests = nil
	mu.Unlock()
	if len(firstRequests) != 2 || firstRequests[0].Action != "tab.activate" || firstRequests[1].Action != "click" {
		t.Fatalf("request order = %#v", firstRequests)
	}
	for _, req := range firstRequests {
		if req.Target.TabID != "tab-second" || req.Target.TargetID != "target-second" {
			t.Fatalf("request was not pinned: %#v", req.Target)
		}
	}
	out.Reset()
	if err := runAction([]string{"-socket", socket, "--target", "target-second", "--", "snapshot"}, nil, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].Target.TabID != "tab-second" {
		t.Fatalf("target ID resolution = %#v", requests)
	}
}

func TestActionRawJSONPreservesTargetAndAllowsExplicitOverride(t *testing.T) {
	info := testActionInfo()
	requests := make(chan action.Request, 2)
	server, socket := startActionCLIServer(t, info, func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var req action.Request
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		requests <- req
		tab, err := action.SelectTarget(info, req.Target)
		if err != nil {
			return nil, err
		}
		return mustJSON(t, action.Respond(req, info, tab, json.RawMessage(`null`), nil)), nil
	})
	defer server.Close()

	raw := `{"action":"snapshot","target":{"browserId":"browser-1","tabId":"tab-second"}}`
	if err := runAction([]string{"-socket", socket, "-request-json", "-"}, bytes.NewBufferString(raw), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := <-requests; got.Target.TabID != "tab-second" || got.Target.TargetID != "target-second" || got.ID == "" {
		t.Fatalf("raw target = %#v", got)
	}

	if err := runAction([]string{"-socket", socket, "-tab", "1", "-request-json", raw}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := <-requests; got.Target.TabID != "tab-active" || got.Target.TargetID != "target-active" {
		t.Fatalf("explicit override = %#v", got.Target)
	}
}

func TestActionFailureEnvelopeAndClientTimeout(t *testing.T) {
	info := testActionInfo()
	server, socket := startActionCLIServer(t, info, func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var req action.Request
		_ = json.Unmarshal(raw, &req)
		return mustJSON(t, action.Respond(req, info, info.Tabs[0], nil,
			&action.Error{Code: "not_clickable", Message: "element is not clickable"})), nil
	})
	var out bytes.Buffer
	err := runAction([]string{"-socket", socket, "--", "click", "#secret"}, nil, &out, io.Discard)
	var apiErr *action.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "not_clickable" {
		t.Fatalf("error = %T %v", err, err)
	}
	var response action.Response
	if json.Unmarshal(out.Bytes(), &response) != nil || response.OK || response.Error == nil {
		t.Fatalf("failure response = %s", out.Bytes())
	}
	_ = server.Close()

	server, socket = startActionCLIServer(t, info, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, context.Canceled
	})
	out.Reset()
	err = runAction([]string{"-socket", socket, "--", "snapshot"}, nil, &out, io.Discard)
	if !errors.As(err, &apiErr) || apiErr.Code != "canceled" {
		t.Fatalf("canceled error = %T %v", err, err)
	}
	if json.Unmarshal(out.Bytes(), &response) != nil || response.Error == nil || response.Error.Code != "canceled" {
		t.Fatalf("canceled response = %s", out.Bytes())
	}
	_ = server.Close()

	server, socket = startActionCLIServer(t, info, func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	defer server.Close()
	out.Reset()
	err = runAction([]string{"-socket", socket, "-timeout", "5ms", "--", "snapshot"}, nil, &out, io.Discard)
	if !errors.As(err, &apiErr) || apiErr.Code != "timeout" {
		t.Fatalf("timeout error = %T %v", err, err)
	}
	if json.Unmarshal(out.Bytes(), &response) != nil || response.Error == nil || response.Error.Code != "timeout" {
		t.Fatalf("timeout response = %s", out.Bytes())
	}
}

func TestActionInfoAndNoSocket(t *testing.T) {
	info := testActionInfo()
	server, socket := startActionCLIServer(t, info, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("unexpected action")
	})
	defer server.Close()
	var out bytes.Buffer
	if err := runInfo([]string{"-socket", socket}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var got action.Info
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.BrowserID != info.BrowserID {
		t.Fatalf("info = %#v, %v", got, err)
	}

	t.Setenv("KITTY_BROWSE_SOCKET", "")
	if err := runAction([]string{"--", "snapshot"}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("missing socket must not launch a browser")
	}
	if err := runInfo([]string{"-h"}, io.Discard, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v", err)
	}
}

func testActionInfo() action.Info {
	return action.Info{
		APIVersion:  action.APIVersion,
		BrowserID:   "browser-1",
		ActiveTabID: "tab-active",
		Backend:     action.Backend,
		Tabs: []action.TabInfo{
			{ID: "tab-active", TargetID: "target-active", Index: 1, Active: true},
			{ID: "tab-second", TargetID: "target-second", Index: 2},
		},
	}
}

func startActionCLIServer(t *testing.T, info action.Info, fn control.ActionFunc) (*control.Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "kb-action-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "control.sock")
	server, err := control.Start(socket, func(context.Context, bool) ([]byte, error) {
		return nil, errors.New("unexpected screenshot")
	}, control.WithAPI(func(context.Context) (json.RawMessage, error) {
		return mustJSON(t, info), nil
	}, fn))
	if err != nil {
		t.Fatal(err)
	}
	return server, socket
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
