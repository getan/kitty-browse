package term

import (
	"reflect"
	"testing"
)

func TestParseKeys(t *testing.T) {
	got, err := Parse([]byte("ab\r\x7f\x1b[A\x1b[1;3D\x1bf"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{
		{Kind: KeyText, Rune: 'a'},
		{Kind: KeyText, Rune: 'b'},
		{Kind: KeyEnter},
		{Kind: KeyBackspace},
		{Kind: KeyUp},
		{Kind: KeyAltLeft},
		{Kind: KeyAltF},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestParseControlKeys(t *testing.T) {
	got, err := Parse([]byte{0x0c, 0x12, 0x03})
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{{Kind: KeyCtrlL}, {Kind: KeyCtrlR}, {Kind: KeyCtrlC}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}
