package tui

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"cliproxy/internal/core"
	"cliproxy/internal/proxy"
	"cliproxy/internal/term"
)

// run is a span of text with one style, used for syntax colouring.
type run struct {
	text string
	st   Style
}

// tline is one logical line. Either it carries a single style, or a list of
// styled runs (JSON colouring). Runs are never split across visual lines
// incorrectly because wrapping happens over the flattened rune sequence.
type tline struct {
	text string
	st   Style
	runs []run
}

func tl(text string, st Style) tline { return tline{text: text, st: st} }

func runsLine(runs ...run) tline { return tline{runs: runs} }

// spans returns the styled runs of a line.
func (l tline) spans() []run {
	if len(l.runs) > 0 {
		return l.runs
	}
	return []run{{text: l.text, st: l.st}}
}

// plainText flattens a line for measuring.
func (l tline) plainText() string {
	if len(l.runs) == 0 {
		return l.text
	}
	var b strings.Builder
	for _, r := range l.runs {
		b.WriteString(r.text)
	}
	return b.String()
}

// certTabs are the instruction pages shown on the certificate screen.
var certTabs = []string{"iOS", "Android", "macOS", "Windows", "Linux", "this machine"}

var certTabInstructions = map[string][]string{
	"iOS": {
		"Open Safari and go to   <short>/cert",
		"Settings > Profile Downloaded > Install  (enter passcode)",
		"Settings > General > About > Certificate Trust Settings",
		"  -> enable full trust for \"cli-proxy Root CA\"",
		"Wi-Fi > HTTP Proxy > Manual:  <host> : <port>",
	},
	"Android": {
		"Open <short>/cert in Chrome and install the download",
		"Settings > Security > Encryption & credentials",
		"  > Install a certificate > CA certificate",
		"(Android 7+: apps that opt out of user CAs cannot be intercepted)",
		"Wi-Fi > Modify network > Advanced > Proxy > Manual",
		"  Hostname <host>   Port <port>",
	},
	"macOS": {
		"One keystroke: press [i] on this screen, no password needed.",
		"",
		"By hand:",
		"  curl -o ca.crt <base>/cli-proxy-ca.crt",
		"  sudo security add-trusted-cert -d -r trustRoot \\",
		"       -k /Library/Keychains/System.keychain ca.crt",
	},
	"Windows": {
		"curl -o ca.crt <base>/cli-proxy-ca.crt",
		"certutil -user -addstore ROOT ca.crt",
		"Settings > Network & Internet > Proxy > Manual setup",
		"  <host> : <port>",
	},
	"Linux": {
		"Chrome/Chromium (NSS): press [i] on this screen, or",
		"  certutil -A -d sql:$HOME/.pki/nssdb -n cli-proxy -t C,, -i ca.crt",
		"System-wide:",
		"  sudo cp ca.crt /usr/local/share/ca-certificates/cli-proxy.crt",
		"  sudo update-ca-certificates",
		"Firefox keeps its own store: Privacy > Certificates > Import",
	},
	"this machine": {
		"Programs on this computer do not read the device settings.",
		"export HTTP_PROXY=<base> ; export HTTPS_PROXY=<base>",
		"curl -x <base> --cacert ca.crt https://example.com",
		"GUI apps: switch the system proxy on with [s].",
		"NOTE: macOS and Windows bypass the proxy for 127.0.0.1 and *.local,",
		"so requests to services on this machine are not captured.",
	},
}

// ---------------------------------------------------------------------------
// frame
// ---------------------------------------------------------------------------

func (a *App) render() {
	t := a.theme
	a.lay = layout{}
	a.cursorVisible = false

	for x := 0; x < a.W; x++ {
		a.screen.Set(x, 0, boxHorizontal, t.Border)
	}
	a.screen.Set(0, 0, boxTopLeft, t.Border)
	a.screen.Set(a.W-1, 0, boxTopRight, t.Border)
	a.screen.Text(2, 0, " cli-proxy ", t.Title)
	a.renderStatusRight(0)
	a.screen.Set(1, 0, boxHorizontal, t.Border)

	a.screen.Set(0, 1, boxVertical, t.Border)
	a.screen.Set(a.W-1, 1, boxVertical, t.Border)
	a.renderTabs(1)

	for x := 1; x < a.W-1; x++ {
		a.screen.Set(x, 2, boxHorizontal, t.Border)
	}
	a.screen.Set(0, 2, boxTeeRight, t.Border)
	a.screen.Set(a.W-1, 2, boxTeeLeft, t.Border)

	bodyY := 3
	bodyH := a.H - 1 - bodyY
	if a.p != nil {
		bodyH--
	}
	if bodyH < 3 {
		bodyH = 3
	}
	for y := bodyY; y < bodyY+bodyH; y++ {
		a.screen.Set(0, y, boxVertical, t.Border)
		a.screen.Set(a.W-1, y, boxVertical, t.Border)
	}

	switch a.view {
	case ViewFlows:
		a.renderFlows(bodyY, bodyH)
	case ViewFilters:
		a.renderFilters(bodyY, bodyH)
	case ViewRules:
		a.renderRules(bodyY, bodyH)
	case ViewCert:
		a.renderCert(bodyY, bodyH)
	case ViewLog:
		a.renderLog(bodyY, bodyH)
	case ViewHelp:
		a.renderHelp(bodyY, bodyH)
	case ViewDetail:
		a.renderDetail(bodyY, bodyH)
	case ViewEditor:
		a.renderEditor(bodyY, bodyH)
	}

	if a.p != nil {
		a.renderPrompt(a.H - 2)
	}
	a.renderButtons(a.H - 1)
}

func (a *App) renderStatusRight(row int) {
	t := a.theme
	active, _ := a.proxy.Stats()
	parts := []string{
		a.proxy.DisplayAddr(),
		strings.ToUpper(a.proxy.Mode()),
		fmt.Sprintf("%d flows", a.store.Len()),
	}
	if n := a.brk.Len(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d paused", n))
	}
	if active > 0 {
		parts = append(parts, fmt.Sprintf("%d conn", active))
	}
	if a.opts.BreakRequests.Load() {
		parts = append(parts, "brk:req")
	}
	if a.opts.BreakResponses.Load() {
		parts = append(parts, "brk:resp")
	}
	if a.filterset != nil {
		if n := len(a.filterset.EnabledExprs()); n > 0 {
			parts = append(parts, fmt.Sprintf("%d saved filter", n))
		}
	}
	if a.filterText != "" {
		parts = append(parts, "filter:"+a.filterText)
	}
	if a.sysProxy != nil && a.sysProxy.Active() {
		parts = append(parts, "sys-proxy")
	}
	if a.certPending {
		parts = append(parts, "cert…")
	}
	if a.statusText != "" && time.Since(a.statusAt) < 6*time.Second {
		parts = append(parts, a.statusText)
	} else {
		parts = append(parts, time.Since(a.started).Round(time.Second).String())
	}
	text := strings.Join(parts, " · ") + " "
	st := t.Dim
	if a.statusText != "" && time.Since(a.statusAt) < 6*time.Second {
		switch a.statusKind {
		case 1:
			st = t.Good
		case 2:
			st = t.Bad
		default:
			st = t.Accent
		}
	}
	w := TextWidth(text)
	if w > a.W-20 {
		text = Truncate(text, a.W-20)
		w = TextWidth(text)
	}
	a.screen.Text(a.W-1-w, row, text, st)
}

func (a *App) renderTabs(row int) {
	t := a.theme
	x := 2
	a.lay.tabs = make([]rect, len(tabNames))
	for i, tab := range tabNames {
		label := " " + tab.Name + " "
		st := t.Tab
		switch {
		case a.view == tab.View, (a.view == ViewDetail && tab.View == ViewFlows):
			st = t.TabActive
		case a.hoverTab == i:
			st = t.TabHover
		}
		w := TextWidth(label)
		a.screen.Text(x, row, label, st)
		a.lay.tabs[i] = rect{X: x, Y: row, W: w, H: 1}
		x += w + 1
	}
	if a.view == ViewDetail || a.view == ViewEditor {
		a.screen.Text(x+2, row, "› "+a.breadcrumb(), a.theme.Dim)
	}
}

func (a *App) breadcrumb() string {
	switch a.view {
	case ViewDetail:
		if a.detail != nil {
			return fmt.Sprintf("#%d %s %s", a.detail.ID, a.detail.Method, Truncate(a.detail.URL, 60))
		}
		return "detail"
	case ViewEditor:
		if a.editorBreak != nil {
			return fmt.Sprintf("breakpoint #%d · %s", a.editorBreak.ID, string(a.editorBreak.Phase))
		}
		return "breakpoint"
	}
	return ""
}

// ---------------------------------------------------------------------------
// drawing helpers
// ---------------------------------------------------------------------------

// wrapText breaks a string into visual lines no wider than width, keeping the
// original indentation on continuation lines so JSON stays readable.
func wrapText(text string, width int, indent int) []string {
	if width <= 2 {
		return []string{Truncate(text, maxInt(1, width))}
	}
	if TextWidth(text) <= width {
		return []string{text}
	}
	pad := strings.Repeat(" ", indent)
	var out []string
	cur, curW := "", 0
	first := true
	for _, r := range text {
		rw := runeWidth(r)
		limit := width
		if !first {
			limit = width - indent
		}
		if curW+rw > limit && cur != "" {
			out = append(out, cur)
			cur, curW = pad, indent
			first = false
		}
		cur += string(r)
		curW += rw
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func leadingIndent(rs []rune) int {
	n := 0
	for _, r := range rs {
		if r == ' ' || r == '\t' {
			n++
			continue
		}
		break
	}
	return n
}

// wrapLine turns one logical line into visual lines of styled runs.
func wrapLine(l tline, width int) [][]run {
	spans := l.spans()
	var rs []rune
	var sts []Style
	for _, sp := range spans {
		for _, ch := range sp.text {
			rs = append(rs, ch)
			sts = append(sts, sp.st)
		}
	}
	if len(rs) == 0 {
		return [][]run{{{text: "", st: l.st}}}
	}
	if width <= 2 || TextWidth(string(rs)) <= width {
		return [][]run{spans}
	}
	indent := leadingIndent(rs)
	if indent > width/2 {
		indent = width / 4
	}
	pad := strings.Repeat(" ", indent)

	var out [][]run
	start, first := 0, true
	for start < len(rs) {
		limit := width
		if !first {
			limit = width - indent
		}
		w, end := 0, start
		for end < len(rs) {
			rw := runeWidth(rs[end])
			if w+rw > limit && end > start {
				break
			}
			w += rw
			end++
		}
		// If the whole remainder would fit on the next row, break at the last
		// space instead of splitting a word or a JSON value in half.
		if end < len(rs) {
			lastSpace := -1
			for k := end - 1; k > start; k-- {
				if rs[k] == ' ' {
					lastSpace = k
					break
				}
			}
			if lastSpace > start {
				rest := 0
				for _, ch := range rs[lastSpace+1:] {
					rest += runeWidth(ch)
				}
				if indent+rest <= width {
					end = lastSpace
				}
			}
			for end < len(rs) && rs[end] == ' ' {
				end++
			}
		}
		var line []run
		if !first {
			line = append(line, run{text: pad})
		}
		for i := start; i < end; {
			j := i
			for j < end && sts[j] == sts[i] {
				j++
			}
			line = append(line, run{text: string(rs[i:j]), st: sts[i]})
			i = j
		}
		out = append(out, line)
		start = end
		first = false
	}
	return out
}

// wrapLines expands logical lines into visual lines of styled runs.
func wrapLines(lines []tline, width int) [][]run {
	out := make([][]run, 0, len(lines))
	for _, l := range lines {
		out = append(out, wrapLine(l, width)...)
	}
	return out
}

// drawRuns paints one visual line without wrapping.
func (a *App) drawRuns(x, y, width int, runs []run, base Style) {
	cx := x
	for _, r := range runs {
		st := r.st
		for _, ch := range r.text {
			rw := runeWidth(ch)
			if cx-x+rw > width {
				return
			}
			if st == (Style{}) {
				st = base
			}
			a.screen.Set(cx, y, ch, st)
			cx += rw
		}
	}
}

// drawWrapped renders lines into a rectangle, wrapping long ones so nothing is
// ever cut off horizontally.
func (a *App) drawWrapped(r rect, lines []tline, scroll int) int {
	visual := wrapLines(lines, r.W)
	for i := 0; i < r.H; i++ {
		idx := scroll + i
		if idx < 0 || idx >= len(visual) {
			break
		}
		if len(visual[idx]) == 0 {
			continue
		}
		a.drawRuns(r.X, r.Y+i, r.W, visual[idx], a.theme.Base)
	}
	return len(visual)
}

// drawLines renders lines as-is, truncating anything too wide.
func (a *App) drawLines(r rect, lines []tline, scroll int) {
	for i := 0; i < r.H; i++ {
		idx := scroll + i
		if idx < 0 || idx >= len(lines) {
			break
		}
		a.drawRuns(r.X, r.Y+i, r.W, lines[idx].spans(), lines[idx].st)
	}
}

func (a *App) maxScroll(lines []tline, width, height int) int {
	n := len(wrapLines(lines, width)) - height
	if n < 0 {
		return 0
	}
	return n
}

// visualHeight counts the rows a set of lines occupies once wrapped.
func visualHeight(lines []tline, width int) int { return len(wrapLines(lines, width)) }

// ---------------------------------------------------------------------------
// message panes with tabs
// ---------------------------------------------------------------------------

// renderPane draws one message pane: a rounded box whose top border carries
// the headers/body/raw tabs.
func (a *App) renderPane(x, y, w, h, side int, title string, scroll int) {
	t := a.theme
	if w < 10 || h < 3 {
		return
	}
	border := t.Border
	if side == a.focus {
		border = Style{Fg: 74, Bold: true}
	}

	// Work out the tab strip first so the title can be shortened to fit
	// beside it instead of colliding with it.
	labels := make([]string, paneTabCount)
	total := 0
	for i := 0; i < int(paneTabCount); i++ {
		labels[i] = paneTabNames[i]
		total += TextWidth(labels[i])
		if i < int(paneTabCount)-1 {
			total += 3
		}
	}
	start := x + w - 3 - total
	showTabs := start-(x+2) >= 12
	if !showTabs {
		start = x + w - 1
	}

	titleLimit := start - (x + 2) - 2
	if titleLimit < 4 {
		titleLimit = 4
	}
	DrawBox(a.screen, x, y, w, h, Truncate(title, titleLimit), border, t.Title)

	tabs := &a.lay.paneTabsL
	if side == 1 {
		tabs = &a.lay.paneTabsR
	}
	*tabs = (*tabs)[:0]
	if !showTabs {
		return
	}
	cx := start
	for i := 0; i < int(paneTabCount); i++ {
		own := PaneTab(i) == a.paneTab(side)
		st := t.Dim
		switch {
		case own:
			st = Style{Fg: 81, Bold: true, Reverse: true}
		case a.hoverPaneTab == side && a.hoverPaneTabIndex == i:
			st = t.TabHover
		}
		a.screen.Text(cx, y, labels[i], st)
		a.lay.paneTabsSet(side, i, rect{X: cx, Y: y, W: TextWidth(labels[i]), H: 1})
		cx += TextWidth(labels[i])
		if i < int(paneTabCount)-1 {
			a.screen.Text(cx, y, " · ", t.Dim)
			cx += 3
		}
	}

	content := rect{X: x + 2, Y: y + 1, W: w - 4, H: h - 2}
	a.drawWrapped(content, a.paneLines(side, content.W), scroll)

	if a.paneHint != "" && h >= 5 {
		label := " " + a.paneHint + " "
		if TextWidth(label) < w-4 {
			a.screen.Text(x+w-2-TextWidth(label), y+h-2, label, t.Dim)
		}
		a.paneHint = ""
	}
}

func (a *App) paneLines(side, width int) []tline {
	f := a.detail
	if f == nil {
		return []tline{tl("no flow selected", a.theme.Dim)}
	}
	switch a.paneTab(side) {
	case TabHeaders:
		return a.headerLines(side)
	case TabBody:
		return a.bodyOnlyLines(side)
	default:
		return a.rawLines(side)
	}
}

func (a *App) headerLines(side int) []tline {
	t := a.theme
	f := a.detail
	var out []tline
	if side == 0 {
		out = append(out, tl(fmt.Sprintf("%s %s HTTP/1.1", f.Method, f.Path), t.Accent))
		out = append(out, tl(fmt.Sprintf("scheme %s · client %s", f.Scheme, f.Client), t.Dim))
		if f.Redirected != "" {
			out = append(out, tl("→ substituted by "+f.Redirected, t.Warn))
		}
		out = append(out, tl("", t.Base))
		for _, hd := range f.ReqHeaders {
			out = append(out, headerLine(t, hd.Name, hd.Value))
		}
		if len(f.ReqHeaders) == 0 {
			out = append(out, tl("(no headers captured)", t.Dim))
		}
		return out
	}
	out = append(out, tl("HTTP/1.1 "+statusLine(f), t.StatusStyle(f.Status, f.Err)))
	out = append(out, tl(fmt.Sprintf("took %s · in %s · out %s",
		core.FormatDuration(f.Duration), core.FormatBytes(f.BytesIn), core.FormatBytes(f.BytesOut)), t.Dim))
	if f.Err != "" {
		out = append(out, tl("error: "+f.Err, t.Bad))
	}
	out = append(out, tl("", t.Base))
	for _, hd := range f.RespHeaders {
		out = append(out, headerLine(t, hd.Name, hd.Value))
	}
	if len(f.RespHeaders) == 0 {
		out = append(out, tl("(no response yet)", t.Dim))
	}
	return out
}

func (a *App) bodyOnlyLines(side int) []tline {
	t := a.theme
	f := a.detail
	if side == 0 {
		return bodyLines(t, f.ReqHeaders, f.ReqBody, f.ReqTrunc, t.Base)
	}
	if f.Status == 0 && f.Err != "" {
		return []tline{tl("no response: "+f.Err, t.Bad)}
	}
	return bodyLines(t, f.RespHeaders, f.RespBody, f.RespTrunc, t.Base)
}

func (a *App) rawLines(side int) []tline {
	f := a.detail
	raw := f.RawRequest()
	if side == 1 {
		raw = f.RawResponse()
	}
	var out []tline
	for _, ln := range strings.Split(raw, "\n") {
		out = append(out, tl(ln, Style{Fg: 250}))
	}
	return out
}

func headerLine(t *Theme, name, value string) tline {
	return tline{text: name + ": " + value, st: Style{Fg: 250}}
}

func statusLine(f *core.Flow) string {
	if f.Err != "" {
		return "error: " + Truncate(f.Err, 60)
	}
	if f.Status == 0 {
		return "pending"
	}
	return fmt.Sprintf("%d %s", f.Status, f.Reason)
}

// ---------------------------------------------------------------------------
// flows
// ---------------------------------------------------------------------------

func (a *App) flowsColumns() []Column {
	cols := []Column{
		{Title: "#", Width: 4, Right: true},
		{Title: "Time", Width: 8},
		{Title: "Method", Width: 7},
		{Title: "Host", Flex: 4},
		{Title: "Path", Flex: 5},
		{Title: "Status", Width: 6, Right: true},
		{Title: "Size", Width: 6, Right: true},
		{Title: "Took", Width: 6, Right: true},
		{Title: "Tags", Width: 12},
	}
	if a.W < 104 {
		cols = cols[:len(cols)-1]
	}
	if a.W < 92 {
		cols = append(cols[:7], cols[8:]...)
	}
	if a.W < 78 {
		cols = cols[1:]
	}
	return cols
}

func (a *App) flowRows() [][]TextCell {
	t := a.theme
	rows := make([][]TextCell, 0, len(a.shown))
	for _, f := range a.shown {
		status := f.StatusText()
		if f.State == core.StatePending {
			status = "···"
		}
		tags := strings.Join(f.Tags, ",")
		if f.Redirected != "" && !strings.Contains(tags, "redirect") {
			tags = strings.Trim(tags+",redirect", ",")
		}
		row := []TextCell{
			Plain(fmt.Sprint(f.ID)),
			Plain(f.Start.Format("15:04:05")),
			Colored(f.Method, t.MethodStyle(f.Method)),
			Plain(f.Host),
			Plain(f.URLShort()),
			Colored(status, t.StatusStyle(f.Status, f.Err)),
			Colored(core.FormatBytes(f.Size()), t.Dim),
			Colored(core.FormatDuration(f.Duration), t.Dim),
			Colored(tags, tagStyle(t, f)),
		}
		if a.W < 104 {
			row = row[:len(row)-1]
		}
		if a.W < 92 {
			row = append(row[:7], row[8:]...)
		}
		if a.W < 78 {
			row = row[1:]
		}
		rows = append(rows, row)
	}
	return rows
}

func tagStyle(t *Theme, f *core.Flow) Style {
	switch {
	case f.State == core.StateIntercepted:
		return Style{Fg: 214, Bold: true}
	case f.State == core.StateError, f.HasTag("pinned?"):
		return t.Bad
	case f.HasTag("mock"):
		return t.Good
	case f.HasTag("tunnel"):
		return t.Dim
	case f.HasTag("edit"):
		return Style{Fg: 214}
	}
	return t.Dim
}

func (a *App) renderFlows(y, h int) {
	x := 1
	w := a.W - 2
	tableH := h
	previewH := 0
	if h >= 16 {
		tableH = h * 55 / 100
		if tableH < 8 {
			tableH = 8
		}
		previewH = h - tableH
		if previewH < 6 {
			previewH = 0
			tableH = h
		}
	}
	title := fmt.Sprintf("flows · %d shown / %d captured", len(a.shown), len(a.flows))
	if a.filterText != "" {
		title += " · filter: " + a.filterText
	}
	if a.filterset != nil {
		if exprs := a.filterset.EnabledExprs(); len(exprs) > 0 {
			title += fmt.Sprintf(" · +%d saved", len(exprs))
		}
	}
	a.lay.table = rect{X: x, Y: y, W: w, H: tableH}
	ts := TableStyle{
		Border: a.theme.Border,
		Title:  a.theme.Title,
		Header: a.theme.Header,
		Row:    a.theme.Row,
		RowAlt: a.theme.RowAlt,
		Sel:    a.theme.Sel,
		Hover:  a.theme.RowHover,
	}
	hover := a.hoverRow
	if len(a.shown) == 0 {
		hover = -1
	}
	a.lay.tableY, a.lay.tableN = DrawTable(a.screen, x, y, w, tableH, title,
		a.flowsColumns(), a.flowRows(), a.scroll, a.sel, hover, ts)

	if previewH > 0 {
		a.renderPreview(x, y+tableH, w, previewH)
	}
}

// renderPreview draws the request and response panes under the flow list.
func (a *App) renderPreview(x, y, w, h int) {
	f := a.selectedFlow()
	if f == nil {
		a.detail = nil
		DrawBox(a.screen, x, y, w, h, "preview", a.theme.Border, a.theme.Title)
		a.screen.Text(x+3, y+2, "no flow selected — capture some traffic or relax the filter", a.theme.Dim)
		return
	}
	a.detail = f

	if w < 80 {
		half := h / 2
		a.renderPane(x, y, w, half, 0, fmt.Sprintf("request · %s", f.Method), a.detailScrollL)
		a.renderPane(x, y+half, w, h-half, 1, "response · "+statusLine(f), a.detailScrollR)
		return
	}
	lw := w / 2
	rw := w - lw - 1
	switch {
	case a.collapseL:
		a.paneHint = "request hidden — ["
		a.renderPane(x, y, w, h, 1, "response · "+statusLine(f), a.detailScrollR)
	case a.collapseR:
		a.paneHint = "response hidden — ]"
		a.renderPane(x, y, w, h, 0, fmt.Sprintf("request · %s %s", f.Method, f.URL), a.detailScrollL)
	default:
		a.renderPane(x, y, lw, h, 0,
			fmt.Sprintf("request · %s %s", f.Method, f.URL), a.detailScrollL)
		a.renderPane(x+lw+1, y, rw, h, 1, "response · "+statusLine(f), a.detailScrollR)
	}
}

// ---------------------------------------------------------------------------
// detail
// ---------------------------------------------------------------------------

func (a *App) renderDetail(y, h int) {
	f := a.detail
	if f == nil {
		a.view = ViewFlows
		return
	}
	w := a.W - 2
	x := 1
	a.lay.detail = rect{X: x, Y: y, W: w, H: h}

	if w < 80 {
		half := h / 2
		a.renderPane(x, y, w, half, 0, fmt.Sprintf("request · %s", f.Method), a.detailScrollL)
		a.renderPane(x, y+half, w, h-half, 1, "response · "+statusLine(f), a.detailScrollR)
		return
	}
	lw := w / 2
	rw := w - lw - 1
	switch {
	case a.collapseL:
		a.paneHint = "request hidden — press [ to show it"
		a.renderPane(x, y, w, h, 1, "response · "+statusLine(f), a.detailScrollR)
	case a.collapseR:
		a.paneHint = "response hidden — press ] to show it"
		a.renderPane(x, y, w, h, 0, fmt.Sprintf("request · %s %s", f.Method, f.URL), a.detailScrollL)
	default:
		a.renderPane(x, y, lw, h, 0,
			fmt.Sprintf("request · %s %s", f.Method, f.URL), a.detailScrollL)
		a.renderPane(x+lw+1, y, rw, h, 1, "response · "+statusLine(f), a.detailScrollR)
	}
}

// ---------------------------------------------------------------------------
// body rendering
// ---------------------------------------------------------------------------

func bodyLines(t *Theme, headers []core.Header, body []byte, truncated bool, base Style) []tline {
	if len(body) == 0 {
		if truncated {
			return []tline{tl("(body truncated by capture limit)", t.Warn)}
		}
		return []tline{tl("(empty)", t.Dim)}
	}
	enc := contentEncoding(headers)
	data := decodeBody(headers, body)

	if !isBinary(data) {
		if pretty, ok := prettyJSON(data); ok {
			out := jsonLines(pretty, t)
			if truncated {
				out = append(out, tl("… capture truncated at the configured body limit", t.Warn))
			}
			return out
		}
		var out []tline
		text := string(data)
		for _, ln := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
			out = append(out, tl(strings.ReplaceAll(ln, "\t", "    "), base))
		}
		if truncated {
			out = append(out, tl("… capture truncated at the configured body limit", t.Warn))
		}
		return out
	}

	var out []tline
	switch {
	case enc != "" && !displayDecodable(enc):
		out = append(out, tl(fmt.Sprintf(
			"(%s-compressed body, %s — not decoded; cli-proxy asks the server for gzip/deflate unless -keep-encoding is set)",
			enc, core.FormatBytes(int64(len(body)))), t.Warn))
	default:
		out = append(out, tl(fmt.Sprintf("(binary payload, %s)", core.FormatBytes(int64(len(body)))), t.Warn))
	}
	out = append(out, hexLines(data, 32)...)
	return out
}

// prettyJSON re-indents a JSON document so a long payload becomes readable
// instead of one endlessly long line.
func prettyJSON(data []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 {
		return nil, false
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return nil, false
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, trimmed, "", "  "); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// jsonLines colours an indented JSON document, one tline per source line.
func jsonLines(data []byte, t *Theme) []tline {
	keySt := Style{Fg: 117}
	strSt := Style{Fg: 150}
	numSt := Style{Fg: 180}
	litSt := Style{Fg: 176}

	var out []tline
	for _, raw := range strings.Split(string(data), "\n") {
		out = append(out, runsLine(jsonLineRuns(raw, t, keySt, strSt, numSt, litSt)...))
	}
	return out
}

func jsonLineRuns(raw string, t *Theme, keySt, strSt, numSt, litSt Style) []run {
	var out []run
	push := func(text string, st Style) {
		if text == "" {
			return
		}
		if n := len(out); n > 0 && out[n-1].st == st {
			out[n-1].text += text
			return
		}
		out = append(out, run{text: text, st: st})
	}
	i := 0
	for i < len(raw) {
		c := raw[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(raw) {
				if raw[j] == '\\' {
					j += 2
					continue
				}
				if raw[j] == '"' {
					j++
					break
				}
				j++
			}
			j = min(j, len(raw))
			st := strSt
			if strings.HasPrefix(strings.TrimLeft(raw[j:], " "), ":") {
				st = keySt
			}
			push(raw[i:j], st)
			i = j
		case c == '-' || (c >= '0' && c <= '9'):
			j := i
			for j < len(raw) && (raw[j] == '-' || raw[j] == '+' || raw[j] == '.' ||
				raw[j] == 'e' || raw[j] == 'E' || (raw[j] >= '0' && raw[j] <= '9')) {
				j++
			}
			push(raw[i:j], numSt)
			i = j
		case strings.HasPrefix(raw[i:], "true"), strings.HasPrefix(raw[i:], "false"), strings.HasPrefix(raw[i:], "null"):
			j := i
			for j < len(raw) && raw[j] >= 'a' && raw[j] <= 'z' {
				j++
			}
			push(raw[i:j], litSt)
			i = j
		default:
			j := i + 1
			for j < len(raw) {
				c2 := raw[j]
				if c2 == '"' || c2 == '-' || (c2 >= '0' && c2 <= '9') {
					break
				}
				if strings.HasPrefix(raw[j:], "true") || strings.HasPrefix(raw[j:], "false") ||
					strings.HasPrefix(raw[j:], "null") {
					break
				}
				j++
			}
			push(raw[i:j], t.Base)
			i = j
		}
	}
	return out
}

func hexLines(data []byte, perLine int) []tline {
	var out []tline
	const maxBytes = 1024
	end := len(data)
	if end > maxBytes {
		end = maxBytes
	}
	for off := 0; off < end; off += perLine {
		stop := off + perLine
		if stop > end {
			stop = end
		}
		chunk := data[off:stop]
		var ascii strings.Builder
		for _, b := range chunk {
			if b >= 32 && b < 127 {
				ascii.WriteByte(b)
			} else {
				ascii.WriteByte('.')
			}
		}
		out = append(out, tl(fmt.Sprintf("%06x  %-*s  %s", off, perLine*3, hex.EncodeToString(chunk), ascii.String()),
			Style{Fg: 245}))
	}
	if len(data) > end {
		out = append(out, tl(fmt.Sprintf("… %d more bytes", len(data)-end), Style{Fg: 244}))
	}
	return out
}

func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if !utf8.Valid(data) {
		return true
	}
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

func contentEncoding(headers []core.Header) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, "Content-Encoding") {
			v := strings.ToLower(strings.TrimSpace(h.Value))
			if i := strings.IndexByte(v, ','); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			return v
		}
	}
	return ""
}

func displayDecodable(enc string) bool {
	switch enc {
	case "", "identity", "gzip", "x-gzip", "deflate":
		return true
	}
	return false
}

func decodeBody(headers []core.Header, body []byte) []byte {
	enc := contentEncoding(headers)
	switch enc {
	case "gzip", "x-gzip":
		if r, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
			if data, err := io.ReadAll(io.LimitReader(r, 8<<20)); err == nil {
				return data
			}
		}
	case "deflate":
		if r, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			if data, err := io.ReadAll(io.LimitReader(r, 8<<20)); err == nil {
				return data
			}
		}
	}
	return body
}

// ---------------------------------------------------------------------------
// saved filters
// ---------------------------------------------------------------------------

func (a *App) renderFilters(y, h int) {
	path := ""
	if a.filterset != nil {
		path = a.filterset.Path()
	}
	title := fmt.Sprintf("saved filters · %d defined · %s", len(a.filterList), path)
	a.lay.filters = rect{X: 1, Y: y, W: a.W - 2, H: h}
	cols := []Column{
		{Title: "On", Width: 3, Center: true},
		{Title: "ID", Width: 4, Right: true},
		{Title: "Expression", Flex: 3},
		{Title: "Note", Flex: 1},
	}
	rows := make([][]TextCell, 0, len(a.filterList))
	for _, f := range a.filterList {
		on, st := "·", a.theme.Dim
		if f.Enabled {
			on, st = "✔", a.theme.Good
		}
		exprStyle := Style{Fg: 250}
		if q := core.CompileQuery(f.Expr); q.Err() != nil {
			exprStyle = a.theme.Bad
		}
		rows = append(rows, []TextCell{
			Colored(on, st),
			Plain(fmt.Sprint(f.ID)),
			Colored(f.Expr, exprStyle),
			Colored(f.Note, a.theme.Dim),
		})
	}
	ts := TableStyle{
		Border: a.theme.Border, Title: a.theme.Title, Header: a.theme.Header,
		Row: a.theme.Row, RowAlt: a.theme.RowAlt, Sel: a.theme.Sel, Hover: a.theme.RowHover,
	}
	hover := a.hoverFilter
	if len(a.filterList) == 0 {
		hover = -1
	}
	a.lay.filtersY, a.lay.filtersN = DrawTable(a.screen, 1, y, a.W-2, h, title, cols, rows,
		a.filterScroll, a.filterSel, hover, ts)

	if len(a.filterList) == 0 && h > 8 {
		hints := []string{
			"Saved filters stay switched on while you move between tabs — unlike the",
			"ad-hoc filter bar, which is cleared when you press Esc.",
			"",
			"  [a] add        [e] edit the selected one      [d] delete",
			"  [space] enable/disable                        [c] clear all",
			"  [p] pin whatever is currently typed in the filter bar",
			"",
			"Several filters are combined with AND, and each one supports OR and",
			"parentheses:",
			"    method:GET | method:POST        /users$/ | /orders$/",
			"    (host:a | host:b) status:5xx",
		}
		for i, s := range hints {
			if y+4+i >= y+h-1 {
				break
			}
			st := a.theme.Dim
			if strings.HasPrefix(strings.TrimSpace(s), "[") {
				st = a.theme.Base
			}
			a.screen.Text(4, y+4+i, Truncate(s, a.W-8), st)
		}
	}
}

// ---------------------------------------------------------------------------
// rules
// ---------------------------------------------------------------------------

func (a *App) renderRules(y, h int) {
	title := fmt.Sprintf("rules · %d defined · saved to %s", len(a.rules), a.ruleset.Path())
	a.lay.rules = rect{X: 1, Y: y, W: a.W - 2, H: h}
	cols := []Column{
		{Title: "On", Width: 3, Center: true},
		{Title: "ID", Width: 4, Right: true},
		{Title: "Method", Width: 10},
		{Title: "URL pattern", Flex: 3},
		{Title: "Actions", Flex: 4},
	}
	rows := make([][]TextCell, 0, len(a.rules))
	for _, r := range a.rules {
		on := "·"
		st := a.theme.Dim
		if r.Enabled {
			on = "✔"
			st = a.theme.Good
		}
		actions := make([]string, 0, len(r.Actions))
		for _, act := range r.Actions {
			s := string(act.Kind)
			if act.Arg != "" {
				s += "=" + act.Arg
			}
			actions = append(actions, s)
		}
		method := r.Method
		if method == "" {
			method = "*"
		}
		stText := strings.Join(actions, " · ")
		actStyle := Style{Fg: 250}
		if r.Err() != "" {
			stText = "INVALID: " + r.Err()
			actStyle = a.theme.Bad
		}
		rows = append(rows, []TextCell{
			Colored(on, st),
			Plain(fmt.Sprint(r.ID)),
			Colored(method, a.theme.Accent),
			Plain(r.URL),
			Colored(stText, actStyle),
		})
	}
	ts := TableStyle{
		Border: a.theme.Border, Title: a.theme.Title, Header: a.theme.Header,
		Row: a.theme.Row, RowAlt: a.theme.RowAlt, Sel: a.theme.Sel, Hover: a.theme.RowHover,
	}
	hover := a.hoverRule
	if len(a.rules) == 0 {
		hover = -1
	}
	a.lay.rulesY, a.lay.rulesN = DrawTable(a.screen, 1, y, a.W-2, h, title, cols, rows,
		a.ruleScroll, a.ruleSel, hover, ts)
	if len(a.rules) == 0 && h > 6 {
		a.screen.Text(4, y+4, "no rules yet — press [a] to add one, or [m]/[M]/[i] on a flow to build one automatically", a.theme.Dim)
	}
}

// ---------------------------------------------------------------------------
// certificates
// ---------------------------------------------------------------------------

func (a *App) renderCert(y, h int) {
	t := a.theme
	a.lay.cert = rect{X: 1, Y: y, W: a.W - 2, H: h}
	DrawBox(a.screen, 1, y, a.W-2, h, "certificates · devices · system proxy", t.Border, t.Title)

	innerX, innerW := 3, a.W-6
	innerY := y + 1
	innerH := h - 2
	if innerH < 4 || innerW < 20 {
		return
	}

	wide := innerW >= 96
	leftW := innerW
	rightW := 0
	if wide {
		leftW = innerW * 46 / 100
		if leftW < 42 {
			leftW = 42
		}
		rightW = innerW - leftW - 1
	}

	cert := a.proxy.CA()
	certState, certStyle := "not available on this platform", t.Dim
	if a.trust != nil {
		certState = a.trustStatus.String()
		if a.trustStatus.Installed {
			certStyle = t.Good
		}
	}
	sysState, sysStyle := "off — press s", t.Dim
	switch {
	case a.sysProxy == nil || !a.sysProxy.Supported():
		sysState, sysStyle = "not available", t.Dim
	case a.sysProxy.Active():
		sysState = "on -> " + a.sysProxy.Target()
		sysStyle = t.Good
	}

	// ---- left: authority and status ---------------------------------------
	left := []tline{
		tl("authority", Style{Fg: 81, Bold: true, Underline: true}),
		tl("Subject  "+cert.Cert.Subject.String(), t.Base),
		tl("Expires  "+cert.Cert.NotAfter.Format("2006-01-02"), t.Base),
		tl("", t.Base),
		tl("fingerprint (SHA-256)", Style{Fg: 81, Bold: true, Underline: true}),
	}
	for _, part := range wrapFingerprint(cert.Fingerprint(), maxInt(16, leftW-2)) {
		left = append(left, tl(part, Style{Fg: 245}))
	}
	left = append(left,
		tl("", t.Base),
		tl("status", Style{Fg: 81, Bold: true, Underline: true}),
		tline{text: "Trusted  " + certState, st: certStyle},
		tline{text: "Proxy    " + sysState, st: sysStyle},
		tl("Short URL  "+a.proxy.ShortCertURL(), t.Accent),
		tl("Storage  "+a.proxy.CAPath(), t.Dim),
	)
	if innerH >= 8 {
		left = append(left, tl("", t.Base))
		left = append(left, tl("actions", Style{Fg: 81, Bold: true, Underline: true}))
		left = append(left,
			tl("[i] install for this user", t.Accent),
			tl("[I] install for every user", t.Accent),
			tl("[u] remove from the trust store", t.Accent),
			tl("[s] toggle the system proxy", t.Accent),
		)
	}
	a.drawWrapped(rect{X: innerX, Y: innerY, W: leftW, H: innerH}, left, 0)

	// ---- right: per-platform instructions ---------------------------------
	if wide && rightW > 20 {
		// A hairline keeps the two columns visually apart.
		for yy := innerY; yy < innerY+innerH; yy++ {
			a.screen.Set(innerX+leftW, yy, boxVertical, t.Border)
		}
		rx := innerX + leftW + 2
		a.lay.certTabs = a.lay.certTabs[:0]
		cx := rx
		for i, name := range certTabs {
			label := " " + name + " "
			st := t.Dim
			switch {
			case i == a.certTab:
				st = Style{Fg: 81, Bold: true, Reverse: true}
			case a.hoverCertTab == i:
				st = t.TabHover
			}
			a.screen.Text(cx, innerY, label, st)
			a.lay.certTabs = append(a.lay.certTabs, rect{X: cx, Y: innerY, W: TextWidth(label), H: 1})
			cx += TextWidth(label) + 1
		}
		if cx+8 < rx+rightW {
			a.screen.Text(cx+1, innerY, "(t cycles)", t.Dim)
		}

		host, port := deviceHostPort(a.proxy)
		base := a.proxy.BaseURL()
		lines := []tline{tl("", t.Base)}
		for _, ln := range certTabInstructions[certTabs[a.certTab]] {
			ln = strings.ReplaceAll(ln, "<short>", "http://"+shortHostName())
			ln = strings.ReplaceAll(ln, "<base>", base)
			ln = strings.ReplaceAll(ln, "<host>", host)
			ln = strings.ReplaceAll(ln, "<port>", port)
			st := t.Base
			if strings.HasPrefix(strings.TrimSpace(ln), "NOTE") {
				st = t.Warn
			}
			lines = append(lines, tl(ln, st))
		}
		a.drawWrapped(rect{X: rx, Y: innerY, W: rightW, H: innerH}, lines, 0)
	}
}

// wrapFingerprint breaks a long hex fingerprint into readable chunks.
func wrapFingerprint(fp string, width int) []string {
	parts := strings.Split(fp, ":")
	if len(parts) < 2 {
		return []string{fp}
	}
	var out []string
	cur := ""
	for i := 0; i+1 < len(parts); i += 2 {
		pair := parts[i] + ":" + parts[i+1]
		switch {
		case cur == "":
			cur = pair
		case TextWidth(cur)+3 > width:
			out = append(out, cur)
			cur = pair
		default:
			cur += ":" + pair
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// ---------------------------------------------------------------------------
// log
// ---------------------------------------------------------------------------

func (a *App) renderLog(y, h int) {
	t := a.theme
	a.lay.log = rect{X: 1, Y: y, W: a.W - 2, H: h}
	DrawBox(a.screen, 1, y, a.W-2, h, "engine log", t.Border, t.Title)
	entries := a.elog.Tail(2000)
	var lines []tline
	for _, e := range entries {
		st := t.Base
		switch {
		case strings.Contains(e.Text, "failed"), strings.Contains(e.Text, "error"):
			st = t.Bad
		case strings.Contains(e.Text, "listening"):
			st = t.Good
		}
		lines = append(lines, tl(e.At.Format("15:04:05.000 ")+e.Text, st))
	}
	scroll := a.logScroll
	if max := a.maxScroll(lines, a.W-6, h-2); scroll > max {
		scroll = max
	}
	a.drawWrapped(rect{X: 3, Y: y + 1, W: a.W - 6, H: h - 2}, lines, scroll)
}

// ---------------------------------------------------------------------------
// help
// ---------------------------------------------------------------------------

type helpRow struct{ k, v string }
type helpSection struct {
	title string
	rows  []helpRow
}

func helpSections() []helpSection {
	kv := func(k, v string) helpRow { return helpRow{k, v} }
	return []helpSection{
		{"Menu & panes", []helpRow{
			kv("← → / 1..6", "switch tab (menu bar)"),
			kv("Tab", "swap request/response pane"),
			kv("Shift-Tab", "cycle the pane tab"),
			kv("h / r", "pane tab: headers, raw"),
			kv("b", "pane tab: body (detail view)"),
			kv("[ / ]", "hide the left / right pane"),
			kv("\\", "show both panes again"),
			kv("click a tab", "switch that pane directly"),
			kv("?", "this help"),
			kv("q / Ctrl-C", "quit"),
		}},
		{"Flows", []helpRow{
			kv("↑ ↓ / j k", "move selection"),
			kv("PgUp PgDn / Home End", "page and jump"),
			kv("Enter / double click", "open the full detail view"),
			kv("f or /", "filter & search (see below)"),
			kv("b / B", "breakpoint on all requests / responses"),
			kv("x", "release every paused breakpoint"),
			kv("m", "mock this response from a local file"),
			kv("M", "fetch it from another address instead"),
			kv("i", "intercept this URL (break=both rule)"),
			kv("s", "write the exchange to a file"),
			kv("c", "clear the captured list"),
			kv("p", "change the listen port"),
		}},
		{"Mouse", []helpRow{
			kv("click", "select a tab, row, pane tab or button"),
			kv("hover", "highlight whatever is under the pointer"),
			kv("wheel", "scroll lists and panes"),
			kv("double click", "open the flow under the pointer"),
		}},
		{"Saved filters", []helpRow{
			kv("space", "enable / disable the selected filter"),
			kv("a / e / d / c", "add, edit, delete, clear"),
			kv("p", "pin the ad-hoc filter bar as a saved one"),
			kv("note", "all enabled filters are ANDed together"),
		}},
		{"Breakpoint editor", []helpRow{
			kv("type / Enter", "edit the raw HTTP message"),
			kv("Ctrl-S or F10", "send the edited message"),
			kv("Esc", "drop the exchange"),
			kv("click", "place the caret, PgUp/PgDn scroll"),
		}},
		{"Rules", []helpRow{
			kv("Space / click", "enable or disable"),
			kv("a / e / d", "add, edit, delete"),
			kv("c", "remove every rule"),
		}},
		{"Certificate", []helpRow{
			kv("i / I / u", "install for this user / all / remove"),
			kv("s", "toggle the system proxy (restored on exit)"),
			kv("t", "cycle the instruction tab"),
			kv("o", "open the install page in a browser"),
		}},
		{"Filter syntax", []helpRow{
			kv("plain text", "substring of the URL"),
			kv("/regex/", "regular expression (no spaces inside)"),
			kv("method:GET,POST", "HTTP method"),
			kv("status:2xx  status:404", "status class or exact code"),
			kv("status:>=400  status:0", "comparisons, or no response at all"),
			kv("host: / body: / scheme:", "host, payload, http or https"),
			kv("tag:mock", "flow marker"),
			kv("!token", "negate"),
			kv("a b", "AND"),
			kv("a | b      a OR b", "OR"),
			kv("(a | b) c", "grouping"),
		}},
		{"Rule DSL", []helpRow{
			kv("* ^https://api\\.x\\.com/v1 :: file=/tmp/u.json", "serve a local file"),
			kv("GET .*/legacy$ :: redirect=https://staging/x", "fetch from elsewhere"),
			kv("* ^http://cdn/logo\\.png :: location=https://y", "reply with a 302"),
			kv("* example\\.com :: header=X-Debug=1 :: break=req", "rewrite and pause"),
			kv("* ^http://x/ :: status=503 :: body={\"down\":1}", "synthesise a response"),
			kv("* ^http://y/ :: block=403", "refuse the request"),
			kv("* ^http://z/ :: delay=750ms", "add latency"),
			kv("* /static/ :: maplocal=/static/=/var/www", "serve a directory tree"),
		}},
	}
}

func renderHelpSection(s helpSection, t *Theme) []tline {
	out := []tline{tl("", t.Base), tl(s.title, Style{Fg: 81, Bold: true, Underline: true})}
	for _, r := range s.rows {
		// Long keys (the rule DSL examples) get their description on the next
		// line so nothing is squeezed.
		if TextWidth(r.k) > 24 {
			out = append(out, tline{text: "  " + r.k, st: Style{Fg: 250}})
			out = append(out, tline{text: "      → " + r.v, st: t.Dim})
			continue
		}
		out = append(out, tline{text: fmt.Sprintf("  %-24s %s", r.k, r.v), st: t.Base})
	}
	return out
}

func (a *App) renderHelp(y, h int) {
	t := a.theme
	a.lay.help = rect{X: 1, Y: y, W: a.W - 2, H: h}
	DrawBox(a.screen, 1, y, a.W-2, h, "keyboard & mouse reference", t.Border, t.Title)

	sections := helpSections()
	innerH := h - 2

	// Wrap each section up front so both layouts can reuse the result.
	wrapped := make([][]tline, len(sections))
	for i, s := range sections {
		wrapped[i] = renderHelpSection(s, t)
	}

	colW := (a.W - 7) / 2
	if colW < 44 {
		var all []tline
		for _, w := range wrapped {
			all = append(all, w...)
		}
		scroll := a.helpScroll
		if max := a.maxScroll(all, a.W-8, innerH); scroll > max {
			scroll = max
		}
		a.drawWrapped(rect{X: 3, Y: y + 1, W: a.W - 6, H: innerH}, all, scroll)
		return
	}

	// Balance the two columns by height rather than by section count.
	total := 0
	for _, w := range wrapped {
		total += len(w)
	}
	split, acc := len(wrapped), 0
	for i, w := range wrapped {
		if acc+len(w) > total/2 && i > 0 {
			split = i
			break
		}
		acc += len(w)
	}
	var left, right []tline
	for i, w := range wrapped {
		if i < split {
			left = append(left, w...)
		} else {
			right = append(right, w...)
		}
	}
	height := maxInt(len(left), len(right))
	scroll := a.helpScroll
	if max := height - innerH; scroll > max {
		scroll = max
	}
	if scroll < 0 {
		scroll = 0
	}
	a.drawLines(rect{X: 3, Y: y + 1, W: colW, H: innerH}, left, scroll)
	a.drawLines(rect{X: 4 + colW, Y: y + 1, W: colW, H: innerH}, right, scroll)
}

// ---------------------------------------------------------------------------
// editor
// ---------------------------------------------------------------------------

func (a *App) renderEditor(y, h int) {
	t := a.theme
	title := a.editorTitle
	if a.editorBreak != nil {
		title = fmt.Sprintf("breakpoint #%d · %s", a.editorBreak.ID, strings.ToUpper(string(a.editorBreak.Phase)))
		if a.editorBreak.Phase == core.PhaseRequest {
			title += " — edit headers, method, URL or body, then send"
		} else {
			title += " — edit status line, headers or body, then send"
		}
	}
	a.lay.editor = rect{X: 1, Y: y, W: a.W - 2, H: h}
	DrawBox(a.screen, 1, y, a.W-2, h, title, t.Border, t.Title)

	gutter := a.editGutter()
	inner := rect{X: 3 + gutter, Y: y + 1, W: a.W - 6 - gutter, H: h - 2}
	a.lay.editRows = inner.H
	a.lay.editCols = inner.W

	e := a.editor
	if e == nil {
		return
	}
	e.EnsureVisible(inner.W, inner.H)

	for i := 0; i < inner.H; i++ {
		idx := e.Top + i
		if idx >= len(e.Lines) {
			break
		}
		line := e.Lines[idx]
		num := fmt.Sprintf("%4d ", idx+1)
		st := t.Dim
		if idx == e.CY {
			st = t.Accent
		}
		a.screen.Text(2, inner.Y+i, num, st)
		a.screen.Set(2+gutter-1, inner.Y+i, boxVertical, t.Border)

		visible := line
		if e.Left < len(visible) {
			visible = visible[e.Left:]
		} else {
			visible = nil
		}
		text := string(visible)
		style := t.Base
		if idx == 0 {
			style = Style{Fg: 81, Bold: true}
		} else if idx == e.CY {
			style = Style{Fg: 250}
		}
		a.screen.Text(inner.X, inner.Y+i, Truncate(text, inner.W), style)
	}
	cursorVisible := false
	if e.CY >= e.Top && e.CY < e.Top+inner.H {
		col := e.CX - e.Left
		if col >= 0 && col < inner.W {
			a.cursorX = inner.X + col
			a.cursorY = inner.Y + (e.CY - e.Top)
			cursorVisible = true
		}
	}
	a.cursorVisible = cursorVisible
	if a.editorNotice != "" && h > 4 {
		a.screen.Text(4, y+h-2, Truncate(a.editorNotice, a.W-10), t.Dim)
	}
}

// ---------------------------------------------------------------------------
// prompt & buttons
// ---------------------------------------------------------------------------

func (a *App) renderPrompt(row int) {
	p := a.p
	t := a.theme
	a.screen.Set(0, row, boxVertical, t.Border)
	a.screen.Set(a.W-1, row, boxVertical, t.Border)
	for x := 1; x < a.W-1; x++ {
		a.screen.Set(x, row, ' ', t.Base)
	}
	valStart := a.screen.Text(2, row, p.Label+": ", t.Accent)
	text := p.text()
	a.screen.Text(valStart, row, Truncate(text, a.W-valStart-4), t.Base)
	if p.Hint != "" {
		hint := Truncate(p.Hint, maxInt(0, a.W-valStart-TextWidth(text)-6))
		if hint != "" {
			a.screen.Text(a.W-2-TextWidth(hint), row, hint, t.Dim)
		}
	}
	a.cursorVisible = true
	a.cursorX = valStart + p.CX
	a.cursorY = row
}

func (a *App) currentButtons() []button {
	switch a.view {
	case ViewFlows:
		return []button{
			{Label: "open", Key: "↵", Action: func() {
				if f := a.selectedFlow(); f != nil {
					a.openDetail(f)
				}
			}},
			{Label: "filter", Key: "f", Action: a.openFilter},
			{Label: "saved", Key: "2", Action: func() { a.view = ViewFilters; a.refresh() }},
			{Label: "port", Key: "p", Action: a.openPortPrompt},
			{Label: "brk-req", Key: "b", Action: func() {
				on := !a.opts.BreakRequests.Load()
				a.opts.BreakRequests.Store(on)
				a.status(fmt.Sprintf("break requests: %v", on), 0)
			}},
			{Label: "brk-resp", Key: "B", Action: func() {
				on := !a.opts.BreakResponses.Load()
				a.opts.BreakResponses.Store(on)
				a.status(fmt.Sprintf("break responses: %v", on), 0)
			}},
			{Label: "mock", Key: "m", Action: a.mockSelected},
			{Label: "redir", Key: "M", Action: a.redirectSelected},
			{Label: "break", Key: "i", Action: a.breakSelected},
			{Label: "save", Key: "s", Action: a.saveSelected},
			{Label: "clear", Key: "c", Action: func() { a.store.Clear() }},
			{Label: "help", Key: "?", Action: func() { a.prevView = a.view; a.view = ViewHelp }},
			{Label: "quit", Key: "q", Action: func() { a.quit = true }},
		}
	case ViewFilters:
		return []button{
			{Label: "toggle", Key: "space", Action: func() { a.handleFiltersKey(term.Key{Rune: ' '}) }},
			{Label: "add", Key: "a", Action: func() { a.handleFiltersKey(term.Key{Rune: 'a'}) }},
			{Label: "edit", Key: "e", Action: func() { a.handleFiltersKey(term.Key{Rune: 'e'}) }},
			{Label: "delete", Key: "d", Action: func() { a.handleFiltersKey(term.Key{Rune: 'd'}) }},
			{Label: "pin current", Key: "p", Action: func() { a.handleFiltersKey(term.Key{Rune: 'p'}) }},
			{Label: "clear-all", Key: "c", Action: func() { a.handleFiltersKey(term.Key{Rune: 'c'}) }},
			{Label: "flows", Key: "1", Action: func() { a.view = ViewFlows; a.refresh() }},
			{Label: "help", Key: "?", Action: func() { a.prevView = a.view; a.view = ViewHelp }},
			{Label: "quit", Key: "q", Action: func() { a.quit = true }},
		}
	case ViewRules:
		return []button{
			{Label: "toggle", Key: "space", Action: func() {
				if a.ruleSel >= 0 && a.ruleSel < len(a.rules) {
					a.ruleset.Toggle(a.rules[a.ruleSel].ID)
					a.refresh()
				}
			}},
			{Label: "add", Key: "a", Action: func() { a.handleRulesKey(term.Key{Rune: 'a'}) }},
			{Label: "edit", Key: "e", Action: func() { a.handleRulesKey(term.Key{Rune: 'e'}) }},
			{Label: "delete", Key: "d", Action: func() { a.handleRulesKey(term.Key{Rune: 'd'}) }},
			{Label: "clear-all", Key: "c", Action: func() { a.ruleset.Clear(); a.refresh() }},
			{Label: "flows", Key: "1", Action: func() { a.view = ViewFlows }},
			{Label: "help", Key: "?", Action: func() { a.prevView = a.view; a.view = ViewHelp }},
			{Label: "quit", Key: "q", Action: func() { a.quit = true }},
		}
	case ViewCert:
		return []button{
			{Label: "install cert", Key: "i", Action: func() { a.installCert(false) }},
			{Label: "system-wide", Key: "I", Action: func() { a.installCert(true) }},
			{Label: "remove cert", Key: "u", Action: a.uninstallCert},
			{Label: "sys-proxy", Key: "s", Action: a.toggleSystemProxy},
			{Label: "browser", Key: "o", Action: a.openInBrowser},
			{Label: "port", Key: "p", Action: a.openPortPrompt},
			{Label: "help", Key: "?", Action: func() { a.prevView = a.view; a.view = ViewHelp }},
			{Label: "quit", Key: "q", Action: func() { a.quit = true }},
		}
	case ViewDetail:
		return []button{
			{Label: "back", Key: "esc", Action: func() { a.view = ViewFlows }},
			{Label: "pane", Key: "tab", Action: func() { a.focus = 1 - a.focus }},
			{Label: "next tab", Key: "s-tab", Action: func() {
				a.setPaneTab(a.focus, a.paneTab(a.focus).next(1))
			}},
			{Label: "hide left [", Action: func() { a.collapseL = !a.collapseL }},
			{Label: "hide right ]", Action: func() { a.collapseR = !a.collapseR }},
			{Label: "show both \\", Action: func() { a.collapseL, a.collapseR = false, false }},
			{Label: "mock", Key: "m", Action: a.mockSelected},
			{Label: "save", Key: "s", Action: a.saveSelected},
			{Label: "quit", Key: "q", Action: func() { a.quit = true }},
		}
	case ViewEditor:
		return []button{
			{Label: "send", Key: "^S", Action: func() { a.submitEditor("") }},
			{Label: "drop", Key: "esc", Action: func() { a.submitEditor("drop") }},
			{Label: "quit", Key: "q", Action: func() { a.quit = true }},
		}
	}
	return []button{
		{Label: "scroll", Key: "↑↓", Action: func() {}},
		{Label: "flows", Key: "1", Action: func() { a.view = ViewFlows }},
		{Label: "quit", Key: "q", Action: func() { a.quit = true }},
	}
}

func (a *App) renderButtons(row int) {
	t := a.theme
	for x := 0; x < a.W; x++ {
		a.screen.Set(x, row, boxHorizontal, t.Border)
	}
	a.screen.Set(0, row, boxBottomLeft, t.Border)
	if a.W > 1 {
		a.screen.Set(a.W-1, row, boxBottomRight, t.Border)
	}
	btns := a.currentButtons()
	a.lay.buttons = make([]button, 0, len(btns))
	if len(btns) == 0 {
		a.hoverBtn = -1
		return
	}

	tailCount := 2
	if len(btns) < tailCount {
		tailCount = len(btns)
	}
	tailW := 0
	for i := len(btns) - tailCount; i < len(btns); i++ {
		if i > len(btns)-tailCount {
			tailW += 2
		}
		tailW += TextWidth(buttonLabel(btns[i]))
	}
	tailX := a.W - 3 - tailW
	if tailX < 3 {
		tailX = 3
	}

	x := 3
	head := len(btns) - tailCount
	for i := 0; i < head; i++ {
		b := btns[i]
		label := buttonLabel(b)
		w := TextWidth(label)
		if x+w > tailX-2 {
			break
		}
		st := t.Btn
		if a.hoverBtn == len(a.lay.buttons) {
			st = t.BtnHover
		}
		a.screen.Text(x, row, label, st)
		b.X, b.W, b.row = x, w, row
		a.lay.buttons = append(a.lay.buttons, b)
		x += w + 2
	}
	for i := head; i < len(btns); i++ {
		b := btns[i]
		label := buttonLabel(b)
		w := TextWidth(label)
		st := t.Btn
		if a.hoverBtn == len(a.lay.buttons) {
			st = t.BtnHover
		}
		a.screen.Text(tailX, row, label, st)
		b.X, b.W, b.row = tailX, w, row
		a.lay.buttons = append(a.lay.buttons, b)
		tailX += w + 2
	}
}

func buttonLabel(b button) string {
	if b.Key == "" {
		return b.Label
	}
	return "[" + b.Key + "]" + b.Label
}

func shortHostName() string { return "cli.proxy" }

// deviceHostPort is the address a phone or tablet should be pointed at, which
// is the LAN address rather than the wildcard the proxy listens on.
func deviceHostPort(px interface {
	Addr() string
	BaseURL() string
}) (string, string) {
	_, port, err := splitHostPortLocal(px.Addr())
	if err != nil {
		port = "8080"
	}
	host := "127.0.0.1"
	if ips := proxy.LocalIPs(); len(ips) > 0 {
		host = ips[0]
	}
	return host, port
}

func splitHostPortLocal(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, "", nil
	}
	return strings.Trim(addr[:i], "[]"), addr[i+1:], nil
}
