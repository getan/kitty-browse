package host

import (
	"context"
	"errors"

	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/browser"
)

// ManualTarget runs an operator-originated operation on a pinned live tab.
// It is an in-process primitive, not an action or a control-socket endpoint.
// An embedding application must authenticate the operator and must never expose
// this function as a general-purpose automation capability.
//
// A pause blocks automatic mutations, not a person's input. The per-target
// gate prevents a manual operation from interleaving with an automated action
// on that target; an already-running mutation on another target also blocks it.
func (b *Bridge) ManualTarget(ctx context.Context, requested action.Target, operation func(*browser.Session) error) error {
	if ctx == nil || operation == nil || requested.BrowserID == "" || requested.TabID == "" || requested.TargetID == "" {
		return errors.New("manual input requires a context and an exact browser and tab target")
	}
	view := b.Load()
	if view == nil {
		return ClientError("browser_unavailable", "browser is not ready")
	}
	target, err := action.SelectTarget(view.Info, requested)
	if err != nil {
		return err
	}
	if b.InputBlocked.Load() > 0 {
		b.NoteInputConflict()
		return ClientError("target_busy", "an automatic action is running; manual input was not sent")
	}
	created := make(chan struct{}, 1)
	actual, _ := b.operatorGates.LoadOrStore(target.ID, created)
	gate := actual.(chan struct{})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case gate <- struct{}{}:
	default:
		b.NoteInputConflict()
		return ClientError("target_busy", "the tab is busy; manual input was not queued")
	}
	release := func() { <-gate }
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.InputBlocked.Load() > 0 {
		b.NoteInputConflict()
		return ClientError("target_busy", "an automatic action is running; manual input was not sent")
	}
	// A tab may close between target selection and taking the gate. Re-resolve
	// it against the newest snapshot rather than acting on the stale session.
	view = b.Load()
	if view == nil {
		return ClientError("browser_unavailable", "browser is not ready")
	}
	target, err = action.SelectTarget(view.Info, requested)
	if err != nil {
		return err
	}
	sess := view.Sessions[target.ID]
	if sess == nil || sess.Context() == nil || sess.Context().Err() != nil {
		return ClientError("target_not_found", "selected tab is closed")
	}
	return operation(sess)
}
