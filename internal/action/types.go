// Package action exposes kitty-browse's versioned, native Chromium automation
// API. Browser and tab lifetimes remain owned by the host application.
package action

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const APIVersion = "v1"

const Backend = "native-cdp"

type Target struct {
	BrowserID string `json:"browserId"`
	TabID     string `json:"tabId"`
	TargetID  string `json:"targetId"`
}

type TabInfo struct {
	ID       string `json:"id"`
	TargetID string `json:"targetId"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	Index    int    `json:"index"`
	Active   bool   `json:"active"`
}

type Info struct {
	APIVersion   string       `json:"apiVersion"`
	BrowserID    string       `json:"browserId"`
	ActiveTabID  string       `json:"activeTabId"`
	Backend      string       `json:"backend"`
	Tabs         []TabInfo    `json:"tabs"`
	Capabilities []Capability `json:"capabilities"`
}

type Capability struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	ReadOnly    bool            `json:"readOnly"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type Request struct {
	APIVersion string          `json:"apiVersion,omitempty"`
	ID         string          `json:"id"`
	Action     string          `json:"action"`
	Params     json.RawMessage `json:"params"`
	Args       []string        `json:"args"`
	Target     Target          `json:"target"`
	TimeoutMS  int             `json:"timeoutMs"`
}

type Error struct {
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	Retryable bool            `json:"retryable"`
	Details   json.RawMessage `json:"details"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

type Meta struct {
	BrowserID string `json:"browserId"`
	TabID     string `json:"tabId"`
	TargetID  string `json:"targetId"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	Backend   string `json:"backend"`
}

type Response struct {
	APIVersion string          `json:"apiVersion"`
	ID         string          `json:"id"`
	OK         bool            `json:"ok"`
	Result     json.RawMessage `json:"result"`
	Error      *Error          `json:"error"`
	Meta       Meta            `json:"meta"`
}

// SelectTarget resolves only targets advertised by the host. Tab IDs are
// opaque stable IDs; indices and other transient aliases are not accepted.
func SelectTarget(info Info, target Target) (TabInfo, error) {
	if target.BrowserID != "" && target.BrowserID != info.BrowserID {
		return TabInfo{}, actionError("target_not_found", "browser does not match the active browser", false)
	}
	if target.TabID != "" && target.TargetID != "" {
		for _, tab := range info.Tabs {
			if tab.ID == target.TabID && tab.TargetID != "" && tab.TargetID == target.TargetID {
				return tab, nil
			}
		}
		return TabInfo{}, actionError("target_not_found", "tab and target IDs do not identify the same open target", false)
	}
	if target.TargetID != "" {
		for _, tab := range info.Tabs {
			if tab.TargetID != "" && tab.TargetID == target.TargetID {
				return tab, nil
			}
		}
		return TabInfo{}, actionError("target_not_found", "requested target is not available", false)
	}
	wantedTab := target.TabID
	if wantedTab == "" {
		wantedTab = info.ActiveTabID
		if wantedTab == "" {
			for _, tab := range info.Tabs {
				if tab.Active {
					wantedTab = tab.ID
					break
				}
			}
		}
	}
	for _, tab := range info.Tabs {
		if tab.ID == wantedTab && tab.TargetID != "" {
			return tab, nil
		}
	}
	return TabInfo{}, actionError("target_not_found", "requested tab is not available", false)
}

func actionError(code, message string, retryable bool) *Error {
	return &Error{Code: code, Message: message, Retryable: retryable}
}

// ValidateRequest checks the versioned request envelope. An omitted version is
// the v1 compatibility spelling; versions are otherwise matched exactly.
func ValidateRequest(req Request) error {
	if req.APIVersion != "" && req.APIVersion != APIVersion {
		return actionError("unsupported_version", "unsupported API version: "+req.APIVersion, false)
	}
	if strings.TrimSpace(req.Action) == "" {
		return actionError("invalid_arguments", "action is required", false)
	}
	if _, err := RequestTimeout(req); err != nil {
		return err
	}
	var capability *Capability
	for _, item := range Capabilities() {
		if item.Name == req.Action {
			copy := item
			capability = &copy
			break
		}
	}
	if capability == nil {
		return actionError("unsupported_action", "unsupported action: "+req.Action, false)
	}
	return validateParams(req.Params, capability.InputSchema)
}

func validateParams(raw, schemaRaw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var params map[string]any
	if err := decoder.Decode(&params); err != nil || params == nil {
		return actionError("invalid_arguments", "params must be a JSON object", false)
	}
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
	}
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return actionError("internal_error", "capability schema is invalid", false)
	}
	for _, key := range schema.Required {
		if _, ok := params[key]; !ok {
			return actionError("invalid_arguments", "missing required parameter: "+key, false)
		}
	}
	for key, value := range params {
		property, ok := schema.Properties[key]
		if !ok {
			return actionError("invalid_arguments", "unknown parameter: "+key, false)
		}
		if err := validateProperty(key, value, property); err != nil {
			return err
		}
	}
	return nil
}

func validateProperty(name string, value any, schema map[string]any) error {
	typ, _ := schema["type"].(string)
	valid := true
	switch typ {
	case "string":
		_, valid = value.(string)
	case "boolean":
		_, valid = value.(bool)
	case "number":
		_, valid = value.(json.Number)
	case "integer":
		n, ok := value.(json.Number)
		if valid = ok; ok {
			_, err := strconv.ParseInt(string(n), 10, 64)
			valid = err == nil
		}
	case "array":
		items, ok := value.([]any)
		valid = ok
		if ok {
			if minimum, ok := schema["minItems"].(float64); ok && len(items) < int(minimum) {
				valid = false
			}
			if itemSchema, ok := schema["items"].(map[string]any); ok {
				for _, item := range items {
					if validateProperty(name+"[]", item, itemSchema) != nil {
						valid = false
					}
				}
			}
		}
	case "object":
		_, valid = value.(map[string]any)
	}
	if !valid {
		return actionError("invalid_arguments", "parameter "+name+" has the wrong type", false)
	}
	if n, ok := value.(json.Number); ok {
		if minimum, ok := schema["minimum"].(float64); ok {
			parsed, err := strconv.ParseFloat(string(n), 64)
			if err != nil || parsed < minimum {
				return actionError("invalid_arguments", "parameter "+name+" is below its minimum", false)
			}
		}
	}
	if choices, ok := schema["enum"].([]any); ok {
		matched := false
		for _, choice := range choices {
			if value == choice {
				matched = true
			}
		}
		if !matched {
			return actionError("invalid_arguments", "parameter "+name+" is not an allowed value", false)
		}
	}
	if pattern, ok := schema["pattern"].(string); ok {
		text, _ := value.(string)
		if matched, err := regexp.MatchString(pattern, text); err != nil || !matched {
			return actionError("invalid_arguments", "parameter "+name+" does not match its required pattern", false)
		}
	}
	return nil
}

// Capabilities describes only the native surface. Host-managed entries are
// declarations: Execute returns host_action_required for those actions.
func Capabilities() []Capability {
	locator := map[string]any{
		"selector": map[string]any{"type": "string"}, "ref": map[string]any{"type": "string", "pattern": "^@"},
		"role": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"},
	}
	props := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range locator {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	str := map[string]any{"type": "string"}
	boolean := map[string]any{"type": "boolean"}
	number := map[string]any{"type": "number"}
	integer := map[string]any{"type": "integer", "minimum": 0}
	cap := func(name, description string, readOnly bool, properties map[string]any, required ...string) Capability {
		s := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(required) != 0 {
			s["required"] = required
		}
		data, _ := json.Marshal(s)
		return Capability{Name: name, Description: description, ReadOnly: readOnly, InputSchema: data}
	}
	loc := func(name, description string, readOnly bool) Capability {
		return cap(name, description, readOnly, props(nil))
	}
	empty := map[string]any{}
	return []Capability{
		cap("snapshot", "Accessibility snapshot with stable element refs.", true, map[string]any{"interactive": boolean, "compact": boolean, "includeText": boolean, "depth": integer, "maxNodes": map[string]any{"type": "integer", "minimum": 1}}),
		cap("eval", "Evaluate JavaScript in the selected tab.", false, map[string]any{"script": str}, "script"),
		loc("click", "Click an element by locator.", false), loc("dblclick", "Double-click an element by locator.", false),
		loc("hover", "Hover an element by locator.", false), loc("focus", "Focus an element by locator.", false),
		loc("check", "Check an element by locator.", false), loc("uncheck", "Uncheck an element by locator.", false),
		cap("fill", "Clear and fill an element; value is an alias for text.", false, props(map[string]any{"text": str, "value": str})),
		cap("type", "Insert text into an element; value is an alias for text.", false, props(map[string]any{"text": str, "value": str})),
		cap("press", "Press and release a keyboard key.", false, map[string]any{"key": str}, "key"),
		cap("keydown", "Send a native key-down event.", false, map[string]any{"key": str}, "key"),
		cap("keyup", "Send a native key-up event.", false, map[string]any{"key": str}, "key"),
		cap("keyboard.insert", "Insert text at the focused element.", false, map[string]any{"text": str}, "text"),
		cap("select", "Select an option by value or text.", false, props(map[string]any{"value": str, "text": str})),
		cap("upload", "Set files on a file input.", false, props(map[string]any{"files": map[string]any{"type": "array", "items": str, "minItems": 1}}), "files"),
		cap("wait", "Wait for a locator state or duration.", true, props(map[string]any{"timeoutMs": integer, "state": map[string]any{"type": "string", "enum": []string{"visible", "hidden"}}})),
		cap("wait.url", "Wait for the top-frame URL to match an exact value, glob, or substring.", true, map[string]any{"pattern": str, "exact": boolean}, "pattern"),
		cap("wait.function", "Wait for a JavaScript expression (including a Promise) to become truthy.", true, map[string]any{"script": str}, "script"),
		cap("wait.loadState", "Wait for the current document load state; networkidle is not supported.", true, map[string]any{"state": map[string]any{"type": "string", "enum": []string{"domcontentloaded", "load"}}}, "state"),
		cap("wait.popup", "Wait for an unconsumed popup opened by the selected tab.", true, map[string]any{"urlPattern": str, "titlePattern": str, "sinceToken": integer}),
		cap("dialog.status", "Read the currently open JavaScript dialog.", true, empty),
		cap("dialog.accept", "Accept the open JavaScript dialog, optionally supplying prompt text.", false, map[string]any{"promptText": str}),
		cap("dialog.dismiss", "Dismiss the open JavaScript dialog.", false, empty),
		cap("get", "Get page or element information.", true, props(map[string]any{"kind": str, "name": str}), "kind"),
		cap("is", "Test element state.", true, props(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"visible", "enabled", "checked"}}}), "kind"),
		cap("find", "Find snapshot elements by role, name, or text.", true, props(map[string]any{"exact": boolean})),
		cap("scroll", "Scroll the selected frame.", false, map[string]any{"selector": str, "ref": locator["ref"], "deltaX": number, "deltaY": number, "x": number, "y": number, "viewportY": number}),
		loc("scrollintoview", "Scroll an element into view.", false),
		cap("frame", "Select a frame by CSS selector or return to the main frame.", false, map[string]any{"selector": str, "main": boolean}),
		cap("console", "Inspect or clear console messages.", true, map[string]any{"clear": boolean}),
		cap("errors", "Inspect or clear page errors.", true, map[string]any{"clear": boolean}),
		cap("network.requests", "List or clear captured network requests.", true, map[string]any{"clear": boolean, "filter": str}),
		cap("network.response", "Read an observed response body.", true, map[string]any{"requestId": str}, "requestId"),
		cap("download.wait", "Wait for a browser-scoped download to finish.", true, map[string]any{"guid": str}),
		cap("download.list", "List browser-scoped download metadata; file contents are never returned.", true, empty),
		cap("cookies.get", "Read Chromium cookies.", true, map[string]any{"name": str}),
		cap("cookies.set", "Set a Chromium cookie.", false, map[string]any{"name": str, "value": str, "url": str, "domain": str, "path": str, "expires": number, "secure": boolean, "httpOnly": boolean, "sameSite": str}, "name", "value"),
		cap("cookies.clear", "Clear Chromium cookies.", false, map[string]any{"name": str}),
		cap("storage.get", "Read local or session storage.", true, map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"local", "session"}}, "key": str}, "kind"),
		cap("storage.set", "Set local or session storage.", false, map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"local", "session"}}, "key": str, "value": str}, "kind", "key", "value"),
		cap("storage.clear", "Clear local or session storage.", false, map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"local", "session"}}}, "kind"),
		cap("screenshot", "Capture PNG inline or to a new file.", true, map[string]any{"path": str, "full": boolean}),
		cap("pdf", "Print PDF inline or to a new file.", true, map[string]any{"path": str}),
		cap("navigate", "Host-managed navigation of the selected tab.", false, map[string]any{"url": str}, "url"),
		cap("back", "Host-managed backward navigation.", false, empty), cap("forward", "Host-managed forward navigation.", false, empty), cap("reload", "Host-managed tab reload.", false, empty),
		cap("set.viewport", "Host-managed viewport change.", false, map[string]any{"width": map[string]any{"type": "integer", "minimum": 1}, "height": map[string]any{"type": "integer", "minimum": 1}}, "width", "height"),
		cap("tab.list", "Host-managed tab listing.", true, empty), cap("tab.new", "Host-managed tab creation.", false, map[string]any{"url": str}),
		cap("tab.close", "Host-managed tab closure.", false, map[string]any{"tabId": str}), cap("tab.activate", "Host-managed tab activation.", false, map[string]any{"tabId": str}),
		cap("info", "Host-managed API information.", true, empty), cap("capabilities", "Host-managed capability listing.", true, empty), cap("done", "Host-managed action mode completion.", false, empty),
		cap("quit", "Host-managed graceful browser shutdown; the reply is sent before the control socket closes.", false, empty),
	}
}
