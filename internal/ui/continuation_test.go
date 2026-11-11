package ui

import (
	"strings"
	"testing"

	"github.com/getan/kitty-browse/internal/host"
	"github.com/getan/kitty-browse/internal/term"
)

func pausedFooter() *Model {
	m := New("https://example.test")
	m.Interaction = host.InteractionState{Paused: true, InstanceEpoch: "epoch", PauseGeneration: 5, ContinuationVersion: 1}
	return m
}

func TestFooterButtonsBindGenerationAndDimensions(t *testing.T) {
	for _, private := range []bool{false, true} {
		m := pausedFooter()
		m.Private = private
		if !strings.HasPrefix(m.Footer(false), "[Resume automation] [Keep paused]") {
			t.Fatal("rendered buttons differ from their hit regions")
		}
		press := term.MouseEvent{Row: 24, Col: 7, Button: 0, Press: true}
		release := press
		release.Press = false
		if got := m.HandleFooterMouse(80, 24, press); got.Kind != ActionNone {
			t.Fatal("press dispatched a recovery")
		}
		if got := m.HandleFooterMouse(80, 24, release); got.Kind != ActionResumeAutomation || got.PauseGeneration != 5 || got.InstanceEpoch != "epoch" {
			t.Fatalf("release: %+v", got)
		}
		m.HandleFooterMouse(80, 24, press)
		m.Interaction.PauseGeneration++
		if got := m.HandleFooterMouse(80, 24, release); got.Kind != ActionNone {
			t.Fatal("new pause inherited old mouse press")
		}
		m.HandleFooterMouse(80, 24, press)
		if got := m.HandleFooterMouse(81, 24, release); got.Kind != ActionNone {
			t.Fatal("resize did not cancel click")
		}
		m.HandleFooterMouse(80, 24, press)
		m.CancelFooterPress()
		if got := m.HandleFooterMouse(80, 24, release); got.Kind != ActionNone {
			t.Fatal("A-B-A resize revived a click")
		}
	}
}

func TestFooterKeyboardAndBusyFeedback(t *testing.T) {
	m := pausedFooter()
	if _, handled := m.HandleFooterKey(term.Event{Kind: term.KeyTab}); !handled || !strings.Contains(m.Footer(false), "{Resume automation}") {
		t.Fatal("Tab did not focus the visible recovery control")
	}
	if got, handled := m.HandleFooterKey(term.Event{Kind: term.KeyEnter}); !handled || got.Kind != ActionResumeAutomation {
		t.Fatal("Enter did not activate the focused button")
	}
	m.HandleFooterKey(term.Event{Kind: term.KeyTab})
	m.Interaction.PauseGeneration++
	if got, handled := m.HandleFooterKey(term.Event{Kind: term.KeyText, Rune: ' '}); !handled || got.Kind != ActionNone {
		t.Fatal("new generation inherited keyboard focus")
	}
	m.ResumeBusy = true
	if !strings.Contains(m.Footer(false), "Resuming automation") {
		t.Fatal("in-flight recovery has no visible feedback")
	}
	if got, _ := m.HandleFooterKey(term.Event{Kind: term.KeyCtrlG}); got.Kind != ActionNone {
		t.Fatal("busy Ctrl+G submitted another recovery")
	}
	m.ResumeBusy, m.ResumeUnknown = false, true
	if !strings.Contains(m.Footer(false), "outcome unknown") {
		t.Fatal("uncertain result was hidden by pause help")
	}
}
