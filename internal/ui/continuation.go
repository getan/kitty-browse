package ui

import "github.com/getan/kitty-browse/internal/term"

// HandleFooterMouse always consumes the footer outside the webpage. A click
// completes only on release over the same button and pause generation.
func (m *Model) HandleFooterMouse(cols, rows int, mouse term.MouseEvent) Action {
	if mouse.Row != rows || mouse.Button != 0 || mouse.Motion || mouse.Wheel != 0 || mouse.WheelX != 0 {
		m.footerPress = Action{}
		return Action{}
	}
	state := m.Interaction
	a := Action{PauseGeneration: state.PauseGeneration, InstanceEpoch: state.InstanceEpoch}
	if state.Paused && state.ContinuationVersion == 1 && !m.ResumeBusy && !m.ResumeUnknown && cols >= 19 {
		switch {
		case mouse.Col >= 1 && mouse.Col <= 19:
			a.Kind = ActionResumeAutomation
		case cols >= 33 && mouse.Col >= 21 && mouse.Col <= 33:
			a.Kind = ActionKeepPaused
		}
	}
	if mouse.Press {
		m.footerPress = a
		m.footerPressCols, m.footerPressRows = cols, rows
		return Action{}
	}
	pressed := m.footerPress
	m.footerPress = Action{}
	if a.Kind != ActionNone && a == pressed && m.footerPressCols == cols && m.footerPressRows == rows {
		return a
	}
	return Action{}
}

func (m *Model) FooterMouseActive() bool { return m.footerPress.Kind != ActionNone }

func (m *Model) CancelFooterPress() { m.footerPress = Action{} }

// Footer focus is bound when Tab is pressed, not reinterpreted for a later
// pause. Enter/Space never become an extra page key after selecting a button.
func (m *Model) HandleFooterKey(ev term.Event) (Action, bool) {
	s := m.Interaction
	if !s.Paused || m.Editing || m.HintMode || m.Overlay != OverlayNone {
		m.footerFocus = 0
		return Action{}, false
	}
	if ev.Kind == term.KeyCtrlG {
		if s.ContinuationVersion != 1 || m.ResumeBusy || m.ResumeUnknown {
			return Action{}, true
		}
		return Action{Kind: ActionResumeAutomation, InstanceEpoch: s.InstanceEpoch, PauseGeneration: s.PauseGeneration}, true
	}
	if ev.Kind == term.KeyTab {
		m.footerFocus = (m.footerFocus + 1) % 3
		m.footerFocusEpoch, m.footerFocusGeneration = s.InstanceEpoch, s.PauseGeneration
		return Action{}, true
	}
	if m.footerFocus == 0 {
		return Action{}, false
	}
	if ev.Kind == term.KeyEscape {
		m.footerFocus = 0
		return Action{}, true
	}
	if ev.Kind != term.KeyEnter && !(ev.Kind == term.KeyText && ev.Rune == ' ') {
		m.footerFocus = 0
		return Action{}, false
	}
	focus := m.footerFocus
	m.footerFocus = 0
	if m.footerFocusEpoch != s.InstanceEpoch || m.footerFocusGeneration != s.PauseGeneration || s.ContinuationVersion != 1 || m.ResumeBusy || m.ResumeUnknown {
		return Action{}, true
	}
	kind := ActionResumeAutomation
	if focus == 2 {
		kind = ActionKeepPaused
	}
	return Action{Kind: kind, InstanceEpoch: s.InstanceEpoch, PauseGeneration: s.PauseGeneration}, true
}
