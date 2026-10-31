package browser

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Pointer focus and the keyboard gate must agree. Automation fill does not use
// this gate, so a working fill alone cannot detect broken manual typing.
func TestEditableFocusNestedInputsIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run real browser input tests")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/frame" {
			fmt.Fprint(w, `<body style="margin:0"><input id="field" style="width:300px;height:60px">`)
			return
		}
		fmt.Fprint(w, `<!doctype html><body style="margin:0">
<input id="plain" style="position:absolute;left:0;top:0;width:300px;height:60px">
<div id="host" style="position:absolute;left:0;top:100px"></div>
<iframe id="frame" src="/frame" style="position:absolute;left:0;top:200px;width:320px;height:80px;border:0"></iframe>
<script>
const root = document.querySelector('#host').attachShadow({mode:'open'});
root.innerHTML = '<div id="inner"></div>';
root.querySelector('#inner').attachShadow({mode:'open'}).innerHTML =
  '<input id="field" style="width:300px;height:60px">';
</script>`)
	}))
	defer server.Close()
	ctx, cancel := integrationAllocator(t, chrome)
	defer cancel()
	s, err := Open(ctx, server.URL, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		name, field string
		y           float64
	}{
		{"plain", `document.querySelector('#plain')`, 30},
		{"nested-shadow", `document.querySelector('#host').shadowRoot.querySelector('#inner').shadowRoot.querySelector('#field')`, 130},
		{"same-origin-frame", `document.querySelector('#frame').contentDocument.querySelector('#field')`, 230},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Click(80, tc.y); err != nil {
				t.Fatal(err)
			}
			if focused, err := s.EditableFocus(s.Context()); err != nil || !focused {
				t.Fatalf("clicked input rejected by keyboard gate: focused=%v err=%v", focused, err)
			}
			if err := s.InsertText("a中文"); err != nil {
				t.Fatal(err)
			}
			if got := evalPageString(t, s, tc.field+`.value`); got != "a中文" {
				t.Fatalf("manual insertion = %q", got)
			}
			if err := s.KeyPress("Backspace"); err != nil {
				t.Fatal(err)
			}
			if got := evalPageString(t, s, tc.field+`.value`); got != "a中" {
				t.Fatalf("manual backspace = %q", got)
			}
			if err := s.BlurFocused(s.Context()); err != nil {
				t.Fatal(err)
			}
			if focused, err := s.EditableFocus(s.Context()); err != nil || focused {
				t.Fatalf("Escape did not release nested input: focused=%v err=%v", focused, err)
			}
		})
	}
}
