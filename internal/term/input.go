package term

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// EventKind discriminates input events.
type EventKind int

const (
	EvKey EventKind = iota
	EvMouse
)

// Key describes a decoded key press.
type Key struct {
	Name string // "up","down","left","right","enter","esc","tab","backspace","delete","home","end","pgup","pgdn","insert","space","f1".."f12"
	Rune rune   // set for printable input
	Ctrl bool
	Alt  bool
}

// IsRune reports whether the key carries printable input.
func (k Key) IsRune() bool { return k.Rune != 0 && k.Name == "" }

// Is reports whether the key matches a name, accepting a single literal
// character for printable keys whose Name is empty.
func (k Key) Is(name string) bool {
	if k.Name != "" {
		return k.Name == name
	}
	if len(name) == 1 {
		return k.Rune == rune(name[0])
	}
	return false
}

// Mouse describes a decoded mouse event. Coordinates are 0-based.
type Mouse struct {
	X, Y    int
	Button  int // 0 left, 1 middle, 2 right
	Press   bool
	Release bool
	Motion  bool
	Wheel   int // -1 up, +1 down
}

// Event is a decoded terminal input event.
type Event struct {
	Kind  EventKind
	Key   Key
	Mouse Mouse
}

// Reader decodes an input stream into keyboard and mouse events.
type Reader struct {
	bytes  chan byte
	events chan Event
	closed chan struct{}
	once   bool
}

// NewReader starts decoding r in the background.
func NewReader(r io.Reader) *Reader {
	rd := &Reader{
		bytes:  make(chan byte, 1024),
		events: make(chan Event, 128),
		closed: make(chan struct{}),
	}
	go rd.readLoop(r)
	go rd.parseLoop()
	return rd
}

// Events returns the decoded event channel.
func (r *Reader) Events() <-chan Event { return r.events }

// Close stops the reader.
func (r *Reader) Close() {
	if !r.once {
		r.once = true
		close(r.closed)
	}
}

func (r *Reader) readLoop(rr io.Reader) {
	buf := make([]byte, 256)
	for {
		n, err := rr.Read(buf)
		for i := 0; i < n; i++ {
			select {
			case r.bytes <- buf[i]:
			case <-r.closed:
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			select {
			case <-time.After(10 * time.Millisecond):
			case <-r.closed:
				return
			}
		}
	}
}

func (r *Reader) next(timeout time.Duration) (byte, bool) {
	if timeout <= 0 {
		select {
		case b := <-r.bytes:
			return b, true
		case <-r.closed:
			return 0, false
		}
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case b := <-r.bytes:
		return b, true
	case <-t.C:
		return 0, false
	case <-r.closed:
		return 0, false
	}
}

func (r *Reader) emit(ev Event) {
	select {
	case r.events <- ev:
	case <-r.closed:
	}
}

func (r *Reader) parseLoop() {
	for {
		b, ok := r.next(0)
		if !ok {
			return
		}
		if b != 0x1b {
			r.emit(decodeByte(b, r))
			continue
		}
		seq := []byte{0x1b}
		for {
			nb, ok := r.next(25 * time.Millisecond)
			if !ok {
				break
			}
			seq = append(seq, nb)
			if len(seq) > 128 || seqComplete(seq) {
				break
			}
		}
		r.emit(parseEscape(seq))
	}
}

// seqComplete reports whether an escape sequence has all of its bytes.
func seqComplete(seq []byte) bool {
	if len(seq) < 2 {
		return false
	}
	switch seq[1] {
	case '[':
		if len(seq) >= 3 && seq[2] == 'M' {
			// Legacy X10 mouse: ESC [ M followed by three raw bytes.
			return len(seq) >= 6
		}
		for i := 2; i < len(seq); i++ {
			c := seq[i]
			if c >= 0x40 && c <= 0x7e {
				return true
			}
		}
		return false
	case 'O':
		return len(seq) >= 3
	default:
		if seq[1] >= 0x80 {
			return utf8.FullRune(seq[1:])
		}
		return true
	}
}

func decodeByte(b byte, r *Reader) Event {
	switch b {
	case 0x0d, 0x0a:
		return Event{Kind: EvKey, Key: Key{Name: "enter"}}
	case 0x09:
		return Event{Kind: EvKey, Key: Key{Name: "tab"}}
	case 0x7f, 0x08:
		return Event{Kind: EvKey, Key: Key{Name: "backspace"}}
	case 0x00:
		return Event{Kind: EvKey, Key: Key{Name: "space", Ctrl: true}}
	}
	if b >= 0x01 && b <= 0x1a {
		return Event{Kind: EvKey, Key: Key{Name: strings.ToLower(string(rune('a' + b - 1))), Ctrl: true}}
	}
	if b >= 0x1c && b <= 0x1f {
		return Event{Kind: EvKey, Key: Key{Name: string(rune('a' + b - 1)), Ctrl: true}}
	}
	if b < 0x80 {
		return Event{Kind: EvKey, Key: Key{Rune: rune(b)}}
	}
	// Multi-byte UTF-8: gather the remaining bytes.
	n := 1
	switch {
	case b&0xe0 == 0xc0:
		n = 2
	case b&0xf0 == 0xe0:
		n = 3
	case b&0xf8 == 0xf0:
		n = 4
	}
	buf := []byte{b}
	for len(buf) < n {
		nb, ok := r.next(25 * time.Millisecond)
		if !ok {
			break
		}
		buf = append(buf, nb)
	}
	ru, _ := utf8.DecodeRune(buf)
	return Event{Kind: EvKey, Key: Key{Rune: ru}}
}

func parseEscape(seq []byte) Event {
	if len(seq) == 1 {
		return Event{Kind: EvKey, Key: Key{Name: "esc"}}
	}
	switch seq[1] {
	case '[':
		if len(seq) >= 3 && seq[2] == 'M' {
			return Event{Kind: EvMouse, Mouse: decodeX10Mouse(seq)}
		}
		return parseCSI(seq)
	case 'O':
		if len(seq) >= 3 {
			return Event{Kind: EvKey, Key: ss3Key(seq[2])}
		}
		return Event{Kind: EvKey, Key: Key{Name: "esc"}}
	default:
		ru, _ := utf8.DecodeRune(seq[1:])
		if ru == 0x1b {
			return Event{Kind: EvKey, Key: Key{Name: "esc"}}
		}
		return Event{Kind: EvKey, Key: Key{Rune: ru, Alt: true}}
	}
}

func ss3Key(c byte) Key {
	switch c {
	case 'A':
		return Key{Name: "up"}
	case 'B':
		return Key{Name: "down"}
	case 'C':
		return Key{Name: "right"}
	case 'D':
		return Key{Name: "left"}
	case 'H':
		return Key{Name: "home"}
	case 'F':
		return Key{Name: "end"}
	case 'P':
		return Key{Name: "f1"}
	case 'Q':
		return Key{Name: "f2"}
	case 'R':
		return Key{Name: "f3"}
	case 'S':
		return Key{Name: "f4"}
	}
	return Key{Name: "unknown"}
}

func parseCSI(seq []byte) Event {
	body := seq[2:]
	final := byte(0)
	if len(body) > 0 {
		final = body[len(body)-1]
	}
	params := body[:max(0, len(body)-1)]

	// SGR mouse: ESC [ < b ; x ; y M|m
	if len(params) > 0 && params[0] == '<' {
		nums := splitNums(string(params[1:]))
		if len(nums) >= 3 {
			return Event{Kind: EvMouse, Mouse: decodeSGRMouse(nums, final)}
		}
		return Event{Kind: EvKey, Key: Key{Name: "unknown"}}
	}

	nums := splitNums(string(params))
	mods := 0
	if len(nums) >= 2 {
		mods = nums[len(nums)-1] - 1
	}
	alt := mods&2 != 0
	ctrl := mods&4 != 0

	key := Key{Alt: alt, Ctrl: ctrl}
	switch final {
	case 'A':
		key.Name = "up"
	case 'B':
		key.Name = "down"
	case 'C':
		key.Name = "right"
	case 'D':
		key.Name = "left"
	case 'H':
		key.Name = "home"
	case 'F':
		key.Name = "end"
	case 'Z':
		key.Name = "shift-tab"
	case '~':
		n := 0
		if len(nums) > 0 {
			n = nums[0]
		}
		switch n {
		case 1, 7:
			key.Name = "home"
		case 2:
			key.Name = "insert"
		case 3:
			key.Name = "delete"
		case 4, 8:
			key.Name = "end"
		case 5:
			key.Name = "pgup"
		case 6:
			key.Name = "pgdn"
		case 11:
			key.Name = "f1"
		case 12:
			key.Name = "f2"
		case 13:
			key.Name = "f3"
		case 14:
			key.Name = "f4"
		case 15:
			key.Name = "f5"
		case 17:
			key.Name = "f6"
		case 18:
			key.Name = "f7"
		case 19:
			key.Name = "f8"
		case 20:
			key.Name = "f9"
		case 21:
			key.Name = "f10"
		case 23:
			key.Name = "f11"
		case 24:
			key.Name = "f12"
		default:
			key.Name = "unknown"
		}
	default:
		key.Name = "unknown"
	}
	return Event{Kind: EvKey, Key: key}
}

// decodeSGRMouse turns the SGR parameter list into a mouse event.
//
// The low two bits carry the button; 3 means "no button", which for a motion
// report is a plain hover and must NOT be mistaken for a release.
func decodeSGRMouse(nums []int, final byte) Mouse {
	code := nums[0]
	m := Mouse{X: nums[1] - 1, Y: nums[2] - 1}
	button := code & 3
	motion := code&32 != 0
	switch {
	case code&64 != 0: // wheel
		m.Motion = motion
		if button == 0 {
			m.Wheel = -1
		} else {
			m.Wheel = 1
		}
	case motion:
		m.Motion = true
		if button == 3 {
			m.Button = -1
		} else {
			m.Button = button
		}
	case final == 'm' || button == 3:
		m.Button = -1
		m.Release = true
	default:
		m.Button = button
		m.Press = true
	}
	return m
}

// decodeX10Mouse handles the legacy ESC [ M b x y encoding used by terminals
// that do not implement SGR mouse reporting.
func decodeX10Mouse(seq []byte) Mouse {
	if len(seq) < 6 {
		return Mouse{Button: -1}
	}
	code := int(seq[3]) - 32
	m := Mouse{X: int(seq[4]) - 33, Y: int(seq[5]) - 33}
	button := code & 3
	switch {
	case code&64 != 0:
		if button == 0 {
			m.Wheel = -1
		} else {
			m.Wheel = 1
		}
	case code&32 != 0:
		m.Motion = true
		if button == 3 {
			m.Button = -1
		} else {
			m.Button = button
		}
	case button == 3:
		m.Button = -1
		m.Release = true
	default:
		m.Button = button
		m.Press = true
	}
	return m
}

func splitNums(s string) []int {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ";")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			out = append(out, 0)
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return out
		}
		out = append(out, n)
	}
	return out
}
