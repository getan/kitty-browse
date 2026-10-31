package ui

import (
	"strings"
	"testing"

	"github.com/getan/kitty-browse/internal/host"
)

func TestInteractionFooter(t *testing.T) {
	for _, tc := range []struct {
		state host.InteractionState
		want  string
	}{
		{host.InteractionState{}, "Manual input ready"},
		{host.InteractionState{Busy: true, Action: "click"}, "Agent operating: click"},
		{host.InteractionState{Busy: true, Action: "snapshot", ReadOnly: true}, "Agent observing: snapshot"},
		{host.InteractionState{Completed: true, Action: "fill"}, "Agent finished: fill"},
		{host.InteractionState{Paused: true, Reason: "captcha"}, "Automation paused: captcha"},
		{host.InteractionState{Busy: true, Action: "click", InputConflict: true}, "input ignored"},
		{host.InteractionState{Busy: true, Action: "click", Paused: true}, "please wait"},
	} {
		m := New("https://example.com")
		m.Interaction = tc.state
		if got := m.Footer(false); !strings.Contains(got, tc.want) {
			t.Fatalf("%+v: %q, want %q", tc.state, got, tc.want)
		}
	}
}
