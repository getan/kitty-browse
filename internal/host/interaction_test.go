package host

import "testing"

func TestInteractionStatesAndPause(t *testing.T) {
	b := NewBridge("fixture")
	read, err := b.beginInteraction("snapshot", false)
	if err != nil {
		t.Fatal(err)
	}
	write, err := b.beginInteraction("click", true)
	if err != nil {
		t.Fatal(err)
	}
	if s := b.Interaction(); !s.Busy || s.ReadOnly || s.Action != "click" {
		t.Fatalf("write state: %+v", s)
	}
	b.Pause("rate_limited")
	if _, err := b.beginInteraction("reload", true); err == nil {
		t.Fatal("pause allowed another mutation")
	}
	observe, err := b.beginInteraction("get", false)
	if err != nil {
		t.Fatal("pause must allow observation")
	}
	observe()
	write()
	read()
	if s := b.Interaction(); !s.Paused || s.Busy || s.Reason != "rate_limited" {
		t.Fatalf("pause was lost on completion: %+v", s)
	}
	b.Resume()
	if s := b.Interaction(); s.Paused {
		t.Fatal("explicit resume did not clear pause")
	}
	done, err := b.beginInteraction("fill", true)
	if err != nil {
		t.Fatal(err)
	}
	done()
	if s := b.Interaction(); !s.Completed || s.Busy {
		t.Fatalf("completion state: %+v", s)
	}
	epoch := b.pauseEpoch()
	b.Pause("manual")
	b.Resume()
	if _, err := b.beginInteractionAt("click", true, epoch); err == nil {
		t.Fatal("resume replayed a mutation queued before the pause")
	}
}
