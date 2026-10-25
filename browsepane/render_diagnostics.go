package browsepane

import (
	"context"
	"log"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	engine "github.com/getan/kitty-browse/internal/browser"
)

// Chromium output must not go to the graphics terminal. Quote it into the
// explicitly requested debug log instead; even split lines/escape sequences
// cannot corrupt its structure. Never constructed for private sessions.
type chromiumLogWriter struct{ logger *log.Logger }

func (w chromiumLogWriter) Write(p []byte) (int, error) {
	const maxChunk = 4096
	for start := 0; start < len(p); start += maxChunk {
		w.logger.Printf("chromium output: %q", p[start:min(start+maxChunk, len(p))])
	}
	return len(p), nil
}

func logBrowserDiagnostics(sess *engine.Session) {
	if debugLog.Load() == nil {
		return
	}
	if c := chromedp.FromContext(sess.Context()); c != nil && c.Browser != nil {
		if process := c.Browser.Process(); process != nil {
			debugf("chromium pid=%d target=%s", process.Pid, sess.TargetID())
		}
	}
	ctx, cancel := context.WithTimeout(sess.Context(), time.Second)
	defer cancel()
	v, err := chromedp.CallBrowser(ctx, browser.GetVersion, cdp.Empty{})
	if err != nil {
		debugf("chromium version unavailable: %v", err)
		return
	}
	debugf("chromium product=%q revision=%q protocol=%q", v.Product, v.Revision, v.ProtocolVersion)
}
