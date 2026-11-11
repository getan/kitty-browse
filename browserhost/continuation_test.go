package browserhost

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getan/kitty-browse/internal/host"
)

func TestCheckedRecoveryUsesOnlyExactPrivateViewCapability(t *testing.T) {
	aID, bID := ViewID("conversation-a", "default"), ViewID("conversation-b", "default")
	a, b := host.NewBridge("browser-a"), host.NewBridge("browser-b")
	a.Pause("manual")
	b.Pause("manual")
	opts := DefaultOptions()
	opts.RequireSandbox, opts.LoginProfile, opts.ProfileDir = true, "project/default", t.TempDir()
	s := &sharedServer{opts: SharedOptions{Browser: opts}, token: "master-private", records: map[string]sharedRecord{
		aID: {view: &Host{bridge: a, socket: "a.sock"}, key: "operator-a"},
		bID: {view: &Host{bridge: b, socket: "b.sock"}, key: "operator-b"},
	}}
	req := ContinueRequest{BrowserID: a.ID, InstanceEpoch: a.ID, PauseGeneration: a.Interaction().PauseGeneration, RequestID: "local-person-decision", ExpiresAt: time.Now().Add(time.Minute)}
	call := func(path, key, id string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(sharedRequest{ID: id, Continuation: &req, Challenge: "random-private-challenge"})
		r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/continue", "/continuation-outcome", "/attest"} {
		for _, key := range []string{"", "master-private", "operator-b", "public-request-id"} {
			if got := call(path, key, aID); got.Code != 403 {
				t.Fatalf("%s accepted %q: %d", path, key, got.Code)
			}
		}
	}
	proofReply := call("/attest", "operator-a", aID)
	var proof ViewAttestation
	if proofReply.Code != 200 || json.Unmarshal(proofReply.Body.Bytes(), &proof) != nil || proof.ViewID != aID || proof.InstanceEpoch != a.ID || proof.Challenge != "random-private-challenge" || proof.RuntimeEnvelope != "browser_runtime" || proof.RuntimePolicyDigest != RuntimePolicyDigest(opts) {
		t.Fatalf("incomplete private attestation: %s", proofReply.Body.String())
	}
	if got := call("/continue", "operator-a", aID); got.Code != 200 || a.Interaction().Paused || !b.Interaction().Paused {
		t.Fatalf("exact private decision failed or crossed views: %s", got.Body.String())
	}
	a.Pause("captcha")
	if got := call("/continue", "operator-a", aID); got.Code != 200 || !a.Interaction().Paused {
		t.Fatal("receipt replay cleared a newer pause")
	}
	if got := call("/continue", "operator-a", bID); got.Code != 403 || !b.Interaction().Paused {
		t.Fatal("one view's capability controlled another view")
	}
	a.CloseInteraction()
	if got := call("/continuation-outcome", "operator-a", aID); !strings.Contains(got.Body.String(), "stale") {
		t.Fatal("closed view retained usable receipts")
	}
}
