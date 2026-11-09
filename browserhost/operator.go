package browserhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/browser"
)

// OperatorInput is a bounded, person-originated input event for one exact tab.
// It intentionally does not carry a script, selector, filesystem path, or URL.
type OperatorInput struct {
	BrowserID string  `json:"browserId"`
	TabID     string  `json:"tabId"`
	TargetID  string  `json:"targetId"`
	Kind      string  `json:"kind"` // pointer-down, pointer-hover, pointer-move, pointer-up, scroll, text, key
	X         float64 `json:"x,omitempty"`
	Y         float64 `json:"y,omitempty"`
	DeltaY    float64 `json:"deltaY,omitempty"`
	Text      string  `json:"text,omitempty"`
	Key       string  `json:"key,omitempty"`
}

// SendOperatorInputJSON is the version-independent in-process embedding seam
// for SendOperatorInput. It accepts only the closed OperatorInput schema and
// is not registered on the control socket.
func (h *Host) SendOperatorInputJSON(ctx context.Context, raw []byte) error {
	if len(raw) == 0 || len(raw) > 8192 {
		return errors.New("operator input document is empty or too large")
	}
	var event OperatorInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return fmt.Errorf("invalid operator input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("operator input must contain exactly one JSON document")
	}
	return h.SendOperatorInput(ctx, event)
}

// SendOperatorInput delivers an input from a trusted, locally authenticated
// operator surface. This is deliberately an in-process API, not a socket
// action. Embedders must authenticate remote surfaces before calling it; merely
// possessing the ordinary browser control socket is not sufficient authority.
// It leaves an automation pause in place and never retries a rejected action.
func (h *Host) SendOperatorInput(ctx context.Context, event OperatorInput) error {
	if h == nil || h.bridge == nil {
		return errors.New("browser host is unavailable")
	}
	if err := validateOperatorInput(event); err != nil {
		return err
	}
	target := action.Target{BrowserID: event.BrowserID, TabID: event.TabID, TargetID: event.TargetID}
	return h.bridge.ManualTarget(ctx, target, func(sess *browser.Session) error {
		switch event.Kind {
		case "pointer-down":
			return sess.MouseDown(event.X, event.Y)
		case "pointer-hover":
			return sess.MouseHover(event.X, event.Y)
		case "pointer-move":
			return sess.MouseMove(event.X, event.Y)
		case "pointer-up":
			return sess.MouseUp(event.X, event.Y)
		case "scroll":
			return sess.ScrollContext(ctx, event.X, event.Y, event.DeltaY)
		case "text":
			return sess.InsertText(event.Text)
		case "key":
			return sess.KeyPress(event.Key)
		default:
			return errors.New("unsupported operator input")
		}
	})
}

// OperatorAction lets a trusted surface use browser chrome while automation is
// paused. This closed set has no scripts, file access or arbitrary page actions.
// It is intentionally absent from /v1/actions and requires a private capability
// when called through the shared host's separate management socket.
func (h *Host) OperatorAction(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if h == nil || h.bridge == nil {
		return nil, errors.New("browser host is unavailable")
	}
	var req action.Request
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 8192 || d.Decode(&req) != nil {
		return nil, errors.New("invalid operator action")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, errors.New("operator action must contain one request")
	}
	if req.Target.BrowserID != h.bridge.ID {
		return nil, errors.New("operator action belongs to another view")
	}
	switch req.Action {
	case "navigate", "back", "forward", "reload", "tab.new", "tab.activate", "tab.close", "set.viewport", "quit", "interaction.resume", "interaction.pause":
	default:
		return nil, errors.New("unsupported operator chrome action")
	}
	return h.bridge.ExecuteOperator(ctx, raw)
}

func validateOperatorInput(event OperatorInput) error {
	if event.BrowserID == "" || event.TabID == "" || event.TargetID == "" {
		return errors.New("operator input requires an exact browser and tab target")
	}
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	switch event.Kind {
	case "pointer-down", "pointer-hover", "pointer-move", "pointer-up", "scroll":
		if !finite(event.X) || !finite(event.Y) || event.X < 0 || event.Y < 0 || event.X > 32768 || event.Y > 32768 {
			return errors.New("operator pointer coordinates are invalid")
		}
		if event.Kind == "scroll" && (!finite(event.DeltaY) || math.Abs(event.DeltaY) > 32768) {
			return errors.New("operator scroll distance is invalid")
		}
	case "text":
		if len(event.Text) == 0 || len(event.Text) > 4096 {
			return errors.New("operator text must contain 1 to 4096 bytes")
		}
	case "key":
		switch event.Key {
		case "Enter", "Backspace", "Tab", "Escape", "Delete", "Home", "End", "PageUp", "PageDown", "ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown":
		default:
			return fmt.Errorf("unsupported operator key %q", event.Key)
		}
	default:
		return errors.New("unsupported operator input")
	}
	return nil
}
