package browserhost

import (
	"math"
	"strings"
	"testing"
)

func TestValidateOperatorInput(t *testing.T) {
	base := OperatorInput{BrowserID: "browser", TabID: "tab", TargetID: "target", Kind: "pointer-down", X: 10, Y: 20}
	for _, tc := range []struct {
		name  string
		event OperatorInput
		valid bool
	}{
		{"pointer", base, true},
		{"missing identity", OperatorInput{Kind: "pointer-down"}, false},
		{"nonfinite coordinate", func() OperatorInput { e := base; e.X = math.NaN(); return e }(), false},
		{"invalid scroll", func() OperatorInput { e := base; e.Kind = "scroll"; e.DeltaY = math.Inf(1); return e }(), false},
		{"text", func() OperatorInput { e := base; e.Kind = "text"; e.Text = "中文"; return e }(), true},
		{"oversized text", func() OperatorInput { e := base; e.Kind = "text"; e.Text = strings.Repeat("a", 4097); return e }(), false},
		{"editing key", func() OperatorInput { e := base; e.Kind = "key"; e.Key = "Backspace"; return e }(), true},
		{"unsupported key", func() OperatorInput { e := base; e.Kind = "key"; e.Key = "arbitrary"; return e }(), false},
		{"script refused", func() OperatorInput { e := base; e.Kind = "eval"; return e }(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateOperatorInput(tc.event); (err == nil) != tc.valid {
				t.Fatalf("validation error = %v, valid = %v", err, tc.valid)
			}
		})
	}
}
