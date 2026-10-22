package browser

import "github.com/chromedp/chromedp"

// TargetID is the tab's opaque stable CDP id. No remote debugging listener is
// required: automation uses the existing private Chromium pipe connection.
func (s *Session) TargetID() string {
	if s == nil || s.ctx == nil {
		return ""
	}
	c := chromedp.FromContext(s.ctx)
	if c == nil || c.Target == nil {
		return ""
	}
	return string(c.Target.TargetID)
}
