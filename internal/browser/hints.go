package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chromedp/chromedp"
)

// Hint is one clickable/focusable target. CX/CY are viewport CSS pixels.
type Hint struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	CX    int    `json:"cx"`
	CY    int    `json:"cy"`
	Kind  string `json:"kind"`
}

const hintChars = "asdfjkl"

// HintLabels returns at most n labels: first the home-row keys, then pairs.
func HintLabels(n int) []string {
	if n <= 0 {
		return nil
	}
	var out []string
	for _, a := range hintChars {
		out = append(out, string(a))
		if len(out) == n {
			return out
		}
	}
	for _, a := range hintChars {
		for _, b := range hintChars {
			out = append(out, string([]rune{a, b}))
			if len(out) == n {
				return out
			}
		}
	}
	return out
}

// Hints collects visible clickable/focusable elements and injects a label
// overlay into the page. The overlay is part of the next rendered frame.
func (s *Session) Hints(ctx context.Context, max int) ([]Hint, error) {
	if max <= 0 {
		max = 49
	}
	script := strings.Replace(hintScript, "__MAX__", fmt.Sprint(max), 1)
	raw, err := chromedp.Run(ctx, chromedp.Evaluate[string](script))
	if err != nil {
		return nil, fmt.Errorf("collect hints: %w", err)
	}
	var hints []Hint
	if err := json.Unmarshal([]byte(raw), &hints); err != nil {
		return nil, fmt.Errorf("decode hints: %w", err)
	}
	return hints, nil
}

// ClearHints removes the page overlay and the data attributes.
func (s *Session) ClearHints(ctx context.Context) error {
	_, err := chromedp.Run(ctx, chromedp.Evaluate[any](clearHintsScript))
	return err
}

// hintScript is a single evaluate call: collect geometry, generate labels,
// inject the yellow overlay, and return the hint list.
const hintScript = `(() => {
  const old = document.getElementById('__kitty_browse_hints');
  if (old) old.remove();
  for (const el of document.querySelectorAll('[data-kitty-browse-hint]')) {
    el.removeAttribute('data-kitty-browse-hint');
  }
  const selector = 'a, button, [role="button"], [onclick], summary, [role="link"], [role="tab"], input, textarea, select, [contenteditable]';
  const chars = ['a','s','d','f','j','k','l'];
  const elems = [];
  for (const el of document.querySelectorAll(selector)) {
    if (elems.length >= __MAX__) break;
    const style = getComputedStyle(el);
    if (style.display === 'none' || style.visibility === 'hidden' ||
        style.pointerEvents === 'none' || Number(style.opacity) === 0) continue;

    let point = null;
    for (const r of el.getClientRects()) {
      const left = Math.max(0, r.left);
      const top = Math.max(0, r.top);
      const right = Math.min(innerWidth, r.right);
      const bottom = Math.min(innerHeight, r.bottom);
      if (right <= left || bottom <= top) continue;
      const cx = Math.round((left + right) / 2);
      const cy = Math.round((top + bottom) / 2);
      const hit = document.elementFromPoint(cx, cy);
      if (hit === el || el.contains(hit)) {
        point = {x: Math.round(left), y: Math.round(top), cx, cy};
        break;
      }
    }
    if (!point) continue;

    const focusable = el.matches('input, textarea, select, [contenteditable]');
    const id = String(elems.length);
    el.setAttribute('data-kitty-browse-hint', id);
    elems.push({id, label: '', ...point, kind: focusable ? 'focus' : 'click'});
  }

  const labels = [];
  if (elems.length <= chars.length) {
    for (let i = 0; i < elems.length; i++) labels.push(chars[i]);
  } else {
    for (const a of chars) for (const b of chars) {
      labels.push(a + b);
      if (labels.length >= elems.length) break;
    }
  }
  const box = document.createElement('div');
  box.id = '__kitty_browse_hints';
  box.style.cssText = 'position:fixed;top:0;left:0;width:0;height:0;z-index:2147483647;pointer-events:none;';
  for (let i = 0; i < elems.length; i++) {
    elems[i].label = labels[i];
    const span = document.createElement('span');
    span.textContent = labels[i].toUpperCase();
    span.style.cssText = 'position:fixed;background:#FFEE00;color:#000;font:bold 12px monospace;border:1px solid #C38A00;border-radius:3px;padding:0 2px;z-index:2147483647;pointer-events:none;line-height:1.4;left:' + elems[i].x + 'px;top:' + elems[i].y + 'px;';
    box.appendChild(span);
  }
  document.documentElement.appendChild(box);
  return JSON.stringify(elems);
})()`

const clearHintsScript = `(() => {
  const box = document.getElementById('__kitty_browse_hints');
  if (box) box.remove();
  for (const el of document.querySelectorAll('[data-kitty-browse-hint]')) {
    el.removeAttribute('data-kitty-browse-hint');
  }
})()`
