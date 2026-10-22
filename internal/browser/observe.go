package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const (
	maxObservedRequests = 500
	maxObservedMessages = 200
	maxResponseBody     = 1 << 20
	maxObservedText     = 64 << 10
	maxConsoleArgs      = 100
	inspectTimeout      = 5 * time.Second
)

var sessionObservers sync.Map // map[*Session]*observer

// observedRequest is deliberately a small, header-free projection of CDP's
// network events. In particular, request bodies, Cookie and Authorization are
// never copied into observation state.
type observedRequest struct {
	RequestID         string                  `json:"requestId"`
	URL               string                  `json:"url"`
	Method            string                  `json:"method"`
	Status            int64                   `json:"status,omitempty"`
	ResourceType      network.ResourceType    `json:"resourceType,omitempty"`
	MIME              string                  `json:"mimeType,omitempty"`
	Error             string                  `json:"error,omitempty"`
	Canceled          bool                    `json:"canceled,omitempty"`
	Finished          bool                    `json:"finished,omitempty"`
	EncodedDataLength float64                 `json:"encodedDataLength,omitempty"`
	Timestamp         float64                 `json:"timestamp"`
	DurationMS        float64                 `json:"durationMs,omitempty"`
	Timing            *network.ResourceTiming `json:"timing,omitempty"`
	start             float64
}

type observedMessage struct {
	Type      string   `json:"type,omitempty"`
	Text      string   `json:"text"`
	Args      []string `json:"args,omitempty"`
	URL       string   `json:"url,omitempty"`
	Line      int64    `json:"line,omitempty"`
	Column    int64    `json:"column,omitempty"`
	Timestamp float64  `json:"timestamp,omitempty"`
}

type observer struct {
	mu       sync.Mutex
	requests []*observedRequest
	byID     map[network.RequestID]*observedRequest
	console  []observedMessage
	errors   []observedMessage
	stopped  atomic.Bool
	cancel   context.CancelFunc
}

func newObserver() *observer { return &observer{byID: make(map[network.RequestID]*observedRequest)} }

// StartObservation enables native CDP collection for this tab. It must be
// called after the target has been initialized and before its first load.
func (s *Session) StartObservation() error {
	if s == nil || s.ctx == nil || s.ctx.Err() != nil {
		return automationError("target_closed", "browser target is closed")
	}
	o := newObserver()
	actual, loaded := sessionObservers.LoadOrStore(s, o)
	if loaded {
		if !actual.(*observer).stopped.Load() {
			return nil
		}
		sessionObservers.Store(s, o)
	}
	if err := startDownloadObservation(s.ctx); err != nil {
		sessionObservers.Delete(s)
		return err
	}

	listenCtx, listenCancel := context.WithCancel(s.ctx)
	o.cancel = listenCancel
	startPopupObservation(s, listenCtx)
	startDialogObservation(s, listenCtx)
	requestEvents := chromedp.Events(listenCtx, network.RequestWillBeSent)
	responseEvents := chromedp.Events(listenCtx, network.ResponseReceived)
	finishedEvents := chromedp.Events(listenCtx, network.LoadingFinished)
	failedEvents := chromedp.Events(listenCtx, network.LoadingFailed)
	consoleEvents := chromedp.Events(listenCtx, runtime.ConsoleAPICalled)
	exceptionEvents := chromedp.Events(listenCtx, runtime.ExceptionThrown)
	go func() {
		for e, err := range requestEvents {
			if err != nil {
				return
			}
			o.handleEvent(&e)
		}
	}()
	go func() {
		for e, err := range responseEvents {
			if err != nil {
				return
			}
			o.handleEvent(&e)
		}
	}()
	go func() {
		for e, err := range finishedEvents {
			if err != nil {
				return
			}
			o.handleEvent(&e)
		}
	}()
	go func() {
		for e, err := range failedEvents {
			if err != nil {
				return
			}
			o.handleEvent(&e)
		}
	}()
	go func() {
		for e, err := range consoleEvents {
			if err != nil {
				return
			}
			o.handleEvent(&e)
		}
	}()
	go func() {
		for e, err := range exceptionEvents {
			if err != nil {
				return
			}
			o.handleEvent(&e)
		}
	}()
	ctx, cancel := context.WithTimeout(s.ctx, inspectTimeout)
	_, err := chromedp.Call(ctx, network.Enable, network.EnableParams{})
	if err == nil {
		_, err = chromedp.Call(ctx, runtime.Enable, cdp.Empty{})
	}
	if err == nil {
		_, err = chromedp.Call(ctx, page.Enable, page.EnableParams{})
	}
	cancel()
	if err != nil {
		s.StopObservation()
		if s.ctx.Err() != nil {
			return automationError("target_closed", "browser target is closed")
		}
		return fmt.Errorf("start CDP observation: %w", err)
	}
	go func() {
		<-s.ctx.Done()
		s.StopObservation()
	}()
	return nil
}

// StopObservation stops retaining events and immediately releases all
// observations. The chromedp listener itself is tied to the Session context.
func (s *Session) StopObservation() {
	if s == nil {
		return
	}
	stopPopupObservation(s)
	stopDialogObservation(s)
	v, ok := sessionObservers.LoadAndDelete(s)
	if !ok {
		return
	}
	o := v.(*observer)
	o.stopped.Store(true)
	if o.cancel != nil {
		o.cancel()
	}
	o.mu.Lock()
	o.requests = nil
	o.byID = nil
	o.console = nil
	o.errors = nil
	o.mu.Unlock()
}

func (o *observer) handleEvent(event any) {
	if o.stopped.Load() {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stopped.Load() {
		return
	}
	switch e := event.(type) {
	case *network.EventRequestWillBeSent:
		if e.Request == nil {
			return
		}
		// A redirect reuses its request id. Finish the old projection before
		// making the new request the body-accessible one.
		if old := o.byID[e.RequestID]; old != nil && e.RedirectResponse != nil {
			old.Status = e.RedirectResponse.Status
			old.MIME = e.RedirectResponse.MimeType
			old.Finished = true
		}
		r := &observedRequest{
			RequestID: string(e.RequestID), URL: sanitizeObservedURL(e.Request.URL),
			Method: e.Request.Method, ResourceType: e.Type,
			Timestamp: float64(e.Timestamp), start: float64(e.Timestamp),
		}
		o.requests = append(o.requests, r)
		o.byID[e.RequestID] = r
		o.trimRequests()
	case *network.EventResponseReceived:
		if r := o.byID[e.RequestID]; r != nil && e.Response != nil {
			r.Status = e.Response.Status
			r.ResourceType = e.Type
			r.MIME = e.Response.MimeType
			r.Timing = e.Response.Timing
			r.EncodedDataLength = e.Response.EncodedDataLength
		}
	case *network.EventLoadingFinished:
		if r := o.byID[e.RequestID]; r != nil {
			r.Finished = true
			r.EncodedDataLength = e.EncodedDataLength
			r.DurationMS = (float64(e.Timestamp) - r.start) * 1000
		}
	case *network.EventLoadingFailed:
		if r := o.byID[e.RequestID]; r != nil {
			r.Finished = true
			r.Error = e.ErrorText
			r.Canceled = e.Canceled
			r.ResourceType = e.Type
			r.DurationMS = (float64(e.Timestamp) - r.start) * 1000
		}
	case *runtime.EventConsoleAPICalled:
		m := observedMessage{Type: string(e.Type), Timestamp: float64(e.Timestamp)}
		remaining := maxObservedText
		for _, arg := range e.Args {
			if len(m.Args) == maxConsoleArgs || remaining == 0 {
				break
			}
			if len(m.Args) > 0 {
				remaining-- // The separating space in Text.
			}
			text := truncateText(remoteObjectText(arg), remaining)
			m.Args = append(m.Args, text)
			remaining -= len(text)
		}
		m.Text = strings.Join(m.Args, " ")
		o.console = appendBoundedMessage(o.console, m)
	case *runtime.EventExceptionThrown:
		if e.ExceptionDetails == nil {
			return
		}
		d := e.ExceptionDetails
		text := d.Text
		if d.Exception != nil && d.Exception.Description != "" {
			text = d.Exception.Description
		}
		o.errors = appendBoundedMessage(o.errors, observedMessage{
			Type: "exception", Text: limitText(text), URL: sanitizeObservedURL(d.URL),
			Line: d.LineNumber, Column: d.ColumnNumber,
			Timestamp: float64(e.Timestamp),
		})
	}
}

func (o *observer) trimRequests() {
	for len(o.requests) > maxObservedRequests {
		old := o.requests[0]
		o.requests[0] = nil
		o.requests = o.requests[1:]
		id := network.RequestID(old.RequestID)
		if o.byID[id] == old {
			delete(o.byID, id)
		}
	}
}

func appendBoundedMessage(items []observedMessage, item observedMessage) []observedMessage {
	items = append(items, item)
	if len(items) > maxObservedMessages {
		copy(items, items[len(items)-maxObservedMessages:])
		items = items[:maxObservedMessages]
	}
	return items
}

func remoteObjectText(obj *runtime.RemoteObject) string {
	if obj == nil {
		return "null"
	}
	if obj.Description != "" {
		return limitText(obj.Description)
	}
	if obj.UnserializableValue != "" {
		return limitText(string(obj.UnserializableValue))
	}
	b, err := json.Marshal(obj.Value)
	if err == nil && string(b) != "null" {
		return limitText(string(b))
	}
	return string(obj.Type)
}

func limitText(s string) string {
	return truncateText(s, maxObservedText)
}

func truncateText(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	const marker = "…"
	if max <= len(marker) {
		return marker[:max]
	}
	end := max - len(marker)
	// Keep JSON output valid UTF-8 when the byte boundary splits a rune.
	for end > 0 && end < len(s) && (s[end]&0xc0) == 0x80 {
		end--
	}
	return s[:end] + marker
}

var sensitiveQueryKeyParts = [...]string{
	"token", "auth", "code", "key", "secret", "password", "passwd",
	"signature", "sig", "credential", "session", "cookie",
}

// sanitizeObservedURL is the only URL projection retained by the observer.
// It deliberately preserves ordinary query parameters so network.requests
// filters operate on the same redacted URL that callers receive.
func sanitizeObservedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid URL]"
	}
	u.User = nil
	u.Fragment = ""
	query := u.Query()
	for key, values := range query {
		lower := strings.ToLower(key)
		sensitive := false
		for _, part := range sensitiveQueryKeyParts {
			if strings.Contains(lower, part) {
				sensitive = true
				break
			}
		}
		if sensitive {
			for i := range values {
				values[i] = "[REDACTED]"
			}
			query[key] = values
		}
	}
	u.RawQuery = query.Encode()
	return u.String()
}
