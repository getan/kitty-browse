package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestAutomationErrorContract(t *testing.T) {
	err := autoErr("stale_ref", "element %s", "e1")
	var ae *AutomationError
	if !errors.As(err, &ae) || ae.ErrorCode() != "stale_ref" || ae.Error() != "stale_ref: element e1" {
		t.Fatalf("unexpected error contract: %#v", err)
	}
}

func TestAutomateRejectsNonObjectParams(t *testing.T) {
	s := &Session{ctx: context.Background()}
	for _, raw := range []string{`null`, `[]`, `"x"`, `{`} {
		_, err := s.Automate(context.Background(), "snapshot", json.RawMessage(raw))
		var ae *AutomationError
		if !errors.As(err, &ae) || ae.Code != "invalid_params" {
			t.Errorf("params %s: got %v", raw, err)
		}
	}
}

func TestAutomationStateIsPerSession(t *testing.T) {
	a, b := &Session{}, &Session{}
	sa, sb := a.automationState(), b.automationState()
	if sa == sb {
		t.Fatal("automation state leaked across sessions")
	}
	sa.refs["e1"] = automationRef{Backend: 42, Loader: "a"}
	if _, ok := sb.refs["e1"]; ok {
		t.Fatal("reference leaked across sessions")
	}
}

func TestLocatorAndKeyHelpers(t *testing.T) {
	p := map[string]json.RawMessage{"exact": json.RawMessage(`true`), "n": json.RawMessage(`7`)}
	if !boolParam(p, "exact", false) || intParam(p, "n", 0) != 7 {
		t.Fatal("parameter helpers")
	}
	key, code, number := keyInfo("Enter")
	if key != "Enter" || code != "Enter" || number != 13 {
		t.Fatalf("bad Enter mapping: %q %q %d", key, code, number)
	}
	k, err := parseKey("Ctrl+Shift+A")
	if err != nil || k.key != "A" || k.code != "KeyA" || k.vk != 65 || k.modifiers != 10 || k.text != "" {
		t.Fatalf("bad chord mapping: %#v %v", k, err)
	}
	k, err = parseKey("Cmd+Space")
	if err != nil || k.key != " " || k.code != "Space" || k.modifiers != 4 {
		t.Fatalf("bad Meta chord mapping: %#v %v", k, err)
	}
	for _, bad := range []string{"Control+NoSuchKey", "Hyper+A", "中文"} {
		_, err = parseKey(bad)
		var ae *AutomationError
		if !errors.As(err, &ae) || ae.Code != "invalid_arguments" {
			t.Errorf("unsupported key %q: %v", bad, err)
		}
	}
}

func TestAutomationSafetyHelpers(t *testing.T) {
	if snapshotValueAllowed("textbox", "sensitive", false, false) {
		t.Fatal("textbox value must fail closed when DOM type confirmation fails")
	}
	if snapshotValueAllowed("searchbox", "sensitive", false, false) {
		t.Fatal("text-input roles must fail closed when DOM type confirmation fails")
	}
	if snapshotValueAllowed("textbox", "sensitive", true, true) {
		t.Fatal("password value must never be emitted")
	}
	if !snapshotValueAllowed("textbox", "ordinary", true, false) {
		t.Fatal("confirmed ordinary textbox value should be emitted")
	}
	for _, role := range []string{"treeitem", "menuitemcheckbox", "menuitemradio", "iframe"} {
		if !interactiveRole(role) {
			t.Errorf("%s should be an interactive snapshot role", role)
		}
	}
	if _, err := boundedTextResult(strings.Repeat("x", maxAutomationTextResult+1)); automationErrorCode(err) != "result_too_large" {
		t.Fatalf("oversized result: %v", err)
	}
	if got, err := requiredStringParam(map[string]json.RawMessage{"text": json.RawMessage(`""`)}, "text", "value"); err != nil || got != "" {
		t.Fatalf("explicit empty string must be accepted: %q %v", got, err)
	}
	if _, err := requiredStringParam(map[string]json.RawMessage{}, "text", "value"); automationErrorCode(err) != "invalid_params" {
		t.Fatalf("missing text must fail: %v", err)
	}
}

func automationErrorCode(err error) string {
	var ae *AutomationError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}
