package host

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/getan/kitty-browse/internal/action"
)

func TestOnlyMutatingActionsBlockManualInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params string
		blocks bool
	}{
		{"info", `{}`, false},
		{"tab.list", `{}`, false},
		{"tab.new", `{"url":"about:blank"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := testBridge(nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				raw, _ := json.Marshal(action.Request{ID: "r", Action: tc.name, Params: json.RawMessage(tc.params)})
				_, _ = b.Execute(ctx, raw)
			}()
			select {
			case call := <-b.Calls:
				if b.Active.Load() != 1 || (b.InputBlocked.Load() > 0) != tc.blocks {
					t.Errorf("%s: active=%d inputBlocked=%d", tc.name, b.Active.Load(), b.InputBlocked.Load())
				}
				call.Reply <- action.Respond(call.Req, b.Load().Info, action.TabInfo{}, json.RawMessage(`{}`), nil)
			case <-ctx.Done():
				t.Fatal("action did not reach the host")
			}
			<-done
			if b.Active.Load() != 0 || b.InputBlocked.Load() != 0 {
				t.Fatal("action left input ownership behind")
			}
		})
	}
}
