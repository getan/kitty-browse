package browser

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
)

// Retain no headers: only the parsed Retry-After deadline is projected into
// observation state. A server's very long cooldown must not overflow to a past
// date and accidentally enable another request.
func responseRetryAfter(headers network.Headers, now time.Time) time.Time {
	for name, value := range headers {
		if !strings.EqualFold(name, "Retry-After") {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return time.Time{}
		}
		text = strings.TrimSpace(text)
		if seconds, err := strconv.ParseUint(text, 10, 64); err == nil {
			if seconds > uint64((1<<63-1)/int64(time.Second)) {
				return time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			return now.Add(time.Duration(seconds) * time.Second)
		}
		if text != "" && strings.Trim(text, "0123456789") == "" {
			return time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		if deadline, err := http.ParseTime(text); err == nil && deadline.After(now) {
			return deadline
		}
	}
	return time.Time{}
}
