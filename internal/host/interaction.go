package host

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// InteractionState contains labels only, never selectors, typed text or URLs.
// It is safe to render in the footer or return with browser metadata.
type InteractionState struct {
	Busy                bool   `json:"busy"`
	Action              string `json:"action,omitempty"`
	ReadOnly            bool   `json:"readOnly,omitempty"`
	Completed           bool   `json:"completed,omitempty"`
	Paused              bool   `json:"paused"`
	Closed              bool   `json:"closed,omitempty"`
	Reason              string `json:"reason,omitempty"`
	InputConflict       bool   `json:"inputConflict,omitempty"`
	BrowserID           string `json:"browserId"`
	InstanceEpoch       string `json:"instanceEpoch"`
	PauseGeneration     uint64 `json:"pauseGeneration"`
	PauseSource         string `json:"pauseSource,omitempty"`
	ResumedGeneration   uint64 `json:"resumedGeneration,omitempty"`
	ContinuationVersion int    `json:"continuationVersion"`
	RetryAfterMS        int64  `json:"retryAfterMs,omitempty"`
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
	source     string
	resumed    uint64
	receipts   map[string]continuationReceipt
	retryAfter time.Time
	closed     bool
}

func (b *Bridge) Interaction() InteractionState {
	b.interaction.mu.Lock()
	defer b.interaction.mu.Unlock()
	s := &b.interaction
	out := InteractionState{Paused: s.paused != "", Closed: s.closed, Reason: s.paused, InputConflict: time.Now().Before(s.conflict)}
	out.BrowserID, out.InstanceEpoch = b.ID, b.ID
	out.PauseGeneration, out.PauseSource, out.ResumedGeneration, out.ContinuationVersion = s.pauseEpoch, s.source, s.resumed, 1
	if remaining := time.Until(s.retryAfter).Milliseconds(); remaining > 0 {
		out.RetryAfterMS = remaining
	}
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
	return b.beginInteractionFor(name, mutating, epoch, false)
}

func (b *Bridge) beginInteractionFor(name string, mutating bool, epoch uint64, operator bool) (func(), error) {
	s := &b.interaction
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ClientError("target_closed", "browser view is closed")
	}
	if mutating && !operator && name != "quit" && time.Now().Before(s.retryAfter) && s.paused == "" {
		s.paused, s.source = "rate_limited", "detector"
		s.pauseEpoch++
	}
	if (s.paused != "" || epoch != s.pauseEpoch) && mutating && !operator && name != "quit" {
		data, _ := json.Marshal(map[string]any{"browser": b.ID, "pause_generation": s.pauseEpoch})
		s.mu.Unlock()
		err := ClientError("browser_paused", "automatic actions are paused; use the local Resume automation control after the required step; do not refresh or retry")
		err.Details = data
		return nil, err
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
	source := "operator"
	if reason == "captcha" || reason == "rate_limited" {
		source = "detector"
	}
	b.pauseFrom(reason, source)
}

func (b *Bridge) pauseFrom(reason, source string) {
	b.interaction.mu.Lock()
	b.interaction.paused = reason
	b.interaction.pauseEpoch++
	b.interaction.source = source
	b.interaction.mu.Unlock()
}

// Resume never replays an interrupted or rejected action.
func (b *Bridge) Resume() {
	b.interaction.mu.Lock()
	b.interaction.resumed = b.interaction.pauseEpoch
	b.interaction.paused = ""
	b.interaction.mu.Unlock()
}

// ContinueRequest is accepted only by the private operator seam. Public
// automation may observe its generation, but cannot submit a decision.
type ContinueRequest struct {
	BrowserID       string    `json:"browserId"`
	InstanceEpoch   string    `json:"instanceEpoch"`
	PauseGeneration uint64    `json:"pauseGeneration"`
	RequestID       string    `json:"requestId"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

type continuationReceipt struct {
	generation uint64
	at         time.Time
	outcome    string
}

func (b *Bridge) ContinuationOutcome(req ContinueRequest) string {
	s := &b.interaction
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || req.BrowserID != b.ID || req.InstanceEpoch != b.ID {
		return "stale"
	}
	if r, ok := s.receipts[req.RequestID]; ok && time.Since(r.at) <= 10*time.Minute && r.generation == req.PauseGeneration {
		return r.outcome
	}
	return "unknown"
}

// ContinueChecked performs a generation compare-and-swap after mutations
// drain. It neither sends page input nor replays a rejected action.
func (b *Bridge) ContinueChecked(ctx context.Context, req ContinueRequest) error {
	if req.BrowserID != b.ID || req.InstanceEpoch != b.ID || len(req.RequestID) < 16 || len(req.RequestID) > 128 {
		return errors.New("browser_instance_changed: invalid continuation identity")
	}
	s := &b.interaction
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("browser_instance_changed: view closed")
	}
	for id, receipt := range s.receipts {
		if time.Since(receipt.at) > 10*time.Minute {
			delete(s.receipts, id)
		}
	}
	if r, ok := s.receipts[req.RequestID]; ok {
		s.mu.Unlock()
		if r.generation != req.PauseGeneration {
			return errors.New("browser_pause_changed: request already used")
		}
		if r.outcome == "resumed" {
			return nil
		}
		return errors.New("browser_continuation_already_submitted: query its outcome; do not replay")
	}
	if s.receipts == nil {
		s.receipts = make(map[string]continuationReceipt)
	}
	if len(s.receipts) >= 64 {
		var oldest string
		var at time.Time
		for id, r := range s.receipts {
			if r.outcome != "pending" && (oldest == "" || r.at.Before(at)) {
				oldest, at = id, r.at
			}
		}
		if oldest == "" {
			s.mu.Unlock()
			return errors.New("browser_busy: continuation capacity reached")
		}
		delete(s.receipts, oldest)
	}
	s.receipts[req.RequestID] = continuationReceipt{generation: req.PauseGeneration, at: time.Now(), outcome: "pending"}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if r := s.receipts[req.RequestID]; r.outcome == "pending" {
			r.outcome = "not_consumed"
			s.receipts[req.RequestID] = r
		}
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return errors.New("browser_instance_changed: view closed")
		}
		if req.ExpiresAt.IsZero() || time.Now().After(req.ExpiresAt) || time.Until(req.ExpiresAt) > 5*time.Minute {
			s.mu.Unlock()
			return errors.New("browser_continuation_expired")
		}
		if s.paused == "" || s.pauseEpoch != req.PauseGeneration {
			s.mu.Unlock()
			return errors.New("browser_pause_changed")
		}
		if time.Now().Before(s.retryAfter) {
			s.mu.Unlock()
			return errors.New("browser_rate_limited: server Retry-After has not elapsed")
		}
		busy := false
		for _, call := range s.active {
			busy = busy || call.mutating
		}
		if !busy {
			if err := ctx.Err(); err != nil {
				s.mu.Unlock()
				return err
			}
			s.receipts[req.RequestID] = continuationReceipt{generation: req.PauseGeneration, at: time.Now(), outcome: "resumed"}
			s.resumed, s.paused = s.pauseEpoch, ""
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return errors.New("browser_busy: continuation timed out")
		case <-tick.C:
		}
	}
}

func (b *Bridge) NoteInputConflict() {
	b.interaction.mu.Lock()
	b.interaction.conflict = time.Now().Add(time.Second)
	b.interaction.mu.Unlock()
}

// Closing a view revokes decisions and receipts in the same critical section
// used by checked recovery. An already accepted request cannot revive it.
func (b *Bridge) CloseInteraction() {
	if b == nil {
		return
	}
	b.interaction.mu.Lock()
	defer b.interaction.mu.Unlock()
	if b.interaction.closed {
		return
	}
	b.interaction.closed, b.interaction.paused = true, ""
	b.interaction.pauseEpoch++
	clear(b.interaction.receipts)
}
