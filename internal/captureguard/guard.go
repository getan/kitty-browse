// Package captureguard coordinates CDP screenshots with screencast consumers.
// Chromium may emit transient or intermediate surfaces while capturing; those
// are not page animation frames and must not replace the visible terminal image.
package captureguard

import (
	"sync"
	"time"
)

const grace = 300 * time.Millisecond

type entry struct {
	count int
	until time.Time
}

var (
	mu      sync.Mutex
	targets = make(map[string]*entry)
)

// Begin suppresses screencast frames for target until end is called plus a
// short compositor grace period. The returned function is idempotent.
func Begin(target string) func() {
	if target == "" {
		return func() {}
	}
	mu.Lock()
	state := targets[target]
	if state == nil {
		state = &entry{}
		targets[target] = state
	}
	state.count++
	mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			state.count--
			state.until = time.Now().Add(grace)
			mu.Unlock()
		})
	}
}

// Active reports whether a CDP capture owns the target's compositor surface.
func Active(target string) bool {
	mu.Lock()
	defer mu.Unlock()
	state, ok := targets[target]
	if !ok {
		return false
	}
	active := state.count > 0 || time.Now().Before(state.until)
	if !active {
		delete(targets, target)
	}
	return active
}
