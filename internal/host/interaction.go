package host

import (
	"sync"
	"time"
)

// InteractionState contains labels only, never selectors, typed text or URLs.
// It is safe to render in the footer or return with browser metadata.
type InteractionState struct {
	Busy          bool   `json:"busy"`
	Action        string `json:"action,omitempty"`
	ReadOnly      bool   `json:"readOnly,omitempty"`
	Completed     bool   `json:"completed,omitempty"`
	Paused        bool   `json:"paused"`
	Reason        string `json:"reason,omitempty"`
	InputConflict bool   `json:"inputConflict,omitempty"`
}

type interactionCall struct {
	action   string
	mutating bool
}
type interactionState struct {
	mu         sync.Mutex
	next       uint64
	active     map[uint64]interactionCall
	paused     string
	pauseEpoch uint64
	last       string
	completed  time.Time
	conflict   time.Time
}

func (b *Bridge) Interaction() InteractionState {
	b.interaction.mu.Lock()
	defer b.interaction.mu.Unlock()
	s := &b.interaction
	out := InteractionState{Paused: s.paused != "", Reason: s.paused, InputConflict: time.Now().Before(s.conflict)}
	var selected uint64
	var mutation bool
	for id, call := range s.active {
		if selected == 0 || call.mutating && !mutation || call.mutating == mutation && id > selected {
			selected, mutation = id, call.mutating
			out.Action, out.ReadOnly = call.action, !call.mutating
		}
	}
	out.Busy = selected != 0
	if !out.Busy && time.Since(s.completed) < time.Second {
		out.Completed, out.Action = true, s.last
	}
	return out
}

func (b *Bridge) beginInteraction(name string, mutating bool) (func(), error) {
	return b.beginInteractionAt(name, mutating, b.pauseEpoch())
}

func (b *Bridge) pauseEpoch() uint64 {
	b.interaction.mu.Lock()
	defer b.interaction.mu.Unlock()
	return b.interaction.pauseEpoch
}

func (b *Bridge) beginInteractionAt(name string, mutating bool, epoch uint64) (func(), error) {
	s := &b.interaction
	s.mu.Lock()
	if (s.paused != "" || epoch != s.pauseEpoch) && mutating && name != "quit" {
		s.mu.Unlock()
		return nil, ClientError("browser_paused", "automatic actions are paused; a person must finish login/verification and explicitly resume; do not refresh or retry")
	}
	if s.active == nil {
		s.active = make(map[uint64]interactionCall)
	}
	s.next++
	id := s.next
	s.active[id] = interactionCall{name, mutating}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.active, id)
		s.last, s.completed = name, time.Now()
		s.mu.Unlock()
	}, nil
}

func (b *Bridge) Pause(reason string) {
	if reason != "captcha" && reason != "rate_limited" {
		reason = "manual"
	}
	b.interaction.mu.Lock()
	b.interaction.paused = reason
	b.interaction.pauseEpoch++
	b.interaction.mu.Unlock()
}

// Resume never replays an interrupted or rejected action.
func (b *Bridge) Resume() {
	b.interaction.mu.Lock()
	b.interaction.paused = ""
	b.interaction.mu.Unlock()
}

func (b *Bridge) NoteInputConflict() {
	b.interaction.mu.Lock()
	b.interaction.conflict = time.Now().Add(time.Second)
	b.interaction.mu.Unlock()
}
