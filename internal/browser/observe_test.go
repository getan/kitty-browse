package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
)

func TestObserverBoundsAndDoesNotRetainSensitiveRequestData(t *testing.T) {
	o := newObserver()
	for i := 0; i < maxObservedRequests+17; i++ {
		o.handleEvent(&network.EventRequestWillBeSent{
			RequestID: network.RequestID(fmt.Sprintf("request-%d", i)),
			Timestamp: cdp.MonotonicTime(i),
			Type:      network.ResourceTypeFetch,
			Request: &network.Request{
				URL: "https://synthetic.invalid/item/" + fmt.Sprint(i), Method: "POST",
				Headers:     network.Headers{"Authorization": "Bearer do-not-retain", "Cookie": "secret=yes"},
				HasPostData: true,
			},
		})
	}
	o.mu.Lock()
	if got := len(o.requests); got != maxObservedRequests {
		t.Fatalf("retained requests = %d, want %d", got, maxObservedRequests)
	}
	if _, exists := o.byID["request-0"]; exists {
		t.Fatal("evicted request remains in request-id index")
	}
	b, err := json.Marshal(o.requests)
	o.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, forbidden := range []string{"Authorization", "Bearer do-not-retain", "Cookie", "secret=yes", "postData"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("observation retained sensitive request data %q: %s", forbidden, text)
		}
	}
}

func TestObserverMessageBoundsAndInspectClear(t *testing.T) {
	o := newObserver()
	for i := 0; i < maxObservedMessages+11; i++ {
		o.handleEvent(&runtime.EventConsoleAPICalled{
			Type: runtime.ConsoleAPICalledTypeLog,
			Args: []*runtime.RemoteObject{{Description: fmt.Sprintf("message-%d", i)}},
		})
	}
	if got := len(o.console); got != maxObservedMessages {
		t.Fatalf("retained console messages = %d, want %d", got, maxObservedMessages)
	}

	s := &Session{ctx: context.Background()}
	sessionObservers.Store(s, o)
	t.Cleanup(func() { s.StopObservation() })
	result, err := s.inspectMessages(json.RawMessage(`{"clear":true}`), false)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Entries []observedMessage `json:"entries"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Entries) != maxObservedMessages {
		t.Fatalf("returned entries = %d, want %d", len(envelope.Entries), maxObservedMessages)
	}
	if len(o.console) != 0 {
		t.Fatalf("console was not cleared: %d entries", len(o.console))
	}
}

func TestInspectNetworkRequestsFilterAndClear(t *testing.T) {
	o := newObserver()
	for id, url := range map[string]string{
		"keep": "https://synthetic.invalid/api/keep",
		"drop": "https://synthetic.invalid/image.png",
	} {
		o.handleEvent(&network.EventRequestWillBeSent{
			RequestID: network.RequestID(id),
			Request:   &network.Request{URL: url, Method: "GET"},
		})
	}
	s := &Session{ctx: context.Background()}
	sessionObservers.Store(s, o)
	t.Cleanup(func() { s.StopObservation() })

	raw, err := s.inspectNetworkRequests(json.RawMessage(`{"filter":"/api/","clear":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Requests []observedRequest `json:"requests"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Requests) != 1 || result.Requests[0].RequestID != "keep" {
		t.Fatalf("filtered requests = %+v", result.Requests)
	}
	if len(o.requests) != 0 || len(o.byID) != 0 {
		t.Fatalf("clear left requests=%d index=%d", len(o.requests), len(o.byID))
	}
}

func TestObserverRedactsSensitiveURLPartsBeforeRetention(t *testing.T) {
	const rawSecret = "raw-secret-that-must-not-be-retained"
	o := newObserver()
	o.handleEvent(&network.EventRequestWillBeSent{
		RequestID: "redacted",
		Request: &network.Request{
			URL:    "https://alice:" + rawSecret + "@synthetic.invalid/api?page=2&AccessToken=" + rawSecret + "&theme=dark#private",
			Method: "GET",
		},
	})

	o.mu.Lock()
	retained := o.requests[0].URL
	// The complete retained request projection must be safe to serialize; there
	// is intentionally no second field containing the original URL.
	memoryJSON, err := json.Marshal(o.requests)
	o.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(retained, "alice") || strings.Contains(retained, rawSecret) || strings.Contains(retained, "private") {
		t.Fatalf("sensitive URL parts retained: %q", retained)
	}
	if !strings.Contains(retained, "page=2") || !strings.Contains(retained, "theme=dark") ||
		!strings.Contains(retained, "AccessToken=%5BREDACTED%5D") {
		t.Fatalf("URL was not selectively redacted: %q", retained)
	}
	if strings.Contains(string(memoryJSON), rawSecret) {
		t.Fatalf("raw secret appears in observer JSON: %s", memoryJSON)
	}

	s := &Session{ctx: context.Background()}
	sessionObservers.Store(s, o)
	t.Cleanup(func() { s.StopObservation() })
	result, err := s.inspectNetworkRequests(json.RawMessage(`{"filter":"page=2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), `"requestId":"redacted"`) || strings.Contains(string(result), rawSecret) {
		t.Fatalf("filter did not operate on safe observed URL: %s", result)
	}
}

func TestInspectNetworkResponseRejectsKnownOversizeWithoutFetching(t *testing.T) {
	o := newObserver()
	o.byID["large"] = &observedRequest{RequestID: "large", EncodedDataLength: maxResponseBody + 1}
	s := &Session{ctx: context.Background()}
	sessionObservers.Store(s, o)
	t.Cleanup(func() { s.StopObservation() })

	calls := 0
	_, err := s.inspectNetworkResponseWithFetcher(context.Background(), json.RawMessage(`{"requestId":"large"}`),
		func(context.Context, network.RequestID) ([]byte, bool, error) {
			calls++
			return nil, false, nil
		})
	var ae *AutomationError
	if !errors.As(err, &ae) || ae.Code != "result_too_large" {
		t.Fatalf("error = %#v, want result_too_large", err)
	}
	if calls != 0 {
		t.Fatalf("response body fetch called %d times", calls)
	}
}

func TestInspectMessagesReturnsEmptyArrays(t *testing.T) {
	o := newObserver()
	s := &Session{ctx: context.Background()}
	sessionObservers.Store(s, o)
	t.Cleanup(func() { s.StopObservation() })
	for _, exceptions := range []bool{false, true} {
		result, err := s.inspectMessages(nil, exceptions)
		if err != nil {
			t.Fatal(err)
		}
		if string(result) != `{"entries":[]}` {
			t.Fatalf("empty messages = %s", result)
		}
	}
}

func TestObserverTruncatesConsoleAndExceptionMessages(t *testing.T) {
	o := newObserver()
	args := make([]*runtime.RemoteObject, maxConsoleArgs+20)
	for i := range args {
		args[i] = &runtime.RemoteObject{Description: strings.Repeat("x", 1024)}
	}
	o.handleEvent(&runtime.EventConsoleAPICalled{Args: args})
	o.handleEvent(&runtime.EventExceptionThrown{ExceptionDetails: &runtime.ExceptionDetails{
		Text: strings.Repeat("e", maxObservedText+100),
	}})

	if got := len(o.console[0].Args); got > maxConsoleArgs {
		t.Fatalf("console args = %d, max %d", got, maxConsoleArgs)
	}
	if got := len(o.console[0].Text); got > maxObservedText {
		t.Fatalf("console text bytes = %d, max %d", got, maxObservedText)
	}
	if got := len(o.errors[0].Text); got > maxObservedText {
		t.Fatalf("exception text bytes = %d, max %d", got, maxObservedText)
	}
	if !strings.HasSuffix(o.errors[0].Text, "…") {
		t.Fatal("truncated exception has no marker")
	}
}
