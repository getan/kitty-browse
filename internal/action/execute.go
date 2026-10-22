package action

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/getan/kitty-browse/internal/browser"
	"github.com/getan/kitty-browse/internal/screenshot"
)

const defaultRequestTimeout = 30 * time.Second
const maxInlineArtifact = 10 << 20 // Fits base64 plus envelope under the 16 MiB control response cap.

// RequestTimeout validates and converts the public millisecond timeout. A zero
// value means the stable 30-second default.
func RequestTimeout(req Request) (time.Duration, error) {
	if req.TimeoutMS == 0 {
		return defaultRequestTimeout, nil
	}
	if req.TimeoutMS < 1 || req.TimeoutMS > 120000 {
		return 0, invalid("timeoutMs must be between 1 and 120000")
	}
	return time.Duration(req.TimeoutMS) * time.Millisecond, nil
}

// Execute dispatches a request only to native Session/CDP implementations.
// Browser lifecycle operations are returned to the host rather than mutating
// Session state behind the UI's back.
func Execute(ctx context.Context, req Request, sess *browser.Session) (json.RawMessage, error) {
	if err := ValidateRequest(req); err != nil {
		return nil, err
	}
	switch req.Action {
	case "navigate", "back", "forward", "reload", "tab.list", "tab.new", "tab.close", "tab.activate", "set.viewport", "info", "capabilities", "done":
		return nil, actionError("host_action_required", req.Action+" is managed by kitty-browse", false)
	}
	knownAutomation := automationAction(req.Action)
	knownInspection := inspectionAction(req.Action)
	if req.Action != "screenshot" && req.Action != "pdf" && !knownAutomation && !knownInspection && !isDialogAction(req.Action) && !isNativeWaitAction(req.Action) {
		return nil, unsupported(req.Action)
	}
	if sess == nil {
		return nil, actionError("target_not_found", "selected tab is not available", false)
	}
	timeout, _ := RequestTimeout(req)
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()
	if req.Action == "screenshot" {
		return executeScreenshot(ctx, req.Params, sess)
	}
	if req.Action == "pdf" {
		return executePDF(ctx, req.Params, sess)
	}
	if req.Action == "download.wait" || req.Action == "download.list" {
		return executeDownload(ctx, req.Action, req.Params, sess)
	}
	if isDialogAction(req.Action) {
		return executeDialog(ctx, req.Action, req.Params, sess)
	}
	if isNativeWaitAction(req.Action) {
		return executeNativeWait(ctx, req.Action, req.Params, sess)
	}

	if knownAutomation {
		return sess.Automate(ctx, req.Action, req.Params)
	}
	if knownInspection {
		return sess.Inspect(ctx, req.Action, req.Params)
	}
	return nil, unsupported(req.Action)
}

func automationAction(action string) bool {
	switch action {
	case "snapshot", "eval", "click", "dblclick", "hover", "focus", "check", "uncheck", "fill", "type", "press", "keydown", "keyup", "keyboard.insert", "select", "get", "is", "find", "wait", "scroll", "scrollintoview", "frame", "upload":
		return true
	default:
		return false
	}
}

func isDialogAction(action string) bool {
	return action == "dialog.status" || action == "dialog.accept" || action == "dialog.dismiss"
}

func isNativeWaitAction(action string) bool {
	return action == "wait.url" || action == "wait.function" || action == "wait.loadState" || action == "wait.popup"
}

func executeDownload(ctx context.Context, action string, raw json.RawMessage, sess *browser.Session) (json.RawMessage, error) {
	switch action {
	case "download.wait":
		var p struct {
			GUID string `json:"guid"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		result, err := sess.WaitDownload(ctx, p.GUID)
		if err != nil {
			return nil, err
		}
		return marshalResult(result)
	case "download.list":
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		result, err := sess.ListDownloads()
		if err != nil {
			return nil, err
		}
		return marshalResult(result)
	default:
		return nil, unsupported(action)
	}
}

func decodeParams(raw json.RawMessage, value any) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return invalid("invalid parameters: " + err.Error())
	}
	return nil
}

func executeDialog(ctx context.Context, action string, raw json.RawMessage, sess *browser.Session) (json.RawMessage, error) {
	switch action {
	case "dialog.status":
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		status, err := sess.JavaScriptDialogStatus()
		if err != nil {
			return nil, err
		}
		return marshalResult(status)
	case "dialog.accept":
		var p struct {
			PromptText string `json:"promptText"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if err := sess.HandleJavaScriptDialog(ctx, true, p.PromptText); err != nil {
			return nil, err
		}
	case "dialog.dismiss":
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if err := sess.HandleJavaScriptDialog(ctx, false, ""); err != nil {
			return nil, err
		}
	}
	return json.RawMessage(`{"handled":true}`), nil
}

func executeNativeWait(ctx context.Context, action string, raw json.RawMessage, sess *browser.Session) (json.RawMessage, error) {
	switch action {
	case "wait.popup":
		return sess.WaitForPopup(ctx, raw)
	case "wait.url":
		var p struct {
			Pattern string `json:"pattern"`
			Exact   bool   `json:"exact"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		url, err := sess.WaitURL(ctx, p.Pattern, p.Exact)
		if err != nil {
			return nil, err
		}
		return marshalResult(struct {
			URL string `json:"url"`
		}{url})
	case "wait.function":
		var p struct {
			Script string `json:"script"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if err := sess.WaitFunction(ctx, p.Script); err != nil {
			return nil, err
		}
	case "wait.loadState":
		var p struct {
			State string `json:"state"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		if err := sess.WaitLoadState(ctx, p.State); err != nil {
			return nil, err
		}
	}
	return json.RawMessage(`{"matched":true}`), nil
}

func executePDF(ctx context.Context, raw json.RawMessage, sess *browser.Session) (json.RawMessage, error) {
	var p struct {
		Path string `json:"path"`
	}
	if len(bytes.TrimSpace(raw)) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, invalid("invalid PDF parameters: " + err.Error())
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	op, cancel := context.WithTimeout(sess.Context(), defaultRequestTimeout)
	stop := context.AfterFunc(ctx, cancel)
	defer func() { stop(); cancel() }()
	printed, err := chromedp.Call(op, page.PrintToPDF, page.PrintToPDFParams{})
	if err != nil {
		return nil, fmt.Errorf("print PDF: %w", err)
	}
	if len(printed.Data) == 0 {
		return nil, fmt.Errorf("print PDF: browser returned an empty document")
	}
	if p.Path != "" {
		if err := writeNewPrivateFile(p.Path, printed.Data); err != nil {
			return nil, err
		}
		return marshalResult(struct {
			MimeType string `json:"mimeType"`
			Path     string `json:"path"`
			Bytes    int    `json:"bytes"`
		}{"application/pdf", p.Path, len(printed.Data)})
	}
	if len(printed.Data) > maxInlineArtifact {
		return nil, actionError("result_too_large", "PDF is too large for an inline response; provide a new artifact path", false)
	}
	return marshalResult(struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
		Bytes    int    `json:"bytes"`
	}{"application/pdf", base64.StdEncoding.EncodeToString(printed.Data), len(printed.Data)})
}

func writeNewPrivateFile(path string, data []byte) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("save PDF: %w", err)
	}
	complete := false
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("save PDF: %w", closeErr)
		}
		if !complete || err != nil {
			_ = os.Remove(path)
		}
	}()
	var written int
	if written, err = f.Write(data); err != nil {
		return fmt.Errorf("save PDF: %w", err)
	}
	if written != len(data) {
		return fmt.Errorf("save PDF: short write (%d of %d bytes)", written, len(data))
	}
	complete = true
	return nil
}

func inspectionAction(action string) bool {
	switch action {
	case "network.requests", "network.response", "console", "errors", "cookies.get", "cookies.set", "cookies.clear", "storage.get", "storage.set", "storage.clear", "download.wait", "download.list":
		return true
	default:
		return false
	}
}

func executeScreenshot(ctx context.Context, raw json.RawMessage, sess *browser.Session) (json.RawMessage, error) {
	var p struct {
		Path string `json:"path"`
		Full bool   `json:"full"`
	}
	if len(bytes.TrimSpace(raw)) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, invalid("invalid screenshot parameters: " + err.Error())
		}
	}
	data, err := sess.Screenshot(ctx, p.Full)
	if err != nil {
		return nil, err
	}
	if p.Path != "" {
		info, err := screenshot.Save(p.Path, data)
		if err != nil {
			return nil, err
		}
		return marshalResult(struct {
			MimeType string `json:"mimeType"`
			Path     string `json:"path"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			Bytes    int    `json:"bytes"`
		}{"image/png", info.Path, info.Width, info.Height, info.Bytes})
	}
	if len(data) > maxInlineArtifact {
		return nil, actionError("result_too_large", "screenshot is too large for an inline response; provide a new artifact path", false)
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode captured PNG: %w", err)
	}
	return marshalResult(struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
		Bytes    int    `json:"bytes"`
	}{"image/png", base64.StdEncoding.EncodeToString(data), config.Width, config.Height, len(data)})
}

func marshalResult(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	return json.RawMessage(data), err
}

// Respond builds the stable response envelope and maps implementation errors
// without exposing Go or CDP internals.
func Respond(req Request, info Info, target TabInfo, result json.RawMessage, err error) Response {
	backend := info.Backend
	if backend == "" {
		backend = Backend
	}
	response := Response{APIVersion: APIVersion, ID: req.ID, Meta: Meta{BrowserID: info.BrowserID, TabID: target.ID, TargetID: target.TargetID, URL: target.URL, Title: target.Title, Backend: backend}}
	if err == nil {
		response.OK = true
		if len(result) == 0 {
			response.Result = json.RawMessage("null")
		} else {
			response.Result = append(json.RawMessage(nil), result...)
		}
		return response
	}
	response.OK = false
	response.Error = responseError(req, err)
	return response
}

func responseError(req Request, err error) *Error {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		copy := *apiErr
		copy.Details = append(json.RawMessage(nil), apiErr.Details...)
		return executionUncertainty(req.Action, &copy)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return executionUncertainty(req.Action, actionError("timeout", "request timed out", true))
	}
	if errors.Is(err, context.Canceled) {
		return executionUncertainty(req.Action, actionError("canceled", "request canceled", false))
	}
	var automationErr *browser.AutomationError
	if errors.As(err, &automationErr) {
		code := automationErr.Code
		if code == "target_closed" || code == "session_closed" {
			code = "target_not_found"
		}
		if code == "invalid_params" {
			code = "invalid_arguments"
		}
		return executionUncertainty(req.Action, &Error{Code: code, Message: automationErr.Message, Retryable: code == "timeout" || code == "target_busy"})
	}
	return actionError("internal_error", "native action failed", false)
}

func executionUncertainty(action string, apiErr *Error) *Error {
	if apiErr == nil || (apiErr.Code != "timeout" && apiErr.Code != "canceled") || readOnlyAction(action) {
		return apiErr
	}
	apiErr.Retryable = false
	apiErr.Details = json.RawMessage(`{"mayHaveExecuted":true}`)
	return apiErr
}

func readOnlyAction(action string) bool {
	switch action {
	case "", "snapshot", "get", "is", "find", "wait", "wait.url", "wait.function", "wait.loadState", "wait.popup", "dialog.status", "console", "errors", "network.requests", "network.response", "download.wait", "download.list", "cookies.get", "storage.get", "screenshot", "pdf", "tab.list", "info", "capabilities":
		return true
	default:
		return false
	}
}
