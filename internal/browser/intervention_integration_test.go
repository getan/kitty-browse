package browser

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestVisibleInterventionIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME for Chromium integration")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/limit":
			fmt.Fprint(w, `<div role="dialog">操作过快，请慢一点[5014][502]</div>`)
		case "/challenge":
			fmt.Fprint(w, `<div role="dialog">请完成下列验证后继续</div>`)
		case "/hidden":
			fmt.Fprint(w, `<div role="dialog" style="display:none">操作过快</div><input>`)
		case "/http429":
			w.WriteHeader(429)
			fmt.Fprint(w, `<title>Retry later</title>Temporarily unavailable`)
		default:
			fmt.Fprint(w, `<h1>How to fix too many requests and CAPTCHA</h1><input placeholder="手机号"><button>登录</button>`)
		}
	}))
	defer server.Close()
	ctx, cancel := integrationAllocator(t, chrome)
	defer cancel()
	s, err := Open(ctx, server.URL, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct{ path, want string }{
		{"/", ""}, {"/limit", "rate_limited"}, {"/challenge", "captcha"}, {"/hidden", ""}, {"/http429", "rate_limited"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if err := s.Navigate(s.Context(), server.URL+tc.path, 5*time.Second); err != nil {
				t.Fatal(err)
			}
			if reason, err := s.Intervention(s.Context()); err != nil || reason != tc.want {
				t.Fatalf("reason=%q want=%q err=%v", reason, tc.want, err)
			}
		})
	}
}
