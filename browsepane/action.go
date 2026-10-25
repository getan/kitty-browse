package browsepane

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/control"
)

const maxActionRequestSize = 1 << 20

// runAction sends one native, typed action to an already-running kitty-browse
// process. It deliberately has no browser-launch fallback: the socket names the
// browser instance whose live tabs may be operated on.
func runAction(args []string, in io.Reader, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("action", flag.ContinueOnError)
	fs.SetOutput(errOut)
	socket := fs.String("socket", os.Getenv("KITTY_BROWSE_SOCKET"), "running browser's -control-socket path")
	tab := fs.String("tab", "", "opaque tab ID or 1-based displayed tab number")
	target := fs.String("target", "", "Chromium target ID")
	browserID := fs.String("browser", "", "browser ID")
	follow := fs.Bool("follow", false, "activate the selected tab before executing the action")
	timeout := fs.Duration("timeout", 30*time.Second, "request timeout (maximum 120s)")
	_ = fs.Bool("json", false, "print the JSON response (the default; retained for compatibility)")
	requestJSON := fs.String("request-json", "", "typed request JSON, or - to read it from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *socket == "" {
		return fmt.Errorf("specify -socket or KITTY_BROWSE_SOCKET; no browser is started implicitly")
	}
	if *timeout < time.Millisecond || *timeout > 120*time.Second {
		return clientError("invalid_arguments", "timeout must be between 1ms and 120s")
	}

	visited := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if visited["tab"] && visited["target"] {
		return clientError("invalid_arguments", "-tab and -target cannot be used together")
	}

	var req action.Request
	var err error
	if visited["request-json"] {
		if fs.NArg() != 0 {
			return clientError("invalid_arguments", "-request-json cannot be combined with a native command")
		}
		raw, readErr := readActionRequest(*requestJSON, in)
		if readErr != nil {
			return readErr
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&req); err != nil {
			return clientError("invalid_arguments", "request-json is not a valid typed request")
		}
		var extra any
		if err = decoder.Decode(&extra); err != io.EOF {
			return clientError("invalid_arguments", "request-json must contain one object")
		}
		if strings.TrimSpace(req.Action) == "" {
			return clientError("invalid_arguments", "request-json action is required")
		}
	} else {
		if fs.NArg() == 0 {
			return clientError("invalid_arguments", "missing native command (use -- before the command)")
		}
		req, err = action.ParseCommand(fs.Args())
		if err != nil {
			return err
		}
	}

	if req.ID == "" {
		req.ID = newActionRequestID()
	}
	if visited["timeout"] || req.TimeoutMS == 0 {
		req.TimeoutMS = int(*timeout / time.Millisecond)
	}
	requestTimeout, err := action.RequestTimeout(req)
	if err != nil {
		return err
	}

	// Only explicitly supplied selectors replace a selector from raw JSON.
	// Selecting a tab clears a raw target ID (and vice versa), so SelectTarget
	// cannot accidentally combine IDs which the caller did not intend to pair.
	if visited["tab"] {
		req.Target.TabID = *tab
		req.Target.TargetID = ""
	}
	if visited["target"] {
		req.Target.TargetID = *target
		req.Target.TabID = ""
	}
	if visited["browser"] {
		req.Target.BrowserID = *browserID
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	infoRaw, err := control.Info(ctx, *socket)
	if err != nil {
		return writeClientFailure(out, req, err)
	}
	var info action.Info
	if err := json.Unmarshal(infoRaw, &info); err != nil {
		return writeClientFailure(out, req, errors.New("control API returned invalid browser information"))
	}

	if visited["tab"] {
		resolved, resolveErr := displayedTabID(info, *tab)
		if resolveErr != nil {
			return writeActionFailure(out, req, info, action.TabInfo{}, resolveErr)
		}
		req.Target.TabID = resolved
	}

	if err := resolveHostTabID(info, &req); err != nil {
		return writeActionFailure(out, req, info, action.TabInfo{}, err)
	}
	selected, selectErr := action.SelectTarget(info, req.Target)
	if selectErr != nil {
		// These operations can be meaningful before the first tab exists. An
		// explicit selector is never exempt from validation.
		browserMatches := req.Target.BrowserID == "" || req.Target.BrowserID == info.BrowserID
		if !(targetOptional(req.Action) && req.Target.TabID == "" && req.Target.TargetID == "" &&
			browserMatches && len(info.Tabs) == 0) {
			return writeActionFailure(out, req, info, action.TabInfo{}, selectErr)
		}
	} else {
		// Pin all three IDs after discovery. The server therefore cannot apply
		// the request to a different tab if the active tab changes in flight.
		req.Target = action.Target{BrowserID: info.BrowserID, TabID: selected.ID, TargetID: selected.TargetID}
	}
	if req.Target.BrowserID == "" {
		req.Target.BrowserID = info.BrowserID
	}

	if *follow {
		if selected.ID == "" {
			return writeActionFailure(out, req, info, selected,
				clientError("target_not_found", "follow requires an open target"))
		}
		params, _ := json.Marshal(struct {
			TabID string `json:"tabId"`
		}{selected.ID})
		activate := action.Request{
			ID:        newActionRequestID(),
			Action:    "tab.activate",
			Params:    params,
			Target:    req.Target,
			TimeoutMS: req.TimeoutMS,
		}
		response, callErr := callAction(ctx, *socket, activate)
		if callErr != nil {
			return writeClientFailure(out, req, callErr)
		}
		if !response.parsed.OK {
			if err := writeJSON(out, response.raw); err != nil {
				return err
			}
			return responseError(response.parsed)
		}
	}

	response, err := callAction(ctx, *socket, req)
	if err != nil {
		return writeClientFailure(out, req, err)
	}
	if err := writeJSON(out, response.raw); err != nil {
		return err
	}
	if !response.parsed.OK {
		return responseError(response.parsed)
	}
	return nil
}

// runInfo prints the running process's /v1/info document without wrapping or
// transforming it.
func runInfo(args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	fs.SetOutput(errOut)
	socket := fs.String("socket", os.Getenv("KITTY_BROWSE_SOCKET"), "running browser's -control-socket path")
	timeout := fs.Duration("timeout", 30*time.Second, "request timeout (maximum 120s)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: kitty-browse info [-socket PATH] [-timeout DURATION]")
	}
	if *socket == "" {
		return fmt.Errorf("specify -socket or KITTY_BROWSE_SOCKET; no browser is started implicitly")
	}
	if *timeout < time.Millisecond || *timeout > 120*time.Second {
		return clientError("invalid_arguments", "timeout must be between 1ms and 120s")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	raw, err := control.Info(ctx, *socket)
	if err != nil {
		return err
	}
	return writeJSON(out, raw)
}

func readActionRequest(value string, in io.Reader) ([]byte, error) {
	if value == "" {
		return nil, clientError("invalid_arguments", "-request-json requires JSON or -")
	}
	var raw []byte
	var err error
	if value == "-" {
		if in == nil {
			return nil, clientError("invalid_arguments", "request-json stdin is unavailable")
		}
		raw, err = io.ReadAll(io.LimitReader(in, maxActionRequestSize+1))
		if err != nil {
			return nil, fmt.Errorf("read request-json: %w", err)
		}
	} else {
		raw = []byte(value)
	}
	if len(raw) > maxActionRequestSize {
		return nil, clientError("request_too_large", "request-json exceeds 1 MiB")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' || !json.Valid(raw) {
		return nil, clientError("invalid_arguments", "request-json must be a JSON object")
	}
	return raw, nil
}

func targetOptional(name string) bool {
	switch name {
	case "info", "capabilities", "tab.new", "tab.list":
		return true
	default:
		return false
	}
}

type actionCallResponse struct {
	raw    json.RawMessage
	parsed action.Response
}

func callAction(ctx context.Context, socket string, req action.Request) (actionCallResponse, error) {
	rawRequest, err := json.Marshal(req)
	if err != nil {
		return actionCallResponse{}, fmt.Errorf("encode action request: %w", err)
	}
	rawResponse, err := control.Action(ctx, socket, rawRequest)
	if err != nil {
		return actionCallResponse{}, err
	}
	var response action.Response
	if err := json.Unmarshal(rawResponse, &response); err != nil {
		return actionCallResponse{}, errors.New("control API returned an invalid action response")
	}
	return actionCallResponse{raw: rawResponse, parsed: response}, nil
}

func responseError(response action.Response) error {
	if response.Error != nil {
		return response.Error
	}
	return clientError("invalid_response", "action response reported failure without an error")
}

func writeActionFailure(out io.Writer, req action.Request, info action.Info, target action.TabInfo, err error) error {
	response := action.Respond(req, info, target, nil, err)
	if writeErr := json.NewEncoder(out).Encode(response); writeErr != nil {
		return writeErr
	}
	return response.Error
}

func writeClientFailure(out io.Writer, req action.Request, err error) error {
	apiErr := clientError("internal_error", "control API request failed")
	if errors.Is(err, context.DeadlineExceeded) {
		apiErr = clientError("timeout", "request timed out")
		apiErr.Retryable = true
	} else if errors.Is(err, context.Canceled) {
		apiErr = clientError("canceled", "request canceled")
	}
	return writeActionFailure(out, req, action.Info{}, action.TabInfo{}, apiErr)
}

func writeJSON(out io.Writer, raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("control API returned an empty JSON document")
	}
	if _, err := out.Write(raw); err != nil {
		return err
	}
	if raw[len(raw)-1] != '\n' {
		_, err := io.WriteString(out, "\n")
		return err
	}
	return nil
}

func newActionRequestID() string {
	var random [12]byte
	if _, err := rand.Read(random[:]); err == nil {
		return "kb-" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("kb-%d", time.Now().UnixNano())
}
