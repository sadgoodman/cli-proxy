package tui

// Column describes one table column.
type Column struct {
	Title  string
	Width  int     // fixed content width when > 0
	Flex   float64 // relative share of the leftover width when Width == 0
	Right  bool
	Center bool
}

// TableStyle bundles the styles used while drawing a table.
type TableStyle struct {
	Border Style
	Title  Style
	Header Style
	Row    Style
	RowAlt Style
	Sel    Style
	Hover  Style
}

// TextCell is one table cell. When Own is set the cell uses its own
// foreground colour, merged with the row's reverse/underline state so
// selection highlighting still reads correctly.
type TextCell struct {
	Text  string
	Style Style
	Own   bool
}

// Plain builds an unstyled cell.
func Plain(s string) TextCell { return TextCell{Text: s} }

// Colored builds a cell with its own foreground colour.
func Colored(s string, st Style) TextCell { return TextCell{Text: s, Style: st, Own: true} }

// DrawBox renders a rounded-corner box, optionally with a title in the top
// border, and returns without touching the interior.
func DrawBox(s *Screen, x, y, w, h int, title string, border Style, titleStyle Style) {
	if w < 2 || h < 2 {
		return
	}
	for i := 0; i < w; i++ {
		s.Set(x+i, y, boxHorizontal, border)
		s.Set(x+i, y+h-1, boxHorizontal, border)
	}
	for j := 0; j < h; j++ {
		s.Set(x, y+j, boxVertical, border)
		s.Set(x+w-1, y+j, boxVertical, border)
	}
	s.Set(x, y, boxTopLeft, border)
	s.Set(x+w-1, y, boxTopRight, border)
	s.Set(x, y+h-1, boxBottomLeft, border)
	s.Set(x+w-1, y+h-1, boxBottomRight, border)
	if title != "" && w > 4 {
		label := " " + Truncate(title, w-6) + " "
		s.Text(x+2, y, label, titleStyle)
	}
}

// DrawTable renders a rounded-corner table inside the rectangle
// (x, y, w, h). rows are the full data set; scroll is the index of the first
// row shown. sel and hover index into rows, or are -1.
//
// It returns the y coordinate of the first drawn data row and the number of
// data rows drawn, which the caller uses for mouse hit testing.
func DrawTable(s *Screen, x, y, w, h int, title string, cols []Column, rows [][]TextCell,
	scroll, sel, hover int, st TableStyle) (int, int) {

	if w < 4 || h < 3 || len(cols) == 0 {
		return y, 0
	}
	widths := columnWidths(cols, w)
	n := len(cols)

	// Geometry: the table is 1 + sum(cw + 3) cells wide. Every column owns a
	// leading space, cw content cells, a trailing space and a separator.
	drawRule := func(row int, left, mid, right, fill rune) {
		cx := x
		s.Set(cx, row, left, st.Border)
		cx++
		for i, cw := range widths {
			for k := 0; k < cw+2; k++ {
				s.Set(cx, row, fill, st.Border)
				cx++
			}
			if i == n-1 {
				s.Set(cx, row, right, st.Border)
			} else {
				s.Set(cx, row, mid, st.Border)
			}
			cx++
		}
	}

	drawCells := func(row int, cells []TextCell, base Style) {
		cx := x
		s.Set(cx, row, boxVertical, base)
		cx++
		for i, c := range cols {
			cell := TextCell{}
			if i < len(cells) {
				cell = cells[i]
			}
			textStyle := base
			if cell.Own {
				textStyle.Fg = cell.Style.Fg
				textStyle.Bold = textStyle.Bold || cell.Style.Bold
				textStyle.Dim = textStyle.Dim || cell.Style.Dim
				textStyle.Italic = textStyle.Italic || cell.Style.Italic
			}
			s.Set(cx, row, ' ', base)
			s.Text(cx+1, row, fitCell(c, cell.Text, widths[i]), textStyle)
			s.Set(cx+1+widths[i], row, ' ', base)
			s.Set(cx+2+widths[i], row, boxVertical, base)
			cx += widths[i] + 3
		}
	}

	drawRule(y, boxTopLeft, boxTeeDown, boxTopRight, boxHorizontal)
	if title != "" && w > 6 {
		// The title rides on the top border and may span several columns.
		s.Text(x+2, y, " "+Truncate(title, w-6)+" ", st.Title)
	}

	header := make([]TextCell, n)
	for i, c := range cols {
		title := c.Title
		if c.Right {
			title = PadLeft(title, widths[i])
		} else if c.Center {
			title = PadCenter(title, widths[i])
		}
		header[i] = TextCell{Text: title, Style: st.Header, Own: true}
	}
	drawCells(y+1, header, st.Header)
	drawRule(y+2, boxTeeRight, boxCross, boxTeeLeft, boxHorizontal)

	bodyTop := y + 3
	visible := (y + h - 2) - bodyTop + 1
	if visible < 0 {
		visible = 0
	}
	if scroll < 0 {
		scroll = 0
	}
	if scroll > len(rows) {
		scroll = len(rows)
	}

	drawn := 0
	for r := 0; r < visible; r++ {
		idx := scroll + r
		if idx >= len(rows) {
			break
		}
		base := st.Row
		if r%2 == 1 {
			base = st.RowAlt
		}
		switch {
		case idx == sel:
			base = st.Sel
		case idx == hover:
			base = st.Hover
		}
		drawCells(bodyTop+r, rows[idx], base)
		drawn++
	}
	for r := drawn; r < visible; r++ {
		rowY := bodyTop + r
		s.Set(x, rowY, boxVertical, st.Border)
		for cx := x + 1; cx < x+w-1; cx++ {
			s.Set(cx, rowY, ' ', st.Border)
		}
		s.Set(x+w-1, rowY, boxVertical, st.Border)
	}

	drawRule(y+h-1, boxBottomLeft, boxTeeUp, boxBottomRight, boxHorizontal)
	return bodyTop, visible
}

func fitCell(c Column, text string, width int) string {
	switch {
	case c.Right:
		return PadLeft(text, width)
	case c.Center:
		return PadCenter(text, width)
	default:
		return Pad(text, width)
	}
}

func columnWidths(cols []Column, total int) []int {
	n := len(cols)
	// total = sum(widths) + 3n + 1
	avail := total - 3*n - 1
	if avail < n {
		avail = n
	}
	widths := make([]int, n)
	flexTotal := 0.0
	fixed := 0
	for i, c := range cols {
		if c.Width > 0 {
			widths[i] = c.Width
			fixed += c.Width
		} else {
			flexTotal += maxFloat(c.Flex, 0.001)
		}
	}
	leftover := avail - fixed
	if leftover < n {
		leftover = n
	}
	assigned := 0
	for i, c := range cols {
		if c.Width > 0 {
			continue
		}
		share := int(float64(leftover) * (maxFloat(c.Flex, 0.001) / flexTotal))
		if share < 3 {
			share = 3
		}
		widths[i] = share
		assigned += share
	}
	// Correct rounding drift on the widest flexible column.
	if drift := avail - fixed - assigned; drift != 0 {
		best := -1
		for i, c := range cols {
			if c.Width > 0 {
				continue
			}
			if best < 0 || widths[i] > widths[best] {
				best = i
			}
		}
		if best >= 0 {
			widths[best] += drift
			if widths[best] < 3 {
				widths[best] = 3
			}
		}
	}
	// Clip so the table never exceeds the requested width.
	over := 0
	for _, wdt := range widths {
		over += wdt
	}
	over = over + 3*n + 1 - total
	for over > 0 {
		best := -1
		for i := range widths {
			if widths[i] <= 3 {
				continue
			}
			if best < 0 || widths[i] > widths[best] {
				best = i
			}
		}
		if best < 0 {
			break
		}
		widths[best]--
		over--
	}
	return widths
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
