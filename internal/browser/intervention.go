package browser

import (
	"context"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// Intervention reports only explicit, visible verification/limit UI or a 429
// on the current document. It never returns page text or treats an unrelated
// image/analytics 429, login form or an article mentioning CAPTCHAs as a block.
func (s *Session) Intervention(ctx context.Context) (string, error) {
	info, err := s.InterventionDetails(ctx)
	return info.Reason, err
}

type InterventionInfo struct {
	Reason     string
	RetryAfter time.Time
}

func (s *Session) InterventionDetails(ctx context.Context) (InterventionInfo, error) {
	if err := ctx.Err(); err != nil {
		return InterventionInfo{}, err
	}
	op, cancel := s.inspectContext(ctx)
	defer cancel()
	type result struct {
		Reason string `json:"reason"`
		URL    string `json:"url"`
	}
	got, err := chromedp.Run(op, chromedp.Evaluate[result](`(() => {
  const visible = el => {
    const r = el.getBoundingClientRect(), s = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && r.bottom > 0 && r.right > 0 &&
      r.top < innerHeight && r.left < innerWidth && s.display !== 'none' &&
      s.visibility !== 'hidden' && Number(s.opacity) !== 0;
  };
  const result = reason => ({reason, url: location.href.split('#')[0]});
  for (const el of document.querySelectorAll('iframe[src*="captcha"],iframe[src*="verifycenter"],.geetest_panel,.captcha_verify_container')) {
    if (visible(el)) return result('captcha');
  }
  for (const el of document.querySelectorAll('[role="alert"],[role="dialog"],dialog[open],.captcha-verify-container')) {
    if (!visible(el)) continue;
    const text = (el.innerText || '').slice(0, 2048);
    if (/操作过快|操作频繁|请求过于频繁|too many requests|rate limit exceeded|\[5014\]\[502\]/i.test(text)) return result('rate_limited');
    if (/请完成.*验证|拖动.*滑块|verify (that )?you are human|complete the captcha/i.test(text)) return result('captcha');
  }
  return result('');
})()`))
	if err != nil {
		return InterventionInfo{}, err
	}
	info := InterventionInfo{Reason: got.Reason}
	if value, ok := sessionObservers.Load(s); ok {
		o := value.(*observer)
		o.mu.Lock()
		defer o.mu.Unlock()
		for i := len(o.requests) - 1; i >= 0; i-- {
			r := o.requests[i]
			if string(r.ResourceType) == "Document" && strings.Split(r.URL, "#")[0] == got.URL {
				if r.Status == 429 {
					if info.Reason == "" {
						info.Reason = "rate_limited"
					}
					info.RetryAfter = r.retryAfter
				}
				break
			}
		}
	}
	return info, nil
}
