package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// Inspect performs a read or state action against this Session's target. The
// caller context can cancel an operation without becoming the target's owner.
func (s *Session) Inspect(ctx context.Context, name string, params json.RawMessage) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.ctx == nil || s.ctx.Err() != nil {
		return nil, automationError("target_closed", "browser target is closed")
	}

	switch name {
	case "network.requests":
		return s.inspectNetworkRequests(params)
	case "network.response":
		return s.inspectNetworkResponse(ctx, params)
	case "console":
		return s.inspectMessages(params, false)
	case "errors":
		return s.inspectMessages(params, true)
	case "cookies.get":
		return s.inspectCookiesGet(ctx, params)
	case "cookies.set":
		return s.inspectCookiesSet(ctx, params)
	case "cookies.clear":
		return s.inspectCookiesClear(ctx, params)
	case "storage.get", "storage.set", "storage.clear":
		return s.inspectStorage(ctx, name, params)
	default:
		return nil, automationError("unsupported_action", "unsupported inspect action: "+name)
	}
}

type requestListParams struct {
	Filter string `json:"filter"`
	Clear  bool   `json:"clear"`
}

func (s *Session) inspectNetworkRequests(raw json.RawMessage) (json.RawMessage, error) {
	var p requestListParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	o, err := observerFor(s)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	items := make([]observedRequest, 0, len(o.requests))
	for _, item := range o.requests {
		if p.Filter == "" || strings.Contains(item.URL, p.Filter) {
			items = append(items, *item)
		}
	}
	if p.Clear {
		o.requests = nil
		o.byID = make(map[network.RequestID]*observedRequest)
	}
	o.mu.Unlock()
	return marshalObject(struct {
		Requests []observedRequest `json:"requests"`
	}{Requests: items})
}

type responseParams struct {
	RequestID string `json:"requestId"`
}

func (s *Session) inspectNetworkResponse(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	return s.inspectNetworkResponseWithFetcher(ctx, raw,
		func(opCtx context.Context, id network.RequestID) ([]byte, bool, error) {
			result, err := chromedp.Call(opCtx, network.GetResponseBody,
				network.GetResponseBodyParams{RequestID: id})
			if err != nil {
				return nil, false, err
			}
			return result.Body, result.Base64encoded, nil
		})
}

type responseBodyFetcher func(context.Context, network.RequestID) ([]byte, bool, error)

func (s *Session) inspectNetworkResponseWithFetcher(ctx context.Context, raw json.RawMessage, fetch responseBodyFetcher) (json.RawMessage, error) {
	var p responseParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	if p.RequestID == "" {
		return nil, automationError("invalid_arguments", "requestId is required")
	}
	o, err := observerFor(s)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	request, found := o.byID[network.RequestID(p.RequestID)]
	tooLarge := found && request.EncodedDataLength > maxResponseBody
	o.mu.Unlock()
	if !found {
		return nil, automationError("request_not_found", "request was not observed in this tab")
	}
	if tooLarge {
		return nil, automationError("result_too_large", "response body exceeds the 1 MiB limit")
	}

	opCtx, cancel := s.inspectContext(ctx)
	body, base64Encoded, callErr := fetch(opCtx, network.RequestID(p.RequestID))
	cancel()
	if callErr != nil {
		return nil, s.inspectCallError(callErr)
	}
	bodySize := len(body)
	truncated := len(body) > maxResponseBody
	if truncated {
		body = body[:maxResponseBody]
	}
	bodyText := string(body)
	if base64Encoded {
		bodyText = base64.StdEncoding.EncodeToString(body)
	}
	return marshalObject(struct {
		RequestID     string `json:"requestId"`
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
		Truncated     bool   `json:"truncated"`
		Size          int    `json:"size"`
	}{p.RequestID, bodyText, base64Encoded, truncated, bodySize})
}

type clearParams struct {
	Clear bool `json:"clear"`
}

func (s *Session) inspectMessages(raw json.RawMessage, exceptions bool) (json.RawMessage, error) {
	var p clearParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	o, err := observerFor(s)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	source := o.console
	if exceptions {
		source = o.errors
	}
	items := append([]observedMessage{}, source...)
	if p.Clear {
		if exceptions {
			o.errors = nil
		} else {
			o.console = nil
		}
	}
	o.mu.Unlock()
	return marshalObject(struct {
		Entries []observedMessage `json:"entries"`
	}{Entries: items})
}

type cookieGetParams struct {
	Name string `json:"name"`
}

type cookieView struct {
	Name     string                 `json:"name"`
	Value    string                 `json:"value"`
	Domain   string                 `json:"domain"`
	Path     string                 `json:"path"`
	Expires  float64                `json:"expires"`
	HTTPOnly bool                   `json:"httpOnly"`
	Secure   bool                   `json:"secure"`
	Session  bool                   `json:"session"`
	SameSite network.CookieSameSite `json:"sameSite,omitempty"`
}

func (s *Session) inspectCookiesGet(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p cookieGetParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	url, err := s.currentURL(ctx)
	if err != nil {
		return nil, err
	}
	opCtx, cancel := s.inspectContext(ctx)
	result, callErr := chromedp.Call(opCtx, network.GetCookies, network.GetCookiesParams{URLs: []string{url}})
	cancel()
	if callErr != nil {
		return nil, s.inspectCallError(callErr)
	}
	items := make([]cookieView, 0, len(result.Cookies))
	for _, c := range result.Cookies {
		if p.Name != "" && c.Name != p.Name {
			continue
		}
		items = append(items, cookieView{c.Name, c.Value, c.Domain, c.Path, c.Expires,
			c.HTTPOnly, c.Secure, c.Session, c.SameSite})
	}
	return marshalObject(struct {
		Cookies []cookieView `json:"cookies"`
	}{Cookies: items})
}

type cookieSetParams struct {
	Name     string                 `json:"name"`
	Value    *string                `json:"value"`
	URL      string                 `json:"url"`
	Path     string                 `json:"path"`
	Domain   string                 `json:"domain"`
	Expires  *float64               `json:"expires"`
	Secure   *bool                  `json:"secure"`
	HTTPOnly *bool                  `json:"httpOnly"`
	SameSite network.CookieSameSite `json:"sameSite"`
}

func (s *Session) inspectCookiesSet(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p cookieSetParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	if p.Name == "" || p.Value == nil {
		return nil, automationError("invalid_arguments", "cookie name and value are required")
	}
	if p.URL == "" {
		var err error
		p.URL, err = s.currentURL(ctx)
		if err != nil {
			return nil, err
		}
	}
	params := network.SetCookieParams{Name: p.Name, Value: *p.Value, URL: p.URL,
		Domain: p.Domain, Path: p.Path, Secure: p.Secure, HTTPOnly: p.HTTPOnly, SameSite: p.SameSite}
	if p.Expires != nil {
		params.Expires = cdp.TimeSinceEpoch(*p.Expires)
	}
	opCtx, cancel := s.inspectContext(ctx)
	_, callErr := chromedp.Call(opCtx, network.SetCookie, params)
	cancel()
	if callErr != nil {
		return nil, s.inspectCallError(callErr)
	}
	return json.RawMessage(`{"success":true}`), nil
}

func (s *Session) inspectCookiesClear(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p cookieGetParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	url, err := s.currentURL(ctx)
	if err != nil {
		return nil, err
	}
	opCtx, cancel := s.inspectContext(ctx)
	result, callErr := chromedp.Call(opCtx, network.GetCookies, network.GetCookiesParams{URLs: []string{url}})
	if callErr == nil {
		for _, c := range result.Cookies {
			if p.Name != "" && c.Name != p.Name {
				continue
			}
			_, callErr = chromedp.Call(opCtx, network.DeleteCookies, network.DeleteCookiesParams{
				Name: c.Name, Domain: c.Domain, Path: c.Path, PartitionKey: c.PartitionKey,
			})
			if callErr != nil {
				break
			}
		}
	}
	cancel()
	if callErr != nil {
		return nil, s.inspectCallError(callErr)
	}
	count := 0
	for _, c := range result.Cookies {
		if p.Name == "" || c.Name == p.Name {
			count++
		}
	}
	return marshalObject(struct {
		Cleared int `json:"cleared"`
	}{count})
}

type storageParams struct {
	Kind           string  `json:"kind"`
	Key            *string `json:"key"`
	Value          *string `json:"value"`
	CurrentOrigin  *bool   `json:"currentorigin"`
	CurrentOrigin2 *bool   `json:"currentOrigin"`
}

func (s *Session) inspectStorage(ctx context.Context, action string, raw json.RawMessage) (json.RawMessage, error) {
	var p storageParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	if p.Kind != "local" && p.Kind != "session" {
		return nil, automationError("invalid_arguments", "storage kind must be local or session")
	}
	if (p.CurrentOrigin != nil && !*p.CurrentOrigin) || (p.CurrentOrigin2 != nil && !*p.CurrentOrigin2) {
		return nil, automationError("invalid_arguments", "only the current origin is supported")
	}
	if action == "storage.set" && (p.Key == nil || p.Value == nil) {
		return nil, automationError("invalid_arguments", "storage key and value are required")
	}
	store := "localStorage"
	if p.Kind == "session" {
		store = "sessionStorage"
	}
	keyJSON, _ := json.Marshal(p.Key)
	valueJSON, _ := json.Marshal(p.Value)
	var expression string
	switch action {
	case "storage.get":
		if p.Key == nil {
			expression = fmt.Sprintf(`JSON.stringify({kind:%q,origin:location.origin,values:Object.fromEntries(Object.entries(%s))})`, p.Kind, store)
		} else {
			expression = fmt.Sprintf(`(()=>{const k=%s,v=%s.getItem(k);return JSON.stringify({kind:%q,origin:location.origin,key:k,value:v,found:v!==null})})()`, keyJSON, store, p.Kind)
		}
	case "storage.set":
		expression = fmt.Sprintf(`(()=>{%s.setItem(%s,%s);return JSON.stringify({success:true,kind:%q,origin:location.origin})})()`, store, keyJSON, valueJSON, p.Kind)
	case "storage.clear":
		if p.Key == nil {
			expression = fmt.Sprintf(`(()=>{%s.clear();return JSON.stringify({success:true,kind:%q,origin:location.origin})})()`, store, p.Kind)
		} else {
			expression = fmt.Sprintf(`(()=>{%s.removeItem(%s);return JSON.stringify({success:true,kind:%q,origin:location.origin})})()`, store, keyJSON, p.Kind)
		}
	}
	opCtx, cancel := s.inspectContext(ctx)
	encoded, callErr := chromedp.Run(opCtx, chromedp.Evaluate[string](expression))
	cancel()
	if callErr != nil {
		return nil, s.inspectCallError(callErr)
	}
	if !json.Valid([]byte(encoded)) {
		return nil, fmt.Errorf("invalid storage result")
	}
	return json.RawMessage(encoded), nil
}

func (s *Session) currentURL(ctx context.Context) (string, error) {
	opCtx, cancel := s.inspectContext(ctx)
	url, err := chromedp.Run(opCtx, chromedp.Evaluate[string](`location.href`))
	cancel()
	if err != nil {
		return "", s.inspectCallError(err)
	}
	return url, nil
}

func (s *Session) inspectContext(caller context.Context) (context.Context, context.CancelFunc) {
	opCtx, cancel := context.WithTimeout(s.ctx, inspectTimeout)
	stop := context.AfterFunc(caller, cancel)
	return opCtx, func() { stop(); cancel() }
}

func (s *Session) inspectCallError(err error) error {
	if s == nil || s.ctx == nil || s.ctx.Err() != nil {
		return automationError("target_closed", "browser target is closed")
	}
	return err
}

func observerFor(s *Session) (*observer, error) {
	v, ok := sessionObservers.Load(s)
	if !ok || v.(*observer).stopped.Load() {
		return nil, automationError("target_closed", "observation is not active for this tab")
	}
	return v.(*observer), nil
}

func decodeParams(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return automationError("invalid_arguments", "parameters must be a JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return automationError("invalid_arguments", "parameters must be a JSON object")
	}
	return nil
}

func marshalObject(value any) (json.RawMessage, error) {
	b, err := json.Marshal(value)
	return json.RawMessage(b), err
}

func automationError(code, message string) error {
	return &AutomationError{Code: code, Message: message}
}
