package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"

	cdptarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

const (
	maxObservedPopups = 32
	recentPopupAge    = 5 * time.Second
)

// PopupWaitToken is a per-Session event sequence. Call BeginPopupWait before an
// operation which may open a window, then pass the result as sinceToken to
// WaitForPopup to exclude popups which predate that operation.
type PopupWaitToken uint64

type observedPopup struct {
	targetID  string
	openerID  string
	url       string
	title     string
	created   time.Time
	sequence  PopupWaitToken
	consumed  bool
	destroyed bool
}

type popupObserver struct {
	mu       sync.Mutex
	sequence PopupWaitToken
	popups   []*observedPopup
	byID     map[string]*observedPopup
	notify   chan struct{}
	stopped  bool
}

var popupObservers sync.Map // map[*Session]*popupObserver

func newPopupObserver() *popupObserver {
	return &popupObserver{byID: make(map[string]*observedPopup), notify: make(chan struct{}, 1)}
}

func startPopupObservation(s *Session, ctx context.Context) {
	o := newPopupObserver()
	popupObservers.Store(s, o)
	openerID := s.TargetID()
	created := chromedp.BrowserEvents(ctx, cdptarget.TargetCreated)
	changed := chromedp.BrowserEvents(ctx, cdptarget.TargetInfoChanged)
	destroyed := chromedp.BrowserEvents(ctx, cdptarget.TargetDestroyed)
	go func() {
		for event, err := range created {
			if err != nil {
				return
			}
			o.targetCreated(openerID, event.TargetInfo, time.Now())
		}
	}()
	go func() {
		for event, err := range changed {
			if err != nil {
				return
			}
			o.targetChanged(openerID, event.TargetInfo)
		}
	}()
	go func() {
		for event, err := range destroyed {
			if err != nil {
				return
			}
			o.targetDestroyed(string(event.TargetID))
		}
	}()
}

func stopPopupObservation(s *Session) {
	v, ok := popupObservers.LoadAndDelete(s)
	if !ok {
		return
	}
	o := v.(*popupObserver)
	o.mu.Lock()
	o.stopped = true
	o.popups = nil
	o.byID = nil
	o.signalLocked()
	o.mu.Unlock()
}

func popupTarget(info *cdptarget.Info, openerID string) bool {
	if info == nil || info.Type != "page" || string(info.OpenerID) != openerID {
		return false
	}
	return !strings.HasPrefix(info.URL, "chrome-extension://") &&
		!strings.HasPrefix(info.URL, "devtools://")
}

func (o *popupObserver) targetCreated(openerID string, info *cdptarget.Info, when time.Time) {
	if !popupTarget(info, openerID) {
		return
	}
	id := string(info.TargetID)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stopped {
		return
	}
	o.sequence++
	if existing := o.byID[id]; existing != nil {
		existing.url, existing.title = info.URL, info.Title
		o.signalLocked()
		return
	}
	p := &observedPopup{targetID: id, openerID: openerID, url: info.URL, title: info.Title, created: when, sequence: o.sequence}
	o.popups = append(o.popups, p)
	o.byID[id] = p
	if len(o.popups) > maxObservedPopups {
		o.trimLocked()
	}
	o.signalLocked()
}

func (o *popupObserver) targetChanged(openerID string, info *cdptarget.Info) {
	if info == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stopped {
		return
	}
	o.sequence++
	p := o.byID[string(info.TargetID)]
	if p == nil {
		// Usually TargetCreated arrives first. Recording here as well makes the
		// observer robust to listener startup and cross-channel scheduling races.
		if popupTarget(info, openerID) {
			p = &observedPopup{targetID: string(info.TargetID), openerID: openerID, created: time.Now(), sequence: o.sequence}
			o.popups = append(o.popups, p)
			o.byID[p.targetID] = p
			if len(o.popups) > maxObservedPopups {
				o.trimLocked()
			}
		} else {
			return
		}
	}
	if !popupTarget(info, openerID) {
		p.destroyed = true
	} else {
		p.url, p.title = info.URL, info.Title
	}
	o.signalLocked()
}

func (o *popupObserver) targetDestroyed(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stopped {
		return
	}
	o.sequence++
	if p := o.byID[id]; p != nil {
		p.destroyed = true
		o.signalLocked()
	}
}

func (o *popupObserver) trimLocked() {
	for len(o.popups) > maxObservedPopups {
		old := o.popups[0]
		delete(o.byID, old.targetID)
		copy(o.popups, o.popups[1:])
		o.popups[len(o.popups)-1] = nil
		o.popups = o.popups[:len(o.popups)-1]
	}
}

func (o *popupObserver) signalLocked() {
	select {
	case o.notify <- struct{}{}:
	default:
	}
}

// BeginPopupWait returns the current popup event sequence. It has no browser
// side effects and is safe to call immediately before the action that may open
// a popup.
func (s *Session) BeginPopupWait() PopupWaitToken {
	if s == nil {
		return 0
	}
	v, ok := popupObservers.Load(s)
	if !ok {
		return 0
	}
	o := v.(*popupObserver)
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sequence
}

type popupWaitParams struct {
	URLPattern   string          `json:"urlPattern"`
	TitlePattern string          `json:"titlePattern"`
	SinceToken   *PopupWaitToken `json:"sinceToken"`
}

type popupWaitResult struct {
	TargetID       string `json:"targetId"`
	URL            string `json:"url"`
	Title          string `json:"title"`
	OpenerTargetID string `json:"openerTargetId"`
}

// WaitForPopup waits for one unconsumed page opened by this Session. With no
// sinceToken it considers only popups from the preceding five seconds, allowing
// the common click-then-wait flow without accidentally returning old windows.
// A matching popup is consumed exactly once. This method never attaches to,
// activates, or otherwise manages the popup target.
func (s *Session) WaitForPopup(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var p popupWaitParams
	if len(bytes.TrimSpace(params)) == 0 || bytes.Equal(bytes.TrimSpace(params), []byte("null")) {
		params = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(params))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return nil, autoErr("invalid_params", "invalid popup wait parameters")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	op, cancel, err := s.operationContext(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	v, ok := popupObservers.Load(s)
	if !ok {
		return nil, autoErr("session_closed", "popup observation is unavailable")
	}
	o := v.(*popupObserver)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		candidates := o.candidates(p, time.Now())
		if len(candidates) != 0 {
			infos, queryErr := chromedp.Targets(op)
			if queryErr != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, autoErr("cdp_error", "could not inspect popup target")
			}
			liveByID := make(map[string]*cdptarget.Info, len(infos))
			for _, info := range infos {
				if info != nil {
					liveByID[string(info.TargetID)] = info
				}
			}
			for _, candidate := range candidates {
				live := liveByID[candidate.targetID]
				if live == nil || !popupTarget(live, candidate.openerID) {
					o.targetDestroyed(candidate.targetID)
				} else if live.URL != "" && popupPatternMatch(p.URLPattern, live.URL) && popupPatternMatch(p.TitlePattern, live.Title) {
					result, consumed := o.consume(candidate, live)
					if consumed {
						return json.Marshal(result)
					}
				} else {
					o.update(candidate, live)
				}
			}
		}

		select {
		case <-op.Done():
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, op.Err()
		case <-o.notify:
		case <-ticker.C:
		}
	}
}

func (o *popupObserver) candidates(p popupWaitParams, now time.Time) []*observedPopup {
	o.mu.Lock()
	defer o.mu.Unlock()
	var candidates []*observedPopup
	for _, item := range o.popups {
		if item == nil || item.consumed || item.destroyed {
			continue
		}
		if p.SinceToken != nil {
			if item.sequence <= *p.SinceToken {
				continue
			}
		} else if now.Sub(item.created) > recentPopupAge {
			continue
		}
		// Poll even before both patterns match: targetCreated often reports an
		// empty URL/title and TargetInfoChanged follows after navigation.
		candidates = append(candidates, item)
	}
	return candidates
}

func (o *popupObserver) update(item *observedPopup, info *cdptarget.Info) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.byID[item.targetID] == item && !item.destroyed {
		item.url, item.title = info.URL, info.Title
	}
}

func (o *popupObserver) consume(item *observedPopup, info *cdptarget.Info) (popupWaitResult, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.byID[item.targetID] != item || item.consumed || item.destroyed {
		return popupWaitResult{}, false
	}
	item.url, item.title, item.consumed = info.URL, info.Title, true
	return popupWaitResult{TargetID: item.targetID, URL: item.url, Title: item.title, OpenerTargetID: item.openerID}, true
}

func popupPatternMatch(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	if !strings.ContainsAny(pattern, "*?") {
		return strings.Contains(value, pattern)
	}
	glob := regexp.QuoteMeta(pattern)
	glob = strings.ReplaceAll(glob, `\*`, `.*`)
	glob = strings.ReplaceAll(glob, `\?`, `.`)
	return regexp.MustCompile("^(?:" + glob + ")$").MatchString(value)
}
