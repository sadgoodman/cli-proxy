package tui

import (
	"strings"
	"testing"
)

func TestTruncateAndPad(t *testing.T) {
	if got := Truncate("hello world", 8); TextWidth(got) > 8 {
		t.Fatalf("truncate overflowed: %q", got)
	} else if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncate lost the ellipsis: %q", got)
	}
	if got := Pad("abc", 6); got != "abc   " {
		t.Fatalf("Pad = %q", got)
	}
	if got := PadLeft("abc", 6); got != "   abc" {
		t.Fatalf("PadLeft = %q", got)
	}
	if got := PadCenter("ab", 6); got != "  ab  " {
		t.Fatalf("PadCenter = %q", got)
	}
	// Wide characters count as two cells.
	if w := TextWidth("中文字"); w != 6 {
		t.Fatalf("wide text width = %d, want 6", w)
	}
	if got := Pad("中", 4); TextWidth(got) != 4 {
		t.Fatalf("wide pad width = %d (%q)", TextWidth(got), got)
	}
}

func TestDrawTableUsesRoundedCorners(t *testing.T) {
	s := NewScreen(60, 10)
	cols := []Column{
		{Title: "id", Width: 4, Right: true},
		{Title: "method", Width: 8},
		{Title: "url", Flex: 1},
	}
	rows := [][]TextCell{
		{Plain("1"), Colored("GET", Style{Fg: 114}), Plain("/a")},
		{Plain("2"), Plain("POST"), Plain("/b")},
	}
	firstY, visible := DrawTable(s, 0, 0, 60, 10, "flows", cols, rows, 0, 1, -1, TableStyle{
		Border: Style{Fg: 240}, Title: Style{Fg: 74}, Header: Style{Bold: true},
		Row: Style{}, RowAlt: Style{}, Sel: Style{Reverse: true}, Hover: Style{Underline: true},
	})
	if visible != 6 {
		t.Fatalf("visible rows = %d, want 6", visible)
	}
	if firstY != 3 {
		t.Fatalf("first row y = %d, want 3", firstY)
	}
	render := s.Render(nil, true)
	for _, want := range []string{"╭", "╮", "╰", "╯", "┬", "┴", "├", "┼", "┤", "flows"} {
		if !strings.Contains(render, want) {
			t.Errorf("rendered table is missing %q", want)
		}
	}
	if !strings.Contains(render, "GET") || !strings.Contains(render, "/a") {
		t.Error("table content missing")
	}
}

func TestDrawTableColumnWidths(t *testing.T) {
	cols := []Column{
		{Title: "a", Width: 5},
		{Title: "b", Flex: 1},
		{Title: "c", Flex: 2},
	}
	w := columnWidths(cols, 60)
	total := 0
	for _, x := range w {
		total += x
	}
	if got := total + 3*len(cols) + 1; got != 60 {
		t.Fatalf("columns occupy %d cells, want exactly 60 (widths=%v)", got, w)
	}
	if w[2] <= w[1] {
		t.Fatalf("flex weighting ignored: %v", w)
	}
}

func TestDrawBoxCornersAndTitle(t *testing.T) {
	s := NewScreen(30, 6)
	DrawBox(s, 2, 1, 20, 4, "hello", Style{}, Style{Bold: true})
	out := s.Render(nil, true)
	for _, want := range []string{"╭", "╮", "╰", "╯", "hello"} {
		if !strings.Contains(out, want) {
			t.Errorf("box missing %q", want)
		}
	}
}

func TestScreenDiffOnlyEmitsChanges(t *testing.T) {
	a := NewScreen(20, 3)
	a.Text(0, 0, "hello", Style{})
	full := a.Render(nil, true)
	if !strings.Contains(full, "hello") {
		t.Fatal("full render lost content")
	}
	b := NewScreen(20, 3)
	b.Text(0, 0, "hello", Style{})
	diff := b.Render(a, false)
	if strings.Contains(diff, "hello") {
		t.Fatalf("diff re-emitted unchanged content: %q", diff)
	}
	c := NewScreen(20, 3)
	c.Text(0, 0, "hello", Style{})
	c.Text(6, 1, "world", Style{})
	diff = c.Render(a, false)
	if !strings.Contains(diff, "world") {
		t.Fatalf("diff missed the change: %q", diff)
	}
}

func TestEditorOperations(t *testing.T) {
	e := NewEditor("GET / HTTP/1.1\nHost: a\n\nbody")
	if len(e.Lines) != 4 {
		t.Fatalf("lines = %d", len(e.Lines))
	}
	e.End()
	e.Insert('!')
	if e.Lines[0][len(e.Lines[0])-1] != '!' {
		t.Fatalf("insert failed: %q", string(e.Lines[0]))
	}
	e.Backspace()
	e.MoveDown()
	e.Home()
	if e.CY != 1 || e.CX != 0 {
		t.Fatalf("cursor = %d,%d", e.CY, e.CX)
	}
	e.Insert('x')
	if !strings.HasPrefix(e.Text(), "GET / HTTP/1.1\nxHost: a") {
		t.Fatalf("text = %q", e.Text())
	}
	e.Newline()
	if e.CY != 2 || !strings.HasPrefix(e.Text(), "GET / HTTP/1.1\nx\nHost: a") {
		t.Fatalf("newline failed: %q", e.Text())
	}
	e.Delete()
	if got := e.Text(); !strings.Contains(got, "\nost: a") {
		t.Fatalf("forward delete failed: %q", got)
	}
	before := len(e.Lines)
	e.End()
	e.Delete()
	if len(e.Lines) >= before {
		t.Fatal("forward delete at EOL should join lines")
	}
	// Clicking places the caret.
	e2 := NewEditor("aaaa\nbbbb\ncccc")
	e2.SetCursorFromClick(2, 1, 10, 5)
	if e2.CY != 1 || e2.CX != 2 {
		t.Fatalf("click cursor = %d,%d", e2.CY, e2.CX)
	}
}

func TestEditorScrolling(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString("line\n")
	}
	e := NewEditor(sb.String())
	e.CY = 80
	e.EnsureVisible(20, 10)
	if e.Top > 80 || e.Top+10 <= 80 {
		t.Fatalf("cursor not visible, top=%d", e.Top)
	}
	e.CY = 0
	e.EnsureVisible(20, 10)
	if e.Top != 0 {
		t.Fatalf("top should return to 0, got %d", e.Top)
	}
}
