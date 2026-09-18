package tui

import "strings"

// Editor is a small multi-line text editor used for breakpoint raw messages.
type Editor struct {
	Lines [][]rune
	CY    int
	CX    int
	Top   int
	Left  int

	Modified bool
}

// NewEditor creates an editor seeded with text.
func NewEditor(text string) *Editor {
	e := &Editor{}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		e.Lines = append(e.Lines, []rune(line))
	}
	if len(e.Lines) == 0 {
		e.Lines = [][]rune{{}}
	}
	e.CY = 0
	e.CX = 0
	return e
}

// Text serialises the buffer.
func (e *Editor) Text() string {
	var b strings.Builder
	for i, line := range e.Lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(string(line))
	}
	return b.String()
}

// Insert adds a rune at the cursor.
func (e *Editor) Insert(r rune) {
	line := e.Lines[e.CY]
	if e.CX > len(line) {
		e.CX = len(line)
	}
	line = append(line, 0)
	copy(line[e.CX+1:], line[e.CX:])
	line[e.CX] = r
	e.Lines[e.CY] = line
	e.CX++
	e.Modified = true
}

// Newline splits the current line.
func (e *Editor) Newline() {
	line := e.Lines[e.CY]
	if e.CX > len(line) {
		e.CX = len(line)
	}
	head := append([]rune(nil), line[:e.CX]...)
	tail := append([]rune(nil), line[e.CX:]...)
	e.Lines[e.CY] = head
	e.Lines = append(e.Lines, nil)
	copy(e.Lines[e.CY+2:], e.Lines[e.CY+1:])
	e.Lines[e.CY+1] = tail
	e.CY++
	e.CX = 0
	e.Modified = true
}

// Backspace deletes the rune before the cursor.
func (e *Editor) Backspace() {
	if e.CX > 0 {
		line := e.Lines[e.CY]
		if e.CX > len(line) {
			e.CX = len(line)
		}
		e.Lines[e.CY] = append(line[:e.CX-1], line[e.CX:]...)
		e.CX--
		e.Modified = true
		return
	}
	if e.CY > 0 {
		prev := e.Lines[e.CY-1]
		cur := e.Lines[e.CY]
		e.CX = len(prev)
		e.Lines[e.CY-1] = append(prev, cur...)
		e.Lines = append(e.Lines[:e.CY], e.Lines[e.CY+1:]...)
		e.CY--
		e.Modified = true
	}
}

// Delete removes the rune under the cursor.
func (e *Editor) Delete() {
	line := e.Lines[e.CY]
	if e.CX < len(line) {
		e.Lines[e.CY] = append(line[:e.CX], line[e.CX+1:]...)
		e.Modified = true
		return
	}
	if e.CY < len(e.Lines)-1 {
		e.Lines[e.CY] = append(line, e.Lines[e.CY+1]...)
		e.Lines = append(e.Lines[:e.CY+1], e.Lines[e.CY+2:]...)
		e.Modified = true
	}
}

// MoveLeft moves the cursor one cell left.
func (e *Editor) MoveLeft() {
	if e.CX > 0 {
		e.CX--
	} else if e.CY > 0 {
		e.CY--
		e.CX = len(e.Lines[e.CY])
	}
}

// MoveRight moves the cursor one cell right.
func (e *Editor) MoveRight() {
	if e.CX < len(e.Lines[e.CY]) {
		e.CX++
	} else if e.CY < len(e.Lines)-1 {
		e.CY++
		e.CX = 0
	}
}

// MoveUp moves the cursor one line up.
func (e *Editor) MoveUp() {
	if e.CY > 0 {
		e.CY--
		e.clamp()
	}
}

// MoveDown moves the cursor one line down.
func (e *Editor) MoveDown() {
	if e.CY < len(e.Lines)-1 {
		e.CY++
		e.clamp()
	}
}

// Home moves to the start of the line.
func (e *Editor) Home() { e.CX = 0 }

// End moves to the end of the line.
func (e *Editor) End() { e.CX = len(e.Lines[e.CY]) }

// PageUp scrolls a page up.
func (e *Editor) PageUp(n int) {
	e.CY -= n
	if e.CY < 0 {
		e.CY = 0
	}
	e.clamp()
}

// PageDown scrolls a page down.
func (e *Editor) PageDown(n int) {
	e.CY += n
	if e.CY >= len(e.Lines) {
		e.CY = len(e.Lines) - 1
	}
	e.clamp()
}

func (e *Editor) clamp() {
	if e.CX > len(e.Lines[e.CY]) {
		e.CX = len(e.Lines[e.CY])
	}
}

// EnsureVisible scrolls the view so the cursor is inside a w x h viewport.
func (e *Editor) EnsureVisible(w, h int) {
	if h < 1 {
		h = 1
	}
	if e.CY < e.Top {
		e.Top = e.CY
	}
	if e.CY >= e.Top+h {
		e.Top = e.CY - h + 1
	}
	if e.Top < 0 {
		e.Top = 0
	}
	if e.CX < e.Left {
		e.Left = e.CX
	}
	if e.CX >= e.Left+w {
		e.Left = e.CX - w + 1
	}
	if e.Left < 0 {
		e.Left = 0
	}
}

// SetCursorFromClick positions the cursor from a click in the viewport.
func (e *Editor) SetCursorFromClick(x, y, w, h int) {
	line := e.Top + y
	if line < 0 {
		line = 0
	}
	if line >= len(e.Lines) {
		line = len(e.Lines) - 1
	}
	col := e.Left + x
	if col < 0 {
		col = 0
	}
	if col > len(e.Lines[line]) {
		col = len(e.Lines[line])
	}
	e.CY, e.CX = line, col
}
