package browser

import (
	"context"
	"encoding/json"

	"github.com/chromedp/cdproto/cdp"
)

// snapshotNodeVisibility is best-effort presentation evidence, not authority
// to select or click a different target. Unknown stays nil, never false.
func snapshotNodeVisibility(ctx context.Context, t cdp.Session, backend int64) *bool {
	r, err := call[map[string]any, struct {
		Object remoteObject `json:"object"`
	}](ctx, t, "DOM.resolveNode", map[string]any{"backendNodeId": backend})
	if err != nil || r.Object.ObjectID == "" {
		return nil
	}
	defer release(ctx, t, r.Object.ObjectID)
	value, err := callFunction(ctx, t, r.Object.ObjectID, `function(){
const el=this.nodeType===Node.ELEMENT_NODE?this:this.nodeType===Node.TEXT_NODE?this.parentElement:null;
if(!el||!el.isConnected)return null;
for(let p=el;p;p=p.parentElement){const s=getComputedStyle(p);if(s.display==='none'||s.visibility==='hidden'||s.visibility==='collapse'||s.contentVisibility==='hidden'||Number(s.opacity)===0)return false}
const style=getComputedStyle(el);if(style.clipPath!=='none'||style.clip!=='auto')return null;
if(this.nodeType===Node.TEXT_NODE){const r=document.createRange();r.selectNodeContents(this);const b=r.getBoundingClientRect();return b.width>0&&b.height>0}
const b=el.getBoundingClientRect();return b.width>0&&b.height>0
}`, nil, true)
	if err != nil || string(value.Value) == "null" {
		return nil
	}
	var visible bool
	if json.Unmarshal(value.Value, &visible) != nil {
		return nil
	}
	return &visible
}
