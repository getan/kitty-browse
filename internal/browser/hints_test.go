package browser

import "testing"

func TestHintLabels(t *testing.T) {
	labels := HintLabels(9)
	want := []string{"a", "s", "d", "f", "j", "k", "l", "aa", "as"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v", labels)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("labels[%d] = %q, want %q", i, labels[i], want[i])
		}
	}
	if got := HintLabels(0); got != nil {
		t.Fatalf("zero labels = %v", got)
	}
}
