//go:build unix

package browserhost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestServerCooldownSurvivesExplicitRecoveryIntegration(t *testing.T) {
	chrome := hostChrome(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			w.WriteHeader(204)
			return
		}
		requests.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
		fmt.Fprint(w, "<title>Retry later</title>Temporarily unavailable")
	}))
	defer server.Close()
	opts := DefaultOptions()
	opts.Chrome, opts.URL, opts.Private, opts.Socket = chrome, server.URL, true, hostTempSocket(t)
	h := startHost(t, opts)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	before := requests.Load()
	got := hostActionRaw(t, ctx, h.Socket(), `{"id":"retry","action":"reload","params":{}}`)
	if got.OK || got.Error == nil || got.Error.Code != "browser_paused" {
		t.Fatalf("document 429 did not prevent automatic reload: %+v", got)
	}
	s := h.bridge.Interaction()
	if s.RetryAfterMS <= 0 || s.PauseSource != "detector" {
		t.Fatalf("server cooldown was not retained: %+v", s)
	}
	err := h.ContinueChecked(ctx, ContinueRequest{BrowserID: s.BrowserID, InstanceEpoch: s.InstanceEpoch, PauseGeneration: s.PauseGeneration, RequestID: "local-recovery-during-cooldown", ExpiresAt: time.Now().Add(time.Minute)})
	if err == nil || requests.Load() != before || !h.bridge.Interaction().Paused {
		t.Fatal("explicit recovery ignored Retry-After or dispatched another request")
	}
}
