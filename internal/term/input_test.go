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

func TestParseMouse(t *testing.T) {
	got, err := Parse([]byte("\x1b[<0;10;5M\x1b[<0;10;5m\x1b[<64;10;5M"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{
		{Kind: KeyMouse, Mouse: MouseEvent{Button: 0, Col: 10, Row: 5, Press: true}},
		{Kind: KeyMouse, Mouse: MouseEvent{Button: 0, Col: 10, Row: 5, Press: false}},
		{Kind: KeyMouse, Mouse: MouseEvent{Button: 0, Col: 10, Row: 5, Press: true, Wheel: -1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}
