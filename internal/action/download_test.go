package action

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseDownloadCommands(t *testing.T) {
	tests := []struct {
		args      []string
		action    string
		guid      string
		timeoutMS int
	}{
		{[]string{"download", "list"}, "download.list", "", 0},
		{[]string{"download", "wait"}, "download.wait", "", 0},
		{[]string{"download", "wait", "guid-1"}, "download.wait", "guid-1", 0},
		{[]string{"download", "wait", "guid-1", "--timeout-ms", "250"}, "download.wait", "guid-1", 250},
		{[]string{"download", "wait", "--timeout-ms", "250"}, "download.wait", "", 250},
	}
	for _, tt := range tests {
		req, err := ParseCommand(tt.args)
		if err != nil {
			t.Fatalf("ParseCommand(%v): %v", tt.args, err)
		}
		if req.Action != tt.action || req.TimeoutMS != tt.timeoutMS {
			t.Fatalf("ParseCommand(%v) = action %q timeout %d", tt.args, req.Action, req.TimeoutMS)
		}
		var params struct {
			GUID string `json:"guid"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.GUID != tt.guid {
			t.Fatalf("ParseCommand(%v) params = %s, %v", tt.args, req.Params, err)
		}
	}
	for _, args := range [][]string{
		{"download"}, {"download", "list", "extra"},
		{"download", "wait", "a", "b"}, {"download", "wait", "--timeout-ms", "0"},
	} {
		_, err := ParseCommand(args)
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != "invalid_arguments" {
			t.Errorf("ParseCommand(%v) error = %v", args, err)
		}
	}
}

func TestDownloadCapabilities(t *testing.T) {
	wanted := map[string]bool{"download.wait": false, "download.list": false}
	for _, capability := range Capabilities() {
		if _, ok := wanted[capability.Name]; !ok {
			continue
		}
		if !capability.ReadOnly {
			t.Errorf("%s is not read-only", capability.Name)
		}
		var schema struct {
			Type                 string                     `json:"type"`
			Properties           map[string]json.RawMessage `json:"properties"`
			AdditionalProperties bool                       `json:"additionalProperties"`
		}
		if err := json.Unmarshal(capability.InputSchema, &schema); err != nil || schema.Type != "object" || schema.AdditionalProperties {
			t.Errorf("%s schema = %s, %v", capability.Name, capability.InputSchema, err)
		}
		if capability.Name == "download.wait" {
			if _, ok := schema.Properties["guid"]; !ok {
				t.Error("download.wait schema has no guid")
			}
		}
		wanted[capability.Name] = true
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("missing capability %s", name)
		}
	}
}
