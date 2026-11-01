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
	if reason, err := sess.Intervention(probe); err == nil && reason != "" {
		b.Pause(reason)
	}
}
