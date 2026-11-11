package browserhost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/getan/kitty-browse/internal/host"
)

type ContinueRequest = host.ContinueRequest
type InteractionState = host.InteractionState

// ViewAttestation is returned over the authenticated management connection,
// never by a page. It proves runtime facts, not Golder policy approval.
type ViewAttestation struct {
	ViewID              string `json:"viewId"`
	BrowserID           string `json:"browserId"`
	InstanceEpoch       string `json:"instanceEpoch"`
	Socket              string `json:"socket"`
	LoginProfile        string `json:"loginProfile"`
	ProxyMode           string `json:"proxyMode"`
	ChromiumSandbox     bool   `json:"chromiumSandbox"`
	Challenge           string `json:"challenge"`
	RuntimeEnvelope     string `json:"runtimeEnvelope"`
	RuntimePolicyDigest string `json:"runtimePolicyDigest"`
}

// RuntimePolicyDigest identifies the broker's immutable execution configuration,
// not any embedding application's permission decision or outer OS sandbox.
func RuntimePolicyDigest(opts Options) string {
	raw, _ := json.Marshal([]any{opts.ProfileDir, opts.LoginProfile, opts.ProxyServer, opts.RequireSandbox})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (h *Host) ContinueChecked(ctx context.Context, req ContinueRequest) error {
	if h == nil || h.bridge == nil {
		return errors.New("browser unavailable")
	}
	return h.bridge.ContinueChecked(ctx, req)
}

func (v *RemoteView) ContinueChecked(ctx context.Context, req ContinueRequest) error {
	if req.BrowserID != v.BrowserID {
		return errors.New("continuation belongs to another view")
	}
	_, err := sharedCall(ctx, v.hub, v.key, "/continue", sharedRequest{ID: v.id, Continuation: &req})
	return err
}

func (v *RemoteView) ContinuationOutcome(ctx context.Context, req ContinueRequest) (string, error) {
	raw, err := sharedCall(ctx, v.hub, v.key, "/continuation-outcome", sharedRequest{ID: v.id, Continuation: &req})
	if err != nil {
		return "unknown", err
	}
	var result struct {
		Outcome string `json:"outcome"`
	}
	err = json.Unmarshal(raw, &result)
	return result.Outcome, err
}

func (v *RemoteView) Attest(ctx context.Context) (ViewAttestation, error) {
	challenge := rand.Text()
	raw, err := sharedCall(ctx, v.hub, v.key, "/attest", sharedRequest{ID: v.id, Challenge: challenge})
	if err != nil {
		return ViewAttestation{}, err
	}
	var result ViewAttestation
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	if result.Challenge != challenge || result.ViewID != v.id || result.Socket != v.Socket || result.BrowserID != v.BrowserID || result.InstanceEpoch != v.BrowserID {
		return ViewAttestation{}, errors.New("browser_instance_changed")
	}
	return result, nil
}
