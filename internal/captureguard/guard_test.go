package captureguard

import "testing"

func TestGuardLifecycle(t *testing.T) {
	end := Begin("target")
	if !Active("target") {
		t.Fatal("capture was not active")
	}
	end()
	end()
	if !Active("target") {
		t.Fatal("capture grace period ended immediately")
	}
	if Active("") {
		t.Fatal("empty target must never suppress frames")
	}
}
