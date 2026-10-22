package action

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/getan/kitty-browse/internal/browser"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		action string
		params map[string]any
		code   string
	}{
		{"snapshot flags", []string{"snapshot", "-i", "-c", "-d", "3"}, "snapshot", map[string]any{"interactive": true, "compact": true, "depth": float64(3)}, ""},
		{"eval remains one script", []string{"eval", "document.body", "&&", "true"}, "eval", map[string]any{"script": "document.body && true"}, ""},
		{"click ref", []string{"click", "@e3"}, "click", map[string]any{"ref": "@e3"}, ""},
		{"fill", []string{"fill", "#name", "Ada Lovelace"}, "fill", map[string]any{"selector": "#name", "text": "Ada Lovelace"}, ""},
		{"key", []string{"press", "Enter"}, "press", map[string]any{"key": "Enter"}, ""},
		{"keyboard insert", []string{"keyboard", "insert", "hello world"}, "keyboard.insert", map[string]any{"text": "hello world"}, ""},
		{"get text", []string{"get", "text", "main"}, "get", map[string]any{"kind": "text", "selector": "main"}, ""},
		{"find role and name", []string{"find", "role", "button", "--name", "Save", "--exact"}, "find", map[string]any{"role": "button", "name": "Save", "exact": true}, ""},
		{"wait URL", []string{"wait", "url", "*#ready", "--exact", "--timeout-ms", "250"}, "wait.url", map[string]any{"pattern": "*#ready", "exact": true}, ""},
		{"wait function", []string{"wait", "function", "window.ready"}, "wait.function", map[string]any{"script": "window.ready"}, ""},
		{"wait load", []string{"wait", "loadstate", "DOMContentLoaded"}, "wait.loadState", map[string]any{"state": "domcontentloaded"}, ""},
		{"wait popup", []string{"wait", "popup", "*/receipt", "--timeout-ms", "250"}, "wait.popup", map[string]any{"urlPattern": "*/receipt"}, ""},
		{"wait any popup", []string{"wait", "popup"}, "wait.popup", map[string]any{}, ""},
		{"dialog status", []string{"dialog", "status"}, "dialog.status", map[string]any{}, ""},
		{"dialog prompt", []string{"dialog", "accept", "private value"}, "dialog.accept", map[string]any{"promptText": "private value"}, ""},
		{"network", []string{"network", "requests", "--clear", "--filter", "api"}, "network.requests", map[string]any{"clear": true, "filter": "api"}, ""},
		{"cookie", []string{"cookies", "set", "sid", "abc", "--url", "https://example.test"}, "cookies.set", map[string]any{"name": "sid", "value": "abc", "url": "https://example.test"}, ""},
		{"storage", []string{"storage", "session", "set", "theme", "dark"}, "storage.set", map[string]any{"kind": "session", "key": "theme", "value": "dark"}, ""},
		{"open means new tab", []string{"open", "https://example.test"}, "tab.new", map[string]any{"url": "https://example.test"}, ""},
		{"goto means navigate", []string{"goto", "https://example.test"}, "navigate", map[string]any{"url": "https://example.test"}, ""},
		{"viewport", []string{"set", "viewport", "800", "600"}, "set.viewport", map[string]any{"width": float64(800), "height": float64(600)}, ""},
		{"screenshot", []string{"screenshot", "shot.png", "--full"}, "screenshot", map[string]any{"path": "shot.png", "full": true}, ""},
		{"unknown", []string{"launch"}, "", nil, "unsupported_action"},
		{"bad arguments", []string{"fill", "#only"}, "", nil, "invalid_arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := ParseCommand(tt.args)
			if tt.code != "" {
				var apiErr *Error
				if !errors.As(err, &apiErr) || apiErr.Code != tt.code {
					t.Fatalf("error = %v, want %s", err, tt.code)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if req.Action != tt.action {
				t.Fatalf("action = %q, want %q", req.Action, tt.action)
			}
			if tt.name == "dialog prompt" && (len(req.Args) != 2 || req.Args[1] != "[REDACTED]") {
				t.Fatalf("sensitive prompt retained in args: %#v", req.Args)
			}
			var got map[string]any
			if err := json.Unmarshal(req.Params, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.params) {
				t.Fatalf("params = %#v, want %#v", got, tt.params)
			}
		})
	}
}

func TestSelectTargetStrict(t *testing.T) {
	info := Info{BrowserID: "b", ActiveTabID: "a", Tabs: []TabInfo{{ID: "a", TargetID: "ta", Active: true}, {ID: "z", TargetID: ""}, {ID: "c", TargetID: "tc"}}}
	if got, err := SelectTarget(info, Target{}); err != nil || got.TargetID != "ta" {
		t.Fatalf("default = %#v, %v", got, err)
	}
	if got, err := SelectTarget(info, Target{TabID: "c", TargetID: "tc"}); err != nil || got.ID != "c" {
		t.Fatalf("exact = %#v, %v", got, err)
	}
	bad := []Target{{BrowserID: "other"}, {TabID: "missing"}, {TargetID: "missing"}, {TabID: "a", TargetID: "tc"}, {TabID: "z"}}
	for _, target := range bad {
		_, err := SelectTarget(info, target)
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != "target_not_found" {
			t.Errorf("target %#v: %v", target, err)
		}
	}
}

func TestRequestTimeout(t *testing.T) {
	if got, err := RequestTimeout(Request{}); err != nil || got != 30*time.Second {
		t.Fatalf("default = %v, %v", got, err)
	}
	if got, err := RequestTimeout(Request{TimeoutMS: 17}); err != nil || got != 17*time.Millisecond {
		t.Fatalf("explicit = %v, %v", got, err)
	}
	for _, n := range []int{-1, 120001} {
		if _, err := RequestTimeout(Request{TimeoutMS: n}); err == nil {
			t.Errorf("timeout %d accepted", n)
		}
	}
}

func TestRequestAPIVersion(t *testing.T) {
	for _, version := range []string{"", APIVersion} {
		if err := ValidateRequest(Request{APIVersion: version, Action: "done", Params: json.RawMessage(`{}`)}); err != nil {
			t.Fatalf("version %q: %v", version, err)
		}
	}
	_, err := Execute(context.Background(), Request{APIVersion: "v999", Action: "get"}, nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "unsupported_version" {
		t.Fatalf("unsupported version = %v", err)
	}
	data, err := json.Marshal(Request{Action: "get"})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if _, exists := envelope["apiVersion"]; exists {
		t.Fatalf("empty apiVersion was not omitted: %s", data)
	}
}

func TestCapabilitiesHaveSchemas(t *testing.T) {
	wanted := map[string]bool{"snapshot": true, "eval": false, "click": false, "fill": false, "get": true, "wait": true, "wait.url": true, "wait.function": true, "wait.loadState": true, "wait.popup": true, "dialog.status": true, "dialog.accept": false, "dialog.dismiss": false, "network.response": true, "cookies.get": true, "storage.set": false, "tab.list": true, "navigate": false, "set.viewport": false, "keyboard.insert": false, "pdf": true}
	for _, capability := range Capabilities() {
		want, ok := wanted[capability.Name]
		if !ok {
			continue
		}
		if capability.ReadOnly != want {
			t.Errorf("%s readOnly = %v, want %v", capability.Name, capability.ReadOnly, want)
		}
		var schema map[string]any
		if err := json.Unmarshal(capability.InputSchema, &schema); err != nil || schema["type"] != "object" {
			t.Errorf("%s schema = %s, %v", capability.Name, capability.InputSchema, err)
		}
		delete(wanted, capability.Name)
	}
	if len(wanted) != 0 {
		t.Fatalf("missing capabilities: %v", wanted)
	}
	var fill Capability
	for _, capability := range Capabilities() {
		if capability.Name == "fill" {
			fill = capability
		}
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(fill.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"selector", "ref", "text", "value"} {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("fill schema missing %s", name)
		}
	}
}

func TestExecuteHostAndUnsupported(t *testing.T) {
	for _, request := range []Request{
		{Action: "navigate", Params: json.RawMessage(`{"url":"https://example.test"}`)},
		{Action: "tab.new", Params: json.RawMessage(`{}`)},
		{Action: "set.viewport", Params: json.RawMessage(`{"width":800,"height":600}`)},
		{Action: "done", Params: json.RawMessage(`{}`)},
	} {
		_, err := Execute(context.Background(), request, nil)
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != "host_action_required" {
			t.Errorf("%s: %v", request.Action, err)
		}
	}
	_, err := Execute(context.Background(), Request{Action: "compat"}, nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "unsupported_action" {
		t.Fatalf("compat = %v", err)
	}
}

func TestValidateRequestUsesCapabilitySchema(t *testing.T) {
	valid := []Request{
		{Action: "snapshot", Params: json.RawMessage(`{"interactive":true,"maxNodes":10}`)},
		{Action: "fill", Params: json.RawMessage(`{"ref":"@e1","text":""}`)},
		{Action: "storage.set", Params: json.RawMessage(`{"kind":"local","key":"k","value":"v"}`)},
	}
	for _, request := range valid {
		if err := ValidateRequest(request); err != nil {
			t.Errorf("valid %s: %v", request.Action, err)
		}
	}
	for _, request := range []Request{
		{Action: "snapshot", Params: json.RawMessage(`{"unknown":true}`)},
		{Action: "fill", Params: json.RawMessage(`{"ref":"e1","text":"x"}`)},
		{Action: "storage.set", Params: json.RawMessage(`{"kind":"global","key":"k","value":"v"}`)},
		{Action: "set.viewport", Params: json.RawMessage(`{"width":0,"height":600}`)},
		{Action: "no.such.action", Params: json.RawMessage(`{}`)},
	} {
		err := ValidateRequest(request)
		var apiErr *Error
		if !errors.As(err, &apiErr) || (apiErr.Code != "invalid_arguments" && apiErr.Code != "unsupported_action") {
			t.Errorf("invalid %s accepted: %v", request.Action, err)
		}
	}
}

func TestRespondStableEnvelope(t *testing.T) {
	req := Request{ID: "r1"}
	info := Info{BrowserID: "b", Backend: Backend}
	target := TabInfo{ID: "t", TargetID: "cdp", URL: "https://example.test", Title: "Example"}
	good := Respond(req, info, target, json.RawMessage(`{"value":1}`), nil)
	if !good.OK || good.Error != nil || good.Meta.TargetID != "cdp" || string(good.Result) != `{"value":1}` {
		t.Fatalf("success = %#v", good)
	}
	for _, tt := range []struct {
		err   error
		code  string
		retry bool
	}{{context.DeadlineExceeded, "timeout", true}, {context.Canceled, "canceled", false}, {errors.New("secret CDP detail"), "internal_error", false}, {&Error{Code: "target_not_found", Message: "gone"}, "target_not_found", false}} {
		got := Respond(req, info, target, nil, tt.err)
		if got.OK || got.Error == nil || got.Error.Code != tt.code || got.Error.Retryable != tt.retry {
			t.Errorf("Respond(%v) = %#v", tt.err, got)
		}
	}
	mapped := Respond(req, info, target, nil, &browser.AutomationError{Code: "target_closed", Message: "gone"})
	if mapped.Error == nil || mapped.Error.Code != "target_not_found" {
		t.Fatalf("automation mapping = %#v", mapped)
	}
	for _, action := range []string{"click", "fill", "eval", "navigate", "tab.new"} {
		for _, operationErr := range []error{context.DeadlineExceeded, context.Canceled} {
			got := Respond(Request{Action: action}, info, target, nil, operationErr)
			if got.Error == nil || got.Error.Retryable || string(got.Error.Details) != `{"mayHaveExecuted":true}` {
				t.Errorf("mutating %v %s = %#v", operationErr, action, got.Error)
			}
		}
	}
	for _, name := range []string{"get", "wait.popup"} {
		read := Respond(Request{Action: name}, info, target, nil, context.DeadlineExceeded)
		if read.Error == nil || !read.Error.Retryable || len(read.Error.Details) != 0 {
			t.Fatalf("%s timeout = %#v", name, read.Error)
		}
	}
}

func TestWriteNewPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "document.pdf")
	if err := writeNewPrivateFile(path, []byte("%PDF-test")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if err := writeNewPrivateFile(path, []byte("overwrite")); err == nil {
		t.Fatal("existing file was overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "%PDF-test" {
		t.Fatalf("existing contents changed: %q", data)
	}
}

func TestJSONFieldNames(t *testing.T) {
	data, err := json.Marshal(Info{APIVersion: APIVersion, BrowserID: "b", ActiveTabID: "t", Backend: Backend})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"apiVersion", "browserId", "activeTabId", "backend", "tabs", "capabilities"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing %s in %s", key, data)
		}
	}
	if _, ok := got["cdpEndpoint"]; ok {
		t.Fatalf("legacy cdpEndpoint in %s", data)
	}
}
