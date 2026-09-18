package term

import (
	"strings"
	"testing"
	"time"
)

func collect(t *testing.T, input string) []Event {
	t.Helper()
	r := NewReader(strings.NewReader(input))
	defer r.Close()
	var out []Event
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-r.Events():
			out = append(out, ev)
		case <-timeout:
			return out
		case <-time.After(120 * time.Millisecond):
			return out
		}
	}
}

func TestArrowAndControlKeys(t *testing.T) {
	evs := collect(t, "\x1b[A\x1b[B\x1b[C\x1b[D\r\n\t\x7f\x1b")
	want := []string{"up", "down", "right", "left", "enter", "enter", "tab", "backspace", "esc"}
	if len(evs) != len(want) {
		t.Fatalf("got %d events (%+v), want %d", len(evs), evs, len(want))
	}
	for i, w := range want {
		if evs[i].Kind != EvKey || !evs[i].Key.Is(w) {
			t.Errorf("event %d = %+v, want %q", i, evs[i], w)
		}
	}
}

func TestPrintableAndCtrl(t *testing.T) {
	evs := collect(t, "qQ 1\x03\x13")
	if len(evs) != 6 {
		t.Fatalf("got %d events: %+v", len(evs), evs)
	}
	if evs[0].Key.Rune != 'q' || !evs[0].Key.IsRune() {
		t.Errorf("q decoded as %+v", evs[0])
	}
	if evs[1].Key.Rune != 'Q' {
		t.Errorf("Q decoded as %+v", evs[1])
	}
	if evs[2].Key.Rune != ' ' {
		t.Errorf("space decoded as %+v", evs[2])
	}
	if evs[3].Key.Rune != '1' {
		t.Errorf("1 decoded as %+v", evs[3])
	}
	if !evs[4].Key.Ctrl || !evs[4].Key.Is("c") {
		t.Errorf("ctrl-c decoded as %+v", evs[4])
	}
	if !evs[5].Key.Ctrl || !evs[5].Key.Is("s") {
		t.Errorf("ctrl-s decoded as %+v", evs[5])
	}
}

func TestAltKeys(t *testing.T) {
	evs := collect(t, "\x1bx")
	if len(evs) != 1 {
		t.Fatalf("got %+v", evs)
	}
	if !evs[0].Key.Alt || evs[0].Key.Rune != 'x' {
		t.Fatalf("alt-x decoded as %+v", evs[0])
	}
}

func TestFunctionAndEditingKeys(t *testing.T) {
	evs := collect(t, "\x1bOP\x1b[15~\x1b[3~\x1b[5~\x1b[6~\x1bOH\x1bOF\x1b[Z")
	want := []string{"f1", "f5", "delete", "pgup", "pgdn", "home", "end", "shift-tab"}
	if len(evs) != len(want) {
		t.Fatalf("got %d (%+v), want %d", len(evs), evs, len(want))
	}
	for i, w := range want {
		if !evs[i].Key.Is(w) {
			t.Errorf("event %d = %q, want %q", i, evs[i].Key.Name, w)
		}
	}
}

func TestModifiedArrowKeys(t *testing.T) {
	evs := collect(t, "\x1b[1;5C\x1b[1;3A")
	if len(evs) != 2 {
		t.Fatalf("got %+v", evs)
	}
	if !evs[0].Key.Ctrl || !evs[0].Key.Is("right") {
		t.Errorf("ctrl-right = %+v", evs[0].Key)
	}
	if !evs[1].Key.Alt || !evs[1].Key.Is("up") {
		t.Errorf("alt-up = %+v", evs[1].Key)
	}
}

func TestSGRMouse(t *testing.T) {
	evs := collect(t, "\x1b[<0;10;5M\x1b[<0;10;5m\x1b[<64;3;2M\x1b[<65;3;2M\x1b[<32;7;8M")
	if len(evs) != 5 {
		t.Fatalf("got %d events: %+v", len(evs), evs)
	}
	press := evs[0].Mouse
	if evs[0].Kind != EvMouse || !press.Press || press.X != 9 || press.Y != 4 || press.Button != 0 {
		t.Errorf("press = %+v", evs[0])
	}
	release := evs[1].Mouse
	if !release.Release || release.X != 9 || release.Y != 4 {
		t.Errorf("release = %+v", evs[1])
	}
	if evs[2].Mouse.Wheel != -1 {
		t.Errorf("wheel up = %+v", evs[2].Mouse)
	}
	if evs[3].Mouse.Wheel != 1 {
		t.Errorf("wheel down = %+v", evs[3].Mouse)
	}
	if !evs[4].Mouse.Motion || evs[4].Mouse.X != 6 || evs[4].Mouse.Y != 7 {
		t.Errorf("motion = %+v", evs[4].Mouse)
	}
}

func TestUTF8Input(t *testing.T) {
	evs := collect(t, "éя中")
	if len(evs) != 3 {
		t.Fatalf("got %+v", evs)
	}
	for i, want := range []rune{'é', 'я', '中'} {
		if evs[i].Key.Rune != want {
			t.Errorf("event %d = %q, want %q", i, evs[i].Key.Rune, want)
		}
	}
}

func TestKeyIsHelper(t *testing.T) {
	if !(Key{Rune: 'q'}).Is("q") {
		t.Error("literal rune should match its name")
	}
	if (Key{Rune: 'q'}).Is("up") {
		t.Error("literal rune should not match a named key")
	}
	if !(Key{Name: "up"}).Is("up") {
		t.Error("named key should match")
	}
	if (Key{Name: "up", Rune: 'u'}).IsRune() {
		t.Error("named keys are not printable input")
	}
}

// Hovering with no button held arrives as SGR code 35 (motion + "no button").
// It used to be decoded as a button release, which silently disabled hover.
func TestSGRMouseHoverIsNotARelease(t *testing.T) {
	evs := collect(t, "\x1b[<35;12;7M\x1b[<35;13;7M")
	if len(evs) != 2 {
		t.Fatalf("got %d events: %+v", len(evs), evs)
	}
	for i, ev := range evs {
		m := ev.Mouse
		if ev.Kind != EvMouse {
			t.Fatalf("event %d is not a mouse event: %+v", i, ev)
		}
		if !m.Motion {
			t.Errorf("event %d: hover must be reported as motion: %+v", i, m)
		}
		if m.Release || m.Press {
			t.Errorf("event %d: hover must be neither press nor release: %+v", i, m)
		}
		if m.Button != -1 {
			t.Errorf("event %d: hover has no button, got %d", i, m.Button)
		}
	}
	if evs[0].Mouse.X != 11 || evs[0].Mouse.Y != 6 || evs[1].Mouse.X != 12 {
		t.Fatalf("hover coordinates wrong: %+v %+v", evs[0].Mouse, evs[1].Mouse)
	}
}

func TestSGRMouseDragKeepsTheButton(t *testing.T) {
	evs := collect(t, "\x1b[<32;5;5M") // motion with the left button held
	if len(evs) != 1 {
		t.Fatalf("got %+v", evs)
	}
	m := evs[0].Mouse
	if !m.Motion || m.Button != 0 || m.Press || m.Release {
		t.Fatalf("drag decoded as %+v", m)
	}
}

func TestSGRMouseReleaseAndPress(t *testing.T) {
	evs := collect(t, "\x1b[<0;4;4M\x1b[<0;4;4m\x1b[<2;4;4M")
	if len(evs) != 3 {
		t.Fatalf("got %+v", evs)
	}
	if !evs[0].Mouse.Press || evs[0].Mouse.Button != 0 || evs[0].Mouse.Motion {
		t.Errorf("press = %+v", evs[0].Mouse)
	}
	if !evs[1].Mouse.Release {
		t.Errorf("release = %+v", evs[1].Mouse)
	}
	if !evs[2].Mouse.Press || evs[2].Mouse.Button != 2 {
		t.Errorf("right press = %+v", evs[2].Mouse)
	}
}

func TestLegacyX10Mouse(t *testing.T) {
	// ESC [ M followed by button+32, x+32, y+32.
	evs := collect(t, "\x1b[M\x20\x2a\x25") // ' '=32 -> button 0, '*'=42 -> x 9, '%'=37 -> y 4
	if len(evs) != 1 {
		t.Fatalf("got %+v", evs)
	}
	m := evs[0].Mouse
	if !m.Press || m.Button != 0 || m.X != 9 || m.Y != 4 {
		t.Fatalf("legacy press decoded as %+v", m)
	}
	// Button code 35 (motion, no button) must be a hover here too: 35+32 = 'C'.
	evs = collect(t, "\x1b[M\x43\x2a\x25")
	if len(evs) != 1 || !evs[0].Mouse.Motion || evs[0].Mouse.Release {
		t.Fatalf("legacy hover decoded as %+v", evs)
	}
}
