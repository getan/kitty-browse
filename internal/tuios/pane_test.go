package tuios

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestDisabledOutsideTuios(t *testing.T) {
	p := FromEnv(func(k string) string { return "" })
	if p.Enabled() {
		t.Fatal("enabled outside tuios")
	}
	p.Rename("x") // must be a no-op
}

func TestRenameArgs(t *testing.T) {
	p := &Pane{ID: "w1", Binary: "/bin/tuios", enabled: true}
	var mu sync.Mutex
	var got []string
	p.run = func(_ context.Context, argv []string) error {
		mu.Lock()
		got = append([]string(nil), argv...)
		mu.Unlock()
		return nil
	}
	p.Rename("Example title")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 || got[0] != "set-window" || got[2] != "w1" || got[4] != "Example title" {
		t.Fatalf("argv = %v", got)
	}
}
