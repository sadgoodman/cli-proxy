package tui

import (
	"fmt"
	"strings"
)

// Color is a terminal palette entry. The zero value means "terminal default",
// so a zero Style is always valid.
type Color uint8

func (c Color) code() int { return int(c) - 1 }

const (
	ColDefault Color = 0
	ColRed     Color = 168
	ColGreen   Color = 114
	ColYellow  Color = 179
	ColBlue    Color = 110
	ColMagenta Color = 176
	ColCyan    Color = 116
	ColWhite   Color = 252
	ColGray    Color = 245
	ColDark    Color = 240
)

// Style describes how a cell is drawn.
type Style struct {
	Fg, Bg    Color
	Bold      bool
	Dim       bool
	Italic    bool
	Underline bool
	Reverse   bool
}

// WithFg returns a copy of the style with a different foreground colour.
func (st Style) WithFg(c Color) Style { st.Fg = c; return st }

// WithBg returns a copy of the style with a different background colour.
func (st Style) WithBg(c Color) Style { st.Bg = c; return st }

// WithBold returns a copy of the style with bold toggled.
func (st Style) WithBold(on bool) Style { st.Bold = on; return st }

// WithReverse returns a copy of the style with reverse video toggled.
func (st Style) WithReverse(on bool) Style { st.Reverse = on; return st }

func (st Style) seq() string {
	if st == (Style{}) {
		return "\x1b[0m"
	}
	var b strings.Builder
	b.WriteString("\x1b[0")
	if st.Bold {
		b.WriteString(";1")
	}
	if st.Dim {
		b.WriteString(";2")
	}
	if st.Italic {
		b.WriteString(";3")
	}
	if st.Underline {
		b.WriteString(";4")
	}
	if st.Reverse {
		b.WriteString(";7")
	}
	if st.Fg != ColDefault {
		fmt.Fprintf(&b, ";38;5;%d", st.Fg.code())
	}
	if st.Bg != ColDefault {
		fmt.Fprintf(&b, ";48;5;%d", st.Bg.code())
	}
	b.WriteString("m")
	return b.String()
}

type cell struct {
	r    rune
	st   Style
	cont bool // second half of a wide rune
}

// Screen is an off-screen cell buffer that can be diffed against the previous
// frame to produce minimal ANSI output.
type Screen struct {
	W, H  int
	cells []cell
}

// NewScreen allocates a screen buffer.
func NewScreen(w, h int) *Screen {
	s := &Screen{}
	s.Resize(w, h)
	return s
}

// Resize changes the buffer dimensions and clears it.
func (s *Screen) Resize(w, h int) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	s.W, s.H = w, h
	s.cells = make([]cell, w*h)
	for i := range s.cells {
		s.cells[i] = cell{r: ' '}
	}
}

// Clear resets every cell.
func (s *Screen) Clear() {
	for i := range s.cells {
		s.cells[i] = cell{r: ' '}
	}
}

// Set writes a single rune, handling wide glyphs.
func (s *Screen) Set(x, y int, r rune, st Style) {
	if x < 0 || y < 0 || x >= s.W || y >= s.H {
		return
	}
	if r == 0 {
		r = ' '
	}
	w := runeWidth(r)
	if w == 0 {
		r = ' '
		w = 1
	}
	i := y*s.W + x
	s.cells[i] = cell{r: r, st: st}
	if w == 2 && x+1 < s.W {
		s.cells[i+1] = cell{r: 0, st: st, cont: true}
	}
}

// Text draws a string starting at (x, y) and returns the next free column.
func (s *Screen) Text(x, y int, str string, st Style) int {
	for _, r := range str {
		if r == '\n' {
			continue
		}
		w := runeWidth(r)
		if w == 0 {
			continue
		}
		if x >= s.W {
			break
		}
		s.Set(x, y, r, st)
		x += w
	}
	return x
}

// Fill paints a rectangle with a rune.
func (s *Screen) Fill(x, y, w, h int, r rune, st Style) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			s.Set(xx, yy, r, st)
		}
	}
}

// TextWidth returns the display width of a string.
func TextWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// Truncate shortens s to at most width cells, appending an ellipsis.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if TextWidth(s) <= width {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > width-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	b.WriteRune('…')
	return b.String()
}

// Pad pads or truncates s to exactly width cells.
func Pad(s string, width int) string {
	s = Truncate(s, width)
	w := TextWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// PadLeft right-aligns s inside width cells.
func PadLeft(s string, width int) string {
	s = Truncate(s, width)
	w := TextWidth(s)
	if w >= width {
		return s
	}
	return strings.Repeat(" ", width-w) + s
}

// PadCenter centres s inside width cells.
func PadCenter(s string, width int) string {
	s = Truncate(s, width)
	w := TextWidth(s)
	if w >= width {
		return s
	}
	left := (width - w) / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", width-w-left)
}

func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 32 || (r >= 0x7f && r < 0xa0):
		return 0
	case r < 0x1100:
		return 1
	case r == 0x2329 || r == 0x232a:
		return 2
	case r >= 0x1100 && r <= 0x115f, // Hangul Jamo
		r >= 0x2e80 && r <= 0x303e, // CJK radicals, Kangxi
		r >= 0x3041 && r <= 0x33ff, // Hiragana..CJK compatibility
		r >= 0x3400 && r <= 0x4dbf, // CJK ext A
		r >= 0x4e00 && r <= 0x9fff, // CJK unified
		r >= 0xa000 && r <= 0xa4cf, // Yi
		r >= 0xac00 && r <= 0xd7a3, // Hangul syllables
		r >= 0xf900 && r <= 0xfaff, // CJK compatibility ideographs
		r >= 0xfe30 && r <= 0xfe6f, // CJK compatibility forms
		r >= 0xff00 && r <= 0xff60, // Fullwidth forms
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f64f, // emoji
		r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}

// Render diffs the screen against prev and returns the ANSI update. A nil prev
// forces a full repaint.
func (s *Screen) Render(prev *Screen, full bool) string {
	var b strings.Builder
	b.Grow(s.W * s.H / 4)
	if full || prev == nil || prev.W != s.W || prev.H != s.H {
		b.WriteString("\x1b[2J")
		prev = nil
	}
	for y := 0; y < s.H; y++ {
		first, last := -1, -1
		for x := 0; x < s.W; x++ {
			i := y*s.W + x
			if s.cells[i].cont {
				continue
			}
			if prev == nil || prev.cells[i] != s.cells[i] {
				if first < 0 {
					first = x
				}
				last = x
			}
		}
		if first < 0 {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH", y+1, first+1)
		cur := Style{}
		haveStyle := false
		for x := first; x <= last; x++ {
			c := s.cells[y*s.W+x]
			if c.cont {
				continue
			}
			if !haveStyle || c.st != cur {
				b.WriteString(c.st.seq())
				cur, haveStyle = c.st, true
			}
			if c.r == 0 {
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(c.r)
		}
	}
	b.WriteString("\x1b[0m")
	return b.String()
}
