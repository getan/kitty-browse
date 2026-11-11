package host

import (
	"context"
	"time"

	"github.com/getan/kitty-browse/internal/browser"
)

// ObserveIntervention latches a pause; disappearance of the challenge alone
// never authorizes automatic retries. A person explicitly resumes instead.
func (b *Bridge) ObserveIntervention(ctx context.Context, sess *browser.Session) {
	if sess == nil || b.Interaction().Paused {
		return
	}
	probe, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if info, err := sess.InterventionDetails(probe); err == nil && info.Reason != "" {
		b.interaction.mu.Lock()
		if info.RetryAfter.After(b.interaction.retryAfter) {
			b.interaction.retryAfter = info.RetryAfter
		}
		b.interaction.mu.Unlock()
		b.Pause(info.Reason)
	}
}
