package tui

// Theme collects every style the UI uses. The default palette relies on the
// terminal's own foreground and background colours so it looks right on both
// light and dark profiles, and selection uses reverse video which is
// universally supported.
type Theme struct {
	Base      Style
	Border    Style
	Title     Style
	Tab       Style
	TabActive Style
	TabHover  Style
	Header    Style
	Row       Style
	RowAlt    Style
	RowHover  Style
	Sel       Style
	Dim       Style
	Accent    Style
	Good      Style
	Warn      Style
	Bad       Style
	Key       Style
	Btn       Style
	BtnHover  Style
	BtnActive Style

	Method map[string]Color
}

// DefaultTheme returns the built-in palette.
func DefaultTheme() *Theme {
	dim := Style{Fg: 244}
	accent := Style{Fg: 74}
	row := Style{}
	sel := Style{Reverse: true, Bold: true}
	// Hover uses a filled chip rather than underline: underline is nearly
	// invisible on many terminals, especially on table rows.
	hoverBg := Color(237) // palette index 236, a neutral dark grey
	rowHover := Style{Fg: 253, Bg: hoverBg}
	return &Theme{
		Base:      Style{},
		Border:    Style{Fg: 240},
		Title:     Style{Fg: 74, Bold: true},
		Tab:       Style{Fg: 250},
		TabActive: Style{Fg: 81, Bold: true, Underline: true},
		TabHover:  Style{Fg: 117, Bg: hoverBg, Bold: true},
		Header:    Style{Fg: 252, Bold: true},
		Row:       row,
		RowAlt:    Style{Fg: 250},
		RowHover:  rowHover,
		Sel:       sel,
		Dim:       dim,
		Accent:    accent,
		Good:      Style{Fg: 114},
		Warn:      Style{Fg: 179},
		Bad:       Style{Fg: 168, Bold: true},
		Key:       Style{Fg: 180},
		Btn:       Style{Fg: 250},
		BtnHover:  Style{Fg: 81, Bg: hoverBg, Bold: true},
		BtnActive: Style{Reverse: true},
		Method: map[string]Color{
			"GET":     114,
			"POST":    179,
			"PUT":     173,
			"PATCH":   180,
			"DELETE":  168,
			"HEAD":    109,
			"OPTIONS": 109,
			"CONNECT": 141,
			"TRACE":   141,
		},
	}
}

// MethodStyle colours the HTTP method column.
func (t *Theme) MethodStyle(method string) Style {
	if c, ok := t.Method[method]; ok {
		return Style{Fg: c, Bold: true}
	}
	return Style{Fg: 252, Bold: true}
}

// StatusStyle colours a status code.
func (t *Theme) StatusStyle(status int, errText string) Style {
	switch {
	case errText != "" && status == 0:
		return t.Bad
	case status == 0:
		return t.Dim
	case status < 200:
		return Style{Fg: 116}
	case status < 300:
		return Style{Fg: 114, Bold: true}
	case status < 400:
		return Style{Fg: 110}
	case status < 500:
		return Style{Fg: 179}
	default:
		return Style{Fg: 168, Bold: true}
	}
}

// StateTag renders the flow lifecycle marker for tables.
func stateTag(state string) (string, Style) {
	switch state {
	case "break":
		return "BREAK", Style{Fg: 214, Bold: true, Reverse: true}
	case "error":
		return "ERR", Style{Fg: 168, Bold: true}
	case "block":
		return "BLOCK", Style{Fg: 168}
	case "pending":
		return "···", Style{Fg: 244}
	}
	return "", Style{}
}

// Rounded box drawing runes.
const (
	boxTopLeft     = '╭'
	boxTopRight    = '╮'
	boxBottomLeft  = '╰'
	boxBottomRight = '╯'
	boxHorizontal  = '─'
	boxVertical    = '│'
	boxTeeDown     = '┬'
	boxTeeUp       = '┴'
	boxTeeRight    = '├'
	boxTeeLeft     = '┤'
	boxCross       = '┼'
	boxEllipsis    = '…'
)
