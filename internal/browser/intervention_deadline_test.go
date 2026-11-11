package browser

import (
	"net/http"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
)

func TestRetryAfterDeadline(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		value string
		want  time.Time
	}{
		{"30", now.Add(30 * time.Second)},
		{now.Add(time.Minute).Format(http.TimeFormat), now.Add(time.Minute)},
		{"-1", time.Time{}},
		{"1.5", time.Time{}},
		{"invalid", time.Time{}},
	} {
		if got := responseRetryAfter(network.Headers{"retry-after": tc.value, "Set-Cookie": "must-not-be-retained"}, now); !got.Equal(tc.want) {
			t.Fatalf("%q: got %v, want %v", tc.value, got, tc.want)
		}
	}
	if got := responseRetryAfter(network.Headers{"Retry-After": "9223372036854775807"}, now); !got.After(now.Add(365 * 24 * time.Hour)) {
		t.Fatal("huge cooldown overflowed to the past")
	}
}
