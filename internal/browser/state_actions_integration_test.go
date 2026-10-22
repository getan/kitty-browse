package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestInspectIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("KITTY_BROWSE_CHROME is not set")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<body>synthetic<script>
console.log("synthetic-console");
setTimeout(() => { throw new Error("synthetic-error") }, 0);
fetch('/data');
</script></body>`))
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("synthetic-body"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	alloc, allocCancel := chromedp.NewExecAllocator(context.Background(),
		chromedp.ExecPath(chrome), chromedp.Headless, chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck)
	defer allocCancel()
	ctx, cancel := chromedp.NewContext(alloc)
	s := &Session{ctx: ctx, cancel: cancel}
	if err := chromedp.Do(ctx); err != nil {
		t.Fatalf("initialize target: %v", err)
	}
	if err := s.StartObservation(); err != nil {
		t.Fatalf("start observation: %v", err)
	}
	defer func() { s.StopObservation(); cancel() }()
	loadCtx, loadCancel := context.WithTimeout(ctx, 15*time.Second)
	defer loadCancel()
	if err := chromedp.Do(loadCtx, chromedp.Navigate(server.URL), chromedp.WaitReady("body")); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	var requestID string
	deadline := time.Now().Add(5 * time.Second)
	for requestID == "" && time.Now().Before(deadline) {
		raw := inspectOK(t, s, "network.requests", `{"filter":"/data"}`)
		var got struct {
			Requests []observedRequest `json:"requests"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		for _, r := range got.Requests {
			if r.Status == http.StatusCreated && r.Finished {
				requestID = r.RequestID
			}
		}
		if requestID == "" {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if requestID == "" {
		t.Fatal("did not observe completed synthetic fetch")
	}
	body := inspectOK(t, s, "network.response", `{"requestId":`+quoteJSON(requestID)+`}`)
	if !strings.Contains(string(body), "synthetic-body") {
		t.Fatalf("response body result = %s", body)
	}

	console := inspectOK(t, s, "console", `{}`)
	errors := inspectOK(t, s, "errors", `{}`)
	if !strings.Contains(string(console), "synthetic-console") {
		t.Fatalf("console result = %s", console)
	}
	if !strings.Contains(string(errors), "synthetic-error") {
		t.Fatalf("errors result = %s", errors)
	}

	inspectOK(t, s, "cookies.set", `{"name":"synthetic","value":"cookie-value"}`)
	cookies := inspectOK(t, s, "cookies.get", `{"name":"synthetic"}`)
	if !strings.Contains(string(cookies), "cookie-value") {
		t.Fatalf("cookies result = %s", cookies)
	}
	inspectOK(t, s, "cookies.clear", `{"name":"synthetic"}`)
	cookies = inspectOK(t, s, "cookies.get", `{"name":"synthetic"}`)
	if strings.Contains(string(cookies), "cookie-value") {
		t.Fatalf("cookie remained after clear: %s", cookies)
	}

	inspectOK(t, s, "storage.set", `{"kind":"local","key":"synthetic","value":"storage-value","currentorigin":true}`)
	storage := inspectOK(t, s, "storage.get", `{"kind":"local","key":"synthetic"}`)
	if !strings.Contains(string(storage), "storage-value") {
		t.Fatalf("storage result = %s", storage)
	}
	inspectOK(t, s, "storage.clear", `{"kind":"local","key":"synthetic"}`)
}

func inspectOK(t *testing.T, s *Session, action, params string) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := s.Inspect(ctx, action, json.RawMessage(params))
	if err != nil {
		t.Fatalf("Inspect(%s): %v", action, err)
	}
	return raw
}

func quoteJSON(value string) string {
	b, _ := json.Marshal(value)
	return string(b)
}
