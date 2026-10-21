package browser

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"example.com":            "https://example.com",
		"https://example.com/x":  "https://example.com/x",
		"http://localhost:3000":  "http://localhost:3000",
		"file:///tmp/index.html": "file:///tmp/index.html",
	}
	for in, want := range cases {
		got, err := Normalize(in)
		if err != nil {
			t.Fatalf("Normalize(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "ftp://example.com", "https://"} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) succeeded, want error", bad)
		}
	}
}
