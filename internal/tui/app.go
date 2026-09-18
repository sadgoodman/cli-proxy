package tui

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"cliproxy/internal/core"
	"cliproxy/internal/proxy"
	"cliproxy/internal/term"
	"cliproxy/internal/trust"
)

const (
	altOn    = "\x1b[?1049h"
	altOff   = "\x1b[?1049l"
	curHide  = "\x1b[?25l"
	curShow  = "\x1b[?25h"
	mouseOn  = "\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h"
	mouseOff = "\x1b[?1006l\x1b[?1003l\x1b[?1002l\x1b[?1000l"
	syncOn   = "\x1b[?2026h"
	syncOff  = "\x1b[?2026l"
	clearAll = "\x1b[2J\x1b[H"
)

// ViewKind enumerates the top level screens.
type ViewKind int

const (
	ViewFlows ViewKind = iota
	ViewRules
	ViewCert
	ViewLog
	ViewHelp
	ViewDetail
	ViewEditor
	ViewFilters
)

// PaneTab selects which part of a message a pane shows.
type PaneTab int

const (
	TabHeaders PaneTab = iota
	TabBody
	TabRaw
	paneTabCount
)

var paneTabNames = [paneTabCount]string{"headers", "body", "raw"}

func (t PaneTab) String() string { return paneTabNames[t%paneTabCount] }

// next cycles the tab forward or backward.
func (t PaneTab) next(dir int) PaneTab {
	return PaneTab((int(t) + dir + int(paneTabCount)) % int(paneTabCount))
}

var tabNames = []struct {
	Name string
	View ViewKind
}{
	{"Flows", ViewFlows},
	{"Filters", ViewFilters},
	{"Rules", ViewRules},
	{"Cert", ViewCert},
	{"Log", ViewLog},
	{"Help", ViewHelp},
}

type rect struct{ X, Y, W, H int }

func (r rect) contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

type button struct {
	Label  string
	Key    string
	X, W   int
	row    int
	Action func()
}

type prompt struct {
	Label    string
	Value    []rune
	CX       int
	Hint     string
	IsFilter bool
	OnSubmit func(string)
	OnCancel func()
	OnChange func(string)
}

func (p *prompt) text() string { return string(p.Value) }

// SystemProxy is the slice of the OS proxy controller the UI needs.
type SystemProxy interface {
	Supported() bool
	Active() bool
	Target() string
	EnableAddr(addr string) error
	Disable() error
}

// certResult is delivered by the background certificate operation.
type certResult struct {
	action string // "install" | "uninstall"
	where  string
	err    error
}

// CertTrust installs and removes the root certificate.
type CertTrust interface {
	Available() bool
	Check() (trust.Status, error)
	Install(system bool) (string, error)
	Uninstall(system bool) error
}

// Config wires the UI to the running engine.
type Config struct {
	Proxy       *proxy.Proxy
	Store       *core.Store
	Rules       *core.RuleSet
	Breaker     *core.Breaker
	Opts        *core.Options
	Log         *core.Logger
	Version     string
	SystemProxy SystemProxy
	Trust       CertTrust
	Filters     *core.FilterSet
	// Stop asks the UI to exit cleanly (used for SIGINT/SIGTERM) so the
	// terminal and the system proxy are restored.
	Stop <-chan struct{}
}

// App is the terminal user interface.
type App struct {
	cfg      Config
	proxy    *proxy.Proxy
	store    *core.Store
	ruleset  *core.RuleSet
	brk      *core.Breaker
	opts     *core.Options
	elog     *core.Logger
	theme    *Theme
	sysProxy SystemProxy
	trust    CertTrust

	trustStatus  trust.Status
	trustChecked time.Time
	certCh       chan certResult
	certPending  bool

	filterset    *core.FilterSet
	filterList   []core.Filter
	filterSel    int
	filterScroll int
	// Message panes: which tab each side shows, which one has focus, and
	// whether either side is collapsed away.
	reqTab    PaneTab
	respTab   PaneTab
	focus     int // 0 = request, 1 = response
	collapseL bool
	collapseR bool

	certTab     int
	helpScroll2 int
	paneHint    string

	in     *term.Reader
	out    *bufio.Writer
	screen *Screen
	prev   *Screen

	W, H int

	view     ViewKind
	prevView ViewKind

	flows  []*core.Flow
	shown  []*core.Flow
	sel    int
	follow bool
	scroll int

	hoverTab          int
	hoverRow          int
	hoverBtn          int
	hoverRule         int
	hoverFilter       int
	hoverPaneTab      int
	hoverPaneTabIndex int
	hoverCertTab      int

	query      *core.Query
	filterText string

	detailID      int64
	detail        *core.Flow
	detailScrollL int
	detailScrollR int

	rules      []core.Rule
	ruleSel    int
	ruleScroll int

	certScroll int
	logScroll  int
	helpScroll int

	editor       *Editor
	editorTitle  string
	editorBreak  *core.Breakpoint
	editorNotice string

	p *prompt

	lay layout

	cursorX, cursorY int
	cursorVisible    bool
	showCursor       bool

	statusText string
	statusAt   time.Time
	statusKind int // 0 info, 1 good, 2 bad

	quit     bool
	dirty    bool
	resizeTo int
	started  time.Time

	lastClickAt time.Time
	lastClickY  int
}

type layout struct {
	tabs      []rect
	paneTabsL []rect
	paneTabsR []rect
	certTabs  []rect
	filters   rect
	filtersY  int
	filtersN  int
	table     rect
	tableY    int
	tableN    int
	preview   rect
	rules     rect
	rulesY    int
	rulesN    int
	detail    rect
	editor    rect
	editRows  int
	editCols  int
	cert      rect
	log       rect
	help      rect
	buttons   []button
	prompt    rect
	status    rect
}

// paneTabsSet records a pane tab hit area.
func (l *layout) paneTabsSet(side, index int, r rect) {
	if side == 0 {
		for len(l.paneTabsL) <= index {
			l.paneTabsL = append(l.paneTabsL, rect{})
		}
		l.paneTabsL[index] = r
		return
	}
	for len(l.paneTabsR) <= index {
		l.paneTabsR = append(l.paneTabsR, rect{})
	}
	l.paneTabsR[index] = r
}

// Run starts the UI and blocks until the user quits.
func Run(cfg Config) error {
	a := &App{
		cfg:          cfg,
		proxy:        cfg.Proxy,
		store:        cfg.Store,
		ruleset:      cfg.Rules,
		brk:          cfg.Breaker,
		opts:         cfg.Opts,
		elog:         cfg.Log,
		theme:        DefaultTheme(),
		sysProxy:     cfg.SystemProxy,
		trust:        cfg.Trust,
		filterset:    cfg.Filters,
		sel:          0,
		follow:       true,
		hoverTab:     -1,
		hoverRow:     -1,
		hoverBtn:     -1,
		hoverRule:    -1,
		hoverFilter:  -1,
		hoverPaneTab: -1,
		hoverCertTab: -1,
		started:      time.Now(),
	}
	a.out = bufio.NewWriterSize(os.Stdout, 1<<16)
	if err := term.EnableVT(os.Stdout); err != nil {
		// Not fatal: the terminal simply may not support VT sequences.
		_ = err
	}
	state, err := term.MakeRaw(os.Stdin)
	if err != nil {
		return fmt.Errorf("cannot switch terminal to raw mode: %w", err)
	}
	a.in = term.NewReader(os.Stdin)

	a.out.WriteString(altOn + mouseOn + curHide)
	_ = a.out.Flush()

	defer func() {
		a.in.Close()
		a.out.WriteString(curShow + mouseOff + altOff)
		_ = a.out.Flush()
		_ = state.Restore(os.Stdin)
	}()

	a.certCh = make(chan certResult, 4)

	if a.filterset != nil {
		if err := a.filterset.Load(); err != nil {
			a.elog.Addf("cannot load saved filters: %v", err)
		}
		a.filterList = a.filterset.List()
	}

	a.W, a.H = a.terminalSize()
	a.screen = NewScreen(a.W, a.H)
	a.prev = NewScreen(a.W, a.H)
	a.refresh()

	storeCh, unsub := a.store.Subscribe()
	defer unsub()
	bpCh := a.brk.Changes()

	var stopCh <-chan struct{}
	if cfg.Stop != nil {
		stopCh = cfg.Stop
	} else {
		stopCh = make(chan struct{})
	}

	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	needsDraw := true
	for !a.quit {
		if needsDraw {
			needsDraw = false
			a.draw()
		}
		select {
		case ev, ok := <-a.in.Events():
			if !ok {
				a.quit = true
				continue
			}
			a.handle(ev)
			needsDraw = true
		case <-storeCh:
			a.refresh()
			needsDraw = true
		case <-bpCh:
			a.onBreakpointChange()
			needsDraw = true
		case res := <-a.certCh:
			a.finishCert(res)
			needsDraw = true
		case <-stopCh:
			a.quit = true
		case <-tick.C:
			if a.view == ViewCert && time.Since(a.trustChecked) > 3*time.Second {
				a.refreshTrust()
			}
			if w, h := a.terminalSize(); w != a.W || h != a.H {
				a.W, a.H = w, h
				needsDraw = true
			}
			// Repaint periodically so the clock and live counters stay fresh.
			needsDraw = true
		}
	}
	return nil
}

// w and h are the cached terminal size.
func (a *App) terminalSize() (int, int) {
	w, h, err := term.Size(os.Stdout)
	if err != nil {
		if w2, h2, err2 := term.Size(os.Stdin); err2 == nil {
			return w2, h2
		}
		if w, h = a.W, a.H; w > 0 && h > 0 {
			return w, h
		}
		return 100, 30
	}
	if w < 40 {
		w = 40
	}
	if h < 10 {
		h = 10
	}
	return w, h
}

func (a *App) draw() {
	if a.W != a.screen.W || a.H != a.screen.H {
		a.screen.Resize(a.W, a.H)
		a.prev.Resize(a.W, a.H)
	}
	a.screen.Clear()
	a.render()
	frame := a.screen.Render(a.prev, false)
	a.screen, a.prev = a.prev, a.screen

	var b strings.Builder
	b.WriteString(syncOn)
	b.WriteString(frame)
	if a.cursorVisible {
		fmt.Fprintf(&b, "\x1b[%d;%dH%s", a.cursorY+1, a.cursorX+1, curShow)
	} else {
		b.WriteString(curHide)
	}
	b.WriteString(syncOff)
	a.out.WriteString(b.String())
	_ = a.out.Flush()
}

// ---------------------------------------------------------------------------
// state
// ---------------------------------------------------------------------------

func (a *App) refresh() {
	a.flows = a.store.Snapshot()
	a.applyFilter()
	if a.view == ViewDetail && a.detailID != 0 {
		if f, ok := a.store.Get(a.detailID); ok {
			a.detail = f
		}
	}
	if a.view == ViewRules {
		a.rules = a.ruleset.List()
	}
	if a.view == ViewFilters {
		a.refreshFilters()
	}
}

func (a *App) applyFilter() {
	q := core.CompileQuery(a.filterText)
	a.query = q
	a.shown = a.shown[:0]
	for _, f := range a.flows {
		if !q.Match(f) {
			continue
		}
		if a.filterset != nil && !a.filterset.Match(f) {
			continue
		}
		a.shown = append(a.shown, f)
	}
	if a.follow {
		a.sel = len(a.shown) - 1
	}
	if a.sel >= len(a.shown) {
		a.sel = len(a.shown) - 1
		a.follow = true
	}
	if a.sel < 0 {
		a.sel = 0
	}
	a.ensureVisible()
}

func (a *App) ensureVisible() {
	rows := a.lay.tableN
	if rows <= 0 {
		rows = a.H - 8
	}
	if rows < 1 {
		rows = 1
	}
	if a.sel < a.scroll {
		a.scroll = a.sel
	}
	if a.sel >= a.scroll+rows {
		a.scroll = a.sel - rows + 1
	}
	if a.scroll < 0 {
		a.scroll = 0
	}
}

func (a *App) selectedFlow() *core.Flow {
	if a.sel < 0 || a.sel >= len(a.shown) {
		return nil
	}
	return a.shown[a.sel]
}

func (a *App) status(msg string, kind int) {
	a.statusText = msg
	a.statusAt = time.Now()
	a.statusKind = kind
}

func (a *App) onBreakpointChange() {
	if a.view == ViewEditor {
		// Keep editing the current breakpoint unless it was released.
		if a.editorBreak != nil {
			for _, bp := range a.brk.Pending() {
				if bp.ID == a.editorBreak.ID {
					return
				}
			}
			a.editorBreak = nil
			a.editor = nil
			a.view = ViewFlows
		}
	}
	pending := a.brk.Pending()
	if len(pending) == 0 {
		return
	}
	if a.opts.AutoJump.Load() || a.view == ViewFlows {
		a.openEditor(pending[0])
	}
}

func (a *App) openEditor(bp *core.Breakpoint) {
	a.editorBreak = bp
	a.editor = NewEditor(bp.Raw)
	a.editorTitle = fmt.Sprintf("%s · %s %s", strings.ToUpper(string(bp.Phase)), bp.Flow.Method, bp.Flow.URL)
	a.editorNotice = "edit freely, then send or drop"
	a.prevView = a.view
	a.view = ViewEditor
}

// ---------------------------------------------------------------------------
// input dispatch
// ---------------------------------------------------------------------------

func (a *App) handle(ev term.Event) {
	switch {
	case ev.Kind == term.EvMouse:
		a.handleMouse(ev.Mouse)
	case a.p != nil:
		a.handlePromptKey(ev.Key)
	default:
		a.handleKey(ev.Key)
	}
	// Drilling into a flow must not leave the filter prompt holding the
	// keyboard, or the new view would look frozen.
	if a.p != nil && a.p.IsFilter && a.view != ViewFlows {
		text := a.p.text()
		submit := a.p.OnSubmit
		a.p = nil
		if submit != nil {
			submit(text)
		}
	}
}

func (a *App) handlePromptKey(k term.Key) {
	p := a.p

	// While the filter prompt is open the captured list must stay navigable, so
	// the arrow and paging keys drive the list instead of the prompt. Only the
	// horizontal keys move the caret, because the prompt is a single line.
	if p.IsFilter {
		switch {
		case k.Is("up"), k.Is("down"), k.Is("pgup"), k.Is("pgdn"):
			a.handleFlowsKey(k)
			return
		}
	}

	switch {
	case k.Is("esc") || (k.Ctrl && k.Is("c")):
		if p.OnCancel != nil {
			p.OnCancel()
		}
		a.p = nil
	case k.Is("enter"):
		val := p.text()
		a.p = nil
		if p.OnSubmit != nil {
			p.OnSubmit(val)
		}
	case k.Is("backspace"):
		if p.CX > 0 {
			p.Value = append(p.Value[:p.CX-1], p.Value[p.CX:]...)
			p.CX--
		}
		a.promptChanged()
	case k.Is("delete"):
		if p.CX < len(p.Value) {
			p.Value = append(p.Value[:p.CX], p.Value[p.CX+1:]...)
		}
		a.promptChanged()
	case k.Is("left"):
		if p.CX > 0 {
			p.CX--
		}
	case k.Is("right"):
		if p.CX < len(p.Value) {
			p.CX++
		}
	case k.Is("home"):
		p.CX = 0
	case k.Is("end"):
		p.CX = len(p.Value)
	case k.Rune != 0 && (k.IsRune() || k.Rune == ' ') && !k.Ctrl:
		p.Value = append(p.Value, 0)
		copy(p.Value[p.CX+1:], p.Value[p.CX:])
		p.Value[p.CX] = k.Rune
		p.CX++
		a.promptChanged()
	}
}

func (a *App) promptChanged() {
	if a.p != nil && a.p.OnChange != nil {
		a.p.OnChange(a.p.text())
	}
}

func (a *App) handleKey(k term.Key) {
	if k.Ctrl && k.Is("c") {
		a.quit = true
		return
	}
	switch a.view {
	case ViewEditor:
		a.handleEditorKey(k)
		return
	case ViewDetail:
		if a.handleDetailKey(k) {
			return
		}
	case ViewCert:
		if a.handleScrollKey(k, &a.certScroll, 0) {
			return
		}
	case ViewLog:
		if a.handleScrollKey(k, &a.logScroll, 0) {
			return
		}
	case ViewHelp:
		if a.handleScrollKey(k, &a.helpScroll, 0) {
			return
		}
	}
	a.handleGlobalKey(k)
}

// handleScrollKey applies generic scrolling keys and reports whether the key
// was consumed.
func (a *App) handleScrollKey(k term.Key, offset *int, maxScroll int) bool {
	page := a.H - 6
	switch {
	case k.Is("up"), k.Is("k"):
		*offset -= 1
	case k.Is("down"), k.Is("j"):
		*offset += 1
	case k.Is("pgup"):
		*offset -= page
	case k.Is("pgdn"):
		*offset += page
	case k.Is("home"):
		*offset = 0
	case k.Is("end"):
		*offset = maxScroll
	default:
		return false
	}
	if *offset < 0 {
		*offset = 0
	}
	return true
}

func (a *App) handleGlobalKey(k term.Key) {
	switch {
	case k.Is("q") && !k.Ctrl:
		a.quit = true
	case k.Is("?"):
		a.prevView = a.view
		a.view = ViewHelp
	case k.Is("esc"):
		switch a.view {
		case ViewDetail:
			a.view = ViewFlows
		case ViewHelp, ViewFilters, ViewRules, ViewCert, ViewLog:
			a.view = ViewFlows
		}
	case k.Is("left"), k.Is("right"):
		// Arrow keys drive the menu bar, so every view is reachable without
		// leaving the home row.
		if a.view != ViewEditor && a.view != ViewDetail {
			if k.Is("left") {
				a.cycleTab(-1)
			} else {
				a.cycleTab(1)
			}
			return
		}
	case k.Rune >= '1' && k.Rune <= '6':
		idx := int(k.Rune - '1')
		if idx < len(tabNames) {
			a.view = tabNames[idx].View
			a.refresh()
		}
	}

	// On the views that show a request and a response, the pane controls win
	// over the global Tab shortcut.
	if (a.view == ViewFlows || a.view == ViewDetail) && a.handlePaneKeys(k, a.view == ViewDetail) {
		return
	}
	switch {
	case k.Is("tab"):
		a.cycleTab(1)
		return
	case k.Is("shift-tab"):
		a.cycleTab(-1)
		return
	}

	switch a.view {
	case ViewFlows:
		a.handleFlowsKey(k)
	case ViewRules:
		a.handleRulesKey(k)
	case ViewFilters:
		a.handleFiltersKey(k)
	case ViewCert:
		if k.Is("o") && !k.Ctrl {
			a.openInBrowser()
		}
		if k.Is("p") && !k.Ctrl {
			a.openPortPrompt()
		}
		if k.Is("s") && !k.Ctrl {
			a.toggleSystemProxy()
		}
		if k.Is("i") {
			a.installCert(false)
		}
		if k.Is("I") {
			a.installCert(true)
		}
		if k.Is("u") {
			a.uninstallCert()
		}
		if k.Is("t") || k.Is(" ") {
			a.certTab = (a.certTab + 1) % len(certTabs)
		}
	}
}

// paneTab returns the tab shown by one side.
func (a *App) paneTab(side int) PaneTab {
	if side == 0 {
		return a.reqTab
	}
	return a.respTab
}

func (a *App) setPaneTab(side int, t PaneTab) {
	if side == 0 {
		a.reqTab = t
		return
	}
	a.respTab = t
}

// handlePaneKeys drives the request/response panes. It reports whether the key
// was consumed.
func (a *App) handlePaneKeys(k term.Key, allowBody bool) bool {
	switch {
	case k.Is("tab"):
		a.focus = 1 - a.focus
		return true
	case k.Is("shift-tab"):
		a.setPaneTab(a.focus, a.paneTab(a.focus).next(1))
		return true
	case k.Is("h"):
		a.setPaneTab(a.focus, TabHeaders)
		return true
	case k.Is("b") && allowBody:
		a.setPaneTab(a.focus, TabBody)
		return true
	case k.Is("r"):
		a.setPaneTab(a.focus, TabRaw)
		return true
	case k.Is("["):
		a.collapseL = !a.collapseL
		if a.collapseL && a.collapseR {
			a.collapseR = false
		}
		return true
	case k.Is("]"):
		a.collapseR = !a.collapseR
		if a.collapseL && a.collapseR {
			a.collapseL = false
		}
		return true
	case k.Is("\\"):
		a.collapseL, a.collapseR = false, false
		return true
	}
	return false
}

// handleFiltersKey drives the saved filter list.
func (a *App) handleFiltersKey(k term.Key) {
	page := a.lay.filtersN
	if page < 1 {
		page = 10
	}
	switch {
	case k.Is("up"), k.Is("k"):
		a.filterSel--
	case k.Is("down"), k.Is("j"):
		a.filterSel++
	case k.Is("pgup"):
		a.filterSel -= page
	case k.Is("pgdn"):
		a.filterSel += page
	case k.Is("home"):
		a.filterSel = 0
	case k.Is("end"):
		a.filterSel = len(a.filterList) - 1
	case k.Rune == ' ':
		if a.filterSel >= 0 && a.filterSel < len(a.filterList) {
			f := a.filterList[a.filterSel]
			on := a.filterset.Toggle(f.ID)
			a.refreshFilters()
			a.status("filter "+f.Expr+" "+map[bool]string{true: "enabled", false: "disabled"}[on], 0)
		}
	case k.Is("a"):
		a.p = &prompt{
			Label: "save filter",
			Hint:  "method:GET status:>=400 | host:api.example.com",
			OnSubmit: func(v string) {
				v = strings.TrimSpace(v)
				if v == "" {
					return
				}
				a.filterset.Add(v, "")
				a.refreshFilters()
				a.refresh()
				a.status("filter saved: "+v, 1)
			},
		}
	case k.Is("e"):
		if a.filterSel >= 0 && a.filterSel < len(a.filterList) {
			f := a.filterList[a.filterSel]
			a.p = &prompt{
				Label: fmt.Sprintf("edit filter %d", f.ID),
				Value: []rune(f.Expr),
				CX:    len([]rune(f.Expr)),
				OnSubmit: func(v string) {
					v = strings.TrimSpace(v)
					if v == "" {
						return
					}
					f.Expr = v
					a.filterset.Replace(&f)
					a.refreshFilters()
					a.refresh()
					a.status("filter updated", 1)
				},
			}
		}
	case k.Is("d"):
		if a.filterSel >= 0 && a.filterSel < len(a.filterList) {
			a.filterset.Remove(a.filterList[a.filterSel].ID)
			a.refreshFilters()
			a.refresh()
			a.status("filter removed", 0)
		}
	case k.Is("c"):
		a.filterset.Clear()
		a.refreshFilters()
		a.refresh()
		a.status("all saved filters cleared", 0)
	case k.Is("p"):
		// Promote whatever is in the ad-hoc filter bar into a saved filter.
		if strings.TrimSpace(a.filterText) == "" {
			a.status("type a filter first (f), then pin it here", 2)
			return
		}
		a.filterset.Add(a.filterText, "pinned")
		a.refreshFilters()
		a.refresh()
		a.status("pinned: "+a.filterText, 1)
	}
	if a.filterSel < 0 {
		a.filterSel = 0
	}
	if a.filterSel >= len(a.filterList) {
		a.filterSel = len(a.filterList) - 1
	}
	if a.filterSel < 0 {
		a.filterSel = 0
	}
	if a.filterSel < a.filterScroll {
		a.filterScroll = a.filterSel
	}
	if a.filterSel >= a.filterScroll+page {
		a.filterScroll = a.filterSel - page + 1
	}
	if a.filterScroll < 0 {
		a.filterScroll = 0
	}
}

func (a *App) refreshFilters() {
	if a.filterset == nil {
		a.filterList = nil
		return
	}
	a.filterList = a.filterset.List()
}

func (a *App) cycleTab(dir int) {
	cur := 0
	for i, t := range tabNames {
		if t.View == a.view {
			cur = i
		}
	}
	if a.view == ViewDetail || a.view == ViewEditor {
		a.view = ViewFlows
		return
	}
	cur = (cur + dir + len(tabNames)) % len(tabNames)
	a.view = tabNames[cur].View
}

func (a *App) handleFlowsKey(k term.Key) {
	page := a.lay.tableN
	if page < 1 {
		page = 10
	}
	switch {
	case k.Is("up"), k.Is("k"):
		a.sel--
		a.follow = false
	case k.Is("down"), k.Is("j"):
		a.sel++
		if a.sel >= len(a.shown)-1 {
			a.sel = len(a.shown) - 1
			a.follow = true
		}
	case k.Is("pgup"):
		a.sel -= page
		a.follow = false
	case k.Is("pgdn"):
		a.sel += page
		a.follow = a.sel >= len(a.shown)-1
	case k.Is("home"):
		a.sel = 0
		a.follow = false
	case k.Is("end"):
		a.sel = len(a.shown) - 1
		a.follow = true
	case k.Is("enter"):
		if f := a.selectedFlow(); f != nil {
			a.openDetail(f)
		}
	case k.Is("f"), k.Is("/"):
		a.openFilter()
	case k.Is("c") && !k.Ctrl:
		a.store.Clear()
		a.status("cleared all flows", 0)
	case k.Is("b"):
		on := !a.opts.BreakRequests.Load()
		a.opts.BreakRequests.Store(on)
		a.status(fmt.Sprintf("break requests: %v", on), 0)
	case k.Is("B"):
		on := !a.opts.BreakResponses.Load()
		a.opts.BreakResponses.Store(on)
		a.status(fmt.Sprintf("break responses: %v", on), 0)
	case k.Is("x"):
		n := a.brk.Len()
		a.brk.ReleaseAll()
		a.status(fmt.Sprintf("released %d breakpoint(s)", n), 0)
	case k.Is("m"):
		a.mockSelected()
	case k.Is("M"):
		a.redirectSelected()
	case k.Is("i"):
		a.breakSelected()
	case k.Is("s"):
		a.saveSelected()
	case k.Is("p"):
		a.openPortPrompt()
	}
	if a.sel < 0 {
		a.sel = 0
	}
	if a.sel >= len(a.shown) {
		a.sel = len(a.shown) - 1
	}
	a.ensureVisible()
}

func (a *App) handleRulesKey(k term.Key) {
	page := a.lay.rulesN
	if page < 1 {
		page = 10
	}
	switch {
	case k.Is("up"), k.Is("k"):
		a.ruleSel--
	case k.Is("down"), k.Is("j"):
		a.ruleSel++
	case k.Is("pgup"):
		a.ruleSel -= page
	case k.Is("pgdn"):
		a.ruleSel += page
	case k.Is("home"):
		a.ruleSel = 0
	case k.Is("end"):
		a.ruleSel = len(a.rules) - 1
	case k.Rune == ' ':
		if a.ruleSel >= 0 && a.ruleSel < len(a.rules) {
			on := a.ruleset.Toggle(a.rules[a.ruleSel].ID)
			a.status(fmt.Sprintf("rule %d %s", a.rules[a.ruleSel].ID, map[bool]string{true: "enabled", false: "disabled"}[on]), 0)
			a.refresh()
		}
	case k.Is("a"):
		a.p = &prompt{
			Label: "new rule",
			Hint:  "* ^https://example\\.com/api :: file=/tmp/mock.json :: status=200",
			OnSubmit: func(v string) {
				r, err := core.ParseRule(v)
				if err != nil {
					a.status("rule error: "+err.Error(), 2)
					return
				}
				a.ruleset.Add(r)
				a.refresh()
				a.status("rule added", 1)
			},
		}
	case k.Is("e"):
		if a.ruleSel >= 0 && a.ruleSel < len(a.rules) {
			r := a.rules[a.ruleSel]
			a.p = &prompt{
				Label: fmt.Sprintf("edit rule %d", r.ID),
				Value: []rune(r.String()),
				CX:    len([]rune(r.String())),
				OnSubmit: func(v string) {
					parsed, err := core.ParseRule(v)
					if err != nil {
						a.status("rule error: "+err.Error(), 2)
						return
					}
					parsed.ID = r.ID
					parsed.Enabled = r.Enabled
					a.ruleset.Replace(parsed)
					a.refresh()
					a.status("rule updated", 1)
				},
			}
		}
	case k.Is("d"):
		if a.ruleSel >= 0 && a.ruleSel < len(a.rules) {
			a.ruleset.Remove(a.rules[a.ruleSel].ID)
			a.refresh()
			a.status("rule removed", 0)
		}
	case k.Is("c"):
		a.ruleset.Clear()
		a.refresh()
		a.status("all rules cleared", 0)
	}
	if a.ruleSel < 0 {
		a.ruleSel = 0
	}
	if a.ruleSel >= len(a.rules) {
		a.ruleSel = len(a.rules) - 1
	}
	if a.ruleSel < 0 {
		a.ruleSel = 0
	}
	rp := a.lay.rulesN
	if rp < 1 {
		rp = 10
	}
	if a.ruleSel < a.ruleScroll {
		a.ruleScroll = a.ruleSel
	}
	if a.ruleSel >= a.ruleScroll+rp {
		a.ruleScroll = a.ruleSel - rp + 1
	}
	if a.ruleScroll < 0 {
		a.ruleScroll = 0
	}
}

func (a *App) handleDetailKey(k term.Key) bool {
	page := a.lay.detail.H - 4
	if page < 1 {
		page = 10
	}
	scroll := &a.detailScrollL
	if a.focus == 1 {
		scroll = &a.detailScrollR
	}
	switch k.Name {
	case "up":
		*scroll--
	case "down":
		*scroll++
	case "pgup":
		*scroll -= page
	case "pgdn":
		*scroll += page
	case "home":
		*scroll = 0
	case "end":
		*scroll = 1 << 20
	default:
		return false
	}
	if *scroll < 0 {
		*scroll = 0
	}
	return true
}

// openDetail shows a flow in the full detail view.
func (a *App) openDetail(f *core.Flow) {
	a.detailID = f.ID
	a.detail = f
	a.detailScrollL, a.detailScrollR = 0, 0
	a.view = ViewDetail
}

func (a *App) handleEditorKey(k term.Key) {
	e := a.editor
	if e == nil {
		a.view = ViewFlows
		return
	}
	switch {
	case k.Ctrl && k.Is("s"), k.Is("f10"):
		a.submitEditor("")
	case k.Is("esc"):
		a.submitEditor("drop")
	case k.Is("up"):
		e.MoveUp()
	case k.Is("down"):
		e.MoveDown()
	case k.Is("left"):
		e.MoveLeft()
	case k.Is("right"):
		e.MoveRight()
	case k.Is("home"):
		e.Home()
	case k.Is("end"):
		e.End()
	case k.Is("pgup"):
		e.PageUp(a.lay.editRows)
	case k.Is("pgdn"):
		e.PageDown(a.lay.editRows)
	case k.Is("enter"):
		e.Newline()
	case k.Is("tab"):
		for i := 0; i < 2; i++ {
			e.Insert(' ')
		}
	case k.Is("backspace"):
		e.Backspace()
	case k.Is("delete"):
		e.Delete()
	case k.Rune != 0 && !k.Ctrl:
		e.Insert(k.Rune)
	}
}

func (a *App) submitEditor(mode string) {
	bp := a.editorBreak
	if bp == nil {
		a.view = ViewFlows
		return
	}
	verdict := core.Verdict{}
	if mode == "drop" {
		verdict.Drop = true
	} else if a.editor != nil {
		verdict.Raw = a.editor.Text()
	}
	a.brk.Submit(bp.ID, verdict)
	a.editorBreak = nil
	a.editor = nil
	a.status(map[bool]string{true: "breakpoint dropped", false: "breakpoint released"}[mode == "drop"], 0)
	pending := a.brk.Pending()
	if len(pending) > 0 {
		a.openEditor(pending[0])
		return
	}
	a.view = ViewFlows
}

// ---------------------------------------------------------------------------
// actions
// ---------------------------------------------------------------------------

func (a *App) openFilter() {
	a.p = &prompt{
		Label:    "filter",
		Value:    []rune(a.filterText),
		CX:       len([]rune(a.filterText)),
		IsFilter: true,
		Hint:     "method:GET status:2xx host:api /regex/",
		OnChange: func(v string) {
			a.filterText = v
			a.applyFilter()
		},
		OnSubmit: func(v string) {
			a.filterText = v
			a.applyFilter()
			if v == "" {
				a.status("filter cleared", 0)
			} else {
				a.status(fmt.Sprintf("filter: %s (%d/%d)", v, len(a.shown), len(a.flows)), 0)
			}
		},
		OnCancel: func() {
			a.filterText = ""
			a.applyFilter()
		},
	}
}

func (a *App) mockSelected() {
	f := a.selectedFlow()
	if f == nil {
		a.status("no flow selected", 2)
		return
	}
	if len(f.RespBody) == 0 {
		a.status("selected flow has no response body to mock", 2)
		return
	}
	def := filepath.Join(os.TempDir(), fmt.Sprintf("cli-proxy-mock-%d.json", f.ID))
	a.p = &prompt{
		Label: "mock file for " + f.Method + " " + f.Host + f.Path,
		Value: []rune(def),
		CX:    len([]rune(def)),
		Hint:  "the response body is written there and a file= rule is created",
		OnSubmit: func(path string) {
			path = strings.TrimSpace(path)
			if path == "" {
				a.status("mock cancelled", 2)
				return
			}
			if err := os.WriteFile(path, f.RespBody, 0o644); err != nil {
				a.status("write failed: "+err.Error(), 2)
				return
			}
			rule, err := core.ParseRule(ruleFor(f) + " :: file=" + path)
			if err != nil {
				a.status("rule error: "+err.Error(), 2)
				return
			}
			rule.Note = "mock from flow " + fmt.Sprint(f.ID)
			a.ruleset.Add(rule)
			a.refresh()
			a.status("mock rule created -> "+path, 1)
		},
	}
}

func (a *App) redirectSelected() {
	f := a.selectedFlow()
	if f == nil {
		a.status("no flow selected", 2)
		return
	}
	a.p = &prompt{
		Label: "fetch " + f.Method + " " + f.Host + f.Path + " from",
		Hint:  "https://staging.example.com" + f.Path,
		OnSubmit: func(target string) {
			target = strings.TrimSpace(target)
			if target == "" {
				return
			}
			rule, err := core.ParseRule(ruleFor(f) + " :: redirect=" + target)
			if err != nil {
				a.status("rule error: "+err.Error(), 2)
				return
			}
			rule.Note = "redirect from flow " + fmt.Sprint(f.ID)
			a.ruleset.Add(rule)
			a.refresh()
			a.status("redirect rule created -> "+target, 1)
		},
	}
}

func (a *App) breakSelected() {
	f := a.selectedFlow()
	if f == nil {
		a.status("no flow selected", 2)
		return
	}
	rule, err := core.ParseRule(ruleFor(f) + " :: break=both")
	if err != nil {
		a.status("rule error: "+err.Error(), 2)
		return
	}
	rule.Note = "intercept from flow " + fmt.Sprint(f.ID)
	a.ruleset.Add(rule)
	a.view = ViewRules
	a.refresh()
	a.status("intercept rule created for "+f.URL, 1)
}

func (a *App) saveSelected() {
	f := a.selectedFlow()
	if f == nil {
		a.status("no flow selected", 2)
		return
	}
	def := filepath.Join(os.TempDir(), fmt.Sprintf("cli-proxy-flow-%d.txt", f.ID))
	a.p = &prompt{
		Label: "save flow to",
		Value: []rune(def),
		CX:    len([]rune(def)),
		OnSubmit: func(path string) {
			path = strings.TrimSpace(path)
			if path == "" {
				return
			}
			var b strings.Builder
			fmt.Fprintf(&b, "=== REQUEST ===\n%s\n\n=== RESPONSE ===\n%s\n",
				f.RawRequest(), f.RawResponse())
			if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
				a.status("write failed: "+err.Error(), 2)
				return
			}
			a.status("saved -> "+path, 1)
		},
	}
}

// openPortPrompt asks for a new listen port and applies it without restarting
// the proxy: captured flows, rules and the certificate authority all survive.
func (a *App) openPortPrompt() {
	current := strconv.Itoa(a.proxy.Port())
	a.p = &prompt{
		Label: "listen port",
		Value: []rune(current),
		CX:    len(current),
		Hint:  "1-65535, or host:port — applies at once, captured flows are kept",
		OnSubmit: func(v string) {
			a.changePort(v)
		},
	}
}

func (a *App) changePort(v string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return
	}
	target := v
	if !strings.Contains(v, ":") {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			a.status("invalid port: "+v+" (expected 1-65535)", 2)
			return
		}
		host, _, err := net.SplitHostPort(a.proxy.Addr())
		if err != nil || host == "" {
			host = "0.0.0.0"
		}
		target = net.JoinHostPort(host, strconv.Itoa(n))
	}
	if target == a.proxy.Addr() {
		a.status("already listening on "+target, 0)
		return
	}
	if err := a.proxy.Rebind(target); err != nil {
		a.status("cannot change port: "+err.Error(), 2)
		return
	}
	if a.sysProxy != nil && a.sysProxy.Active() {
		if err := a.sysProxy.EnableAddr(a.proxy.Addr()); err != nil {
			a.status("listening on "+a.proxy.Addr()+", but the system proxy: "+err.Error(), 2)
			return
		}
	}
	a.status("listening on "+a.proxy.Addr(), 1)
}

// toggleSystemProxy turns the operating system proxy on or off. Whatever was
// configured before is restored when cli-proxy exits.
func (a *App) toggleSystemProxy() {
	if a.sysProxy == nil || !a.sysProxy.Supported() {
		a.status("system proxy control is not available on this platform", 2)
		return
	}
	if a.sysProxy.Active() {
		if err := a.sysProxy.Disable(); err != nil {
			a.status("system proxy: "+err.Error(), 2)
			return
		}
		a.status("system proxy restored", 1)
		return
	}
	if err := a.sysProxy.EnableAddr(a.proxy.Addr()); err != nil {
		a.status("system proxy: "+err.Error(), 2)
		return
	}
	a.status("system proxy -> "+a.sysProxy.Target()+" (restored when cli-proxy exits)", 1)
}

// refreshTrust re-reads the operating system's trust state.
func (a *App) refreshTrust() {
	a.trustChecked = time.Now()
	if a.trust == nil || !a.trust.Available() {
		a.trustStatus = trust.Status{Detail: "not available on this platform"}
		return
	}
	st, err := a.trust.Check()
	if err != nil {
		a.trustStatus = trust.Status{Detail: err.Error()}
		return
	}
	a.trustStatus = st
}

// installCert puts the root certificate into the trust store so HTTPS
// interception works without extra flags.
//
// It runs in the background: macOS may raise an authorisation dialog, and the
// interface must stay responsive while the operator answers it.
func (a *App) installCert(system bool) {
	if !a.certReady() {
		return
	}
	if a.certPending {
		a.status("a certificate operation is already running", 0)
		return
	}
	target := "this user"
	if system {
		target = "every user"
	}
	a.status("installing the root certificate for "+target+" — approve the system prompt if one appears", 0)
	a.runCert("install", func() (string, error) { return a.trust.Install(system) })
}

func (a *App) uninstallCert() {
	if !a.certReady() {
		return
	}
	if a.certPending {
		a.status("a certificate operation is already running", 0)
		return
	}
	a.status("removing the root certificate…", 0)
	a.runCert("uninstall", func() (string, error) {
		return "", a.trust.Uninstall(false)
	})
}

func (a *App) certReady() bool {
	if a.trust == nil || !a.trust.Available() {
		a.status("certificate installation is not available on this platform", 2)
		return false
	}
	return true
}

func (a *App) runCert(action string, work func() (string, error)) {
	a.certPending = true
	run := func() certResult {
		where, err := work()
		return certResult{action: action, where: where, err: err}
	}
	if a.certCh == nil {
		a.finishCert(run())
		return
	}
	go func() { a.certCh <- run() }()
}

func (a *App) finishCert(res certResult) {
	a.certPending = false
	switch {
	case res.err != nil:
		a.status("certificate "+res.action+" failed: "+res.err.Error(), 2)
	case res.action == "uninstall":
		a.status("root certificate removed from the trust store", 1)
	default:
		a.status("root certificate installed — "+res.where, 1)
	}
	a.refreshTrust()
}

func ruleFor(f *core.Flow) string {
	return f.Method + " ^" + regexp.QuoteMeta(f.URL) + "$"
}

func (a *App) openInBrowser() {
	url := a.proxy.BaseURL() + "/"
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		a.status("cannot open browser: "+err.Error(), 2)
		return
	}
	a.status("opened "+url, 1)
}

// ---------------------------------------------------------------------------
// mouse
// ---------------------------------------------------------------------------

func (a *App) handleMouse(m term.Mouse) {
	if m.Wheel != 0 {
		a.handleWheel(m)
		return
	}
	if !m.Press && !m.Release {
		a.updateHover(m.X, m.Y)
		return
	}
	if m.Release {
		return
	}
	a.updateHover(m.X, m.Y)

	// Buttons in the bottom border.
	for i := range a.lay.buttons {
		btn := a.lay.buttons[i]
		if m.X >= btn.X && m.X < btn.X+btn.W && m.Y == btn.row {
			if btn.Action != nil {
				btn.Action()
			}
			return
		}
	}
	// Menu bar.
	for i, r := range a.lay.tabs {
		if r.contains(m.X, m.Y) {
			a.view = tabNames[i].View
			a.refresh()
			return
		}
	}
	// Pane tabs inside a message pane.
	if side, index, ok := a.paneTabAt(m.X, m.Y); ok {
		a.focus = side
		a.setPaneTab(side, PaneTab(index))
		return
	}
	// Certificate instruction tabs.
	for i, r := range a.lay.certTabs {
		if r.contains(m.X, m.Y) {
			a.certTab = i
			return
		}
	}

	switch a.view {
	case ViewFlows:
		if a.lay.table.contains(m.X, m.Y) && m.Y >= a.lay.tableY && m.Y < a.lay.tableY+a.lay.tableN {
			idx := a.scroll + (m.Y - a.lay.tableY)
			if idx >= 0 && idx < len(a.shown) {
				dbl := time.Since(a.lastClickAt) < 400*time.Millisecond && a.lastClickY == m.Y
				a.sel = idx
				a.follow = idx >= len(a.shown)-1
				a.lastClickAt = time.Now()
				a.lastClickY = m.Y
				if dbl {
					a.openDetail(a.shown[idx])
				}
			}
		}
	case ViewFilters:
		if a.lay.filters.contains(m.X, m.Y) && m.Y >= a.lay.filtersY && m.Y < a.lay.filtersY+a.lay.filtersN {
			idx := a.filterScroll + (m.Y - a.lay.filtersY)
			if idx >= 0 && idx < len(a.filterList) {
				if a.filterSel == idx {
					a.filterset.Toggle(a.filterList[idx].ID)
					a.refreshFilters()
					a.refresh()
				} else {
					a.filterSel = idx
				}
			}
		}
	case ViewRules:
		if a.lay.rules.contains(m.X, m.Y) && m.Y >= a.lay.rulesY && m.Y < a.lay.rulesY+a.lay.rulesN {
			idx := a.ruleScroll + (m.Y - a.lay.rulesY)
			if idx >= 0 && idx < len(a.rules) {
				if a.ruleSel == idx {
					a.ruleset.Toggle(a.rules[idx].ID)
					a.refresh()
				} else {
					a.ruleSel = idx
				}
			}
		}
	case ViewEditor:
		if a.lay.editor.contains(m.X, m.Y) && a.editor != nil {
			relX := m.X - a.lay.editor.X - 1 - a.editGutter()
			relY := m.Y - a.lay.editor.Y - 1
			if relY >= 0 && relY < a.lay.editRows {
				a.editor.SetCursorFromClick(relX, relY, a.lay.editCols, a.lay.editRows)
			}
		}
	}
}

// paneTabAt reports which pane tab, if any, sits under a point.
func (a *App) paneTabAt(x, y int) (side, index int, ok bool) {
	for i, r := range a.lay.paneTabsL {
		if r.H > 0 && r.contains(x, y) {
			return 0, i, true
		}
	}
	for i, r := range a.lay.paneTabsR {
		if r.H > 0 && r.contains(x, y) {
			return 1, i, true
		}
	}
	return 0, 0, false
}

func (a *App) editGutter() int { return 5 }

func (a *App) updateHover(x, y int) {
	a.hoverTab, a.hoverRow, a.hoverBtn = -1, -1, -1
	a.hoverRule, a.hoverFilter, a.hoverCertTab = -1, -1, -1
	a.hoverPaneTab, a.hoverPaneTabIndex = -1, -1

	for i, r := range a.lay.tabs {
		if r.contains(x, y) {
			a.hoverTab = i
		}
	}
	if len(a.lay.buttons) > 0 {
		for i := range a.lay.buttons {
			b := a.lay.buttons[i]
			if y == b.row && x >= b.X && x < b.X+b.W {
				a.hoverBtn = i
			}
		}
	}
	for i, r := range a.lay.certTabs {
		if r.contains(x, y) {
			a.hoverCertTab = i
		}
	}
	if side, index, ok := a.paneTabAt(x, y); ok {
		a.hoverPaneTab, a.hoverPaneTabIndex = side, index
	}

	switch a.view {
	case ViewFlows:
		if y >= a.lay.tableY && y < a.lay.tableY+a.lay.tableN && a.lay.table.contains(x, y) {
			idx := a.scroll + (y - a.lay.tableY)
			if idx >= 0 && idx < len(a.shown) {
				a.hoverRow = idx
			}
		}
	case ViewFilters:
		if y >= a.lay.filtersY && y < a.lay.filtersY+a.lay.filtersN && a.lay.filters.contains(x, y) {
			idx := a.filterScroll + (y - a.lay.filtersY)
			if idx >= 0 && idx < len(a.filterList) {
				a.hoverFilter = idx
			}
		}
	case ViewRules:
		if y >= a.lay.rulesY && y < a.lay.rulesY+a.lay.rulesN && a.lay.rules.contains(x, y) {
			idx := a.ruleScroll + (y - a.lay.rulesY)
			if idx >= 0 && idx < len(a.rules) {
				a.hoverRule = idx
			}
		}
	}
}

func (a *App) handleWheel(m term.Mouse) {
	step := 3 * m.Wheel
	switch a.view {
	case ViewFilters:
		a.filterSel += step
		if a.filterSel < 0 {
			a.filterSel = 0
		}
		if a.filterSel >= len(a.filterList) {
			a.filterSel = len(a.filterList) - 1
		}
		if a.filterSel < 0 {
			a.filterSel = 0
		}
		if a.filterSel < a.filterScroll {
			a.filterScroll = a.filterSel
		}
		if p := a.lay.filtersN; p > 0 && a.filterSel >= a.filterScroll+p {
			a.filterScroll = a.filterSel - p + 1
		}
	case ViewFlows:
		a.sel += step
		if a.sel < 0 {
			a.sel = 0
		}
		if a.sel >= len(a.shown) {
			a.sel = len(a.shown) - 1
			a.follow = true
		} else {
			a.follow = a.sel >= len(a.shown)-1
		}
		a.ensureVisible()
	case ViewRules:
		a.ruleSel += step
		if a.ruleSel < 0 {
			a.ruleSel = 0
		}
		if a.ruleSel >= len(a.rules) {
			a.ruleSel = len(a.rules) - 1
		}
		if a.ruleSel < 0 {
			a.ruleSel = 0
		}
		if a.ruleSel < a.ruleScroll {
			a.ruleScroll = a.ruleSel
		}
		if rp := a.lay.rulesN; rp > 0 && a.ruleSel >= a.ruleScroll+rp {
			a.ruleScroll = a.ruleSel - rp + 1
		}
	case ViewDetail:
		scroll := &a.detailScrollL
		if a.focus == 1 {
			scroll = &a.detailScrollR
		}
		*scroll += step
		if *scroll < 0 {
			*scroll = 0
		}
	case ViewEditor:
		if a.editor != nil {
			if m.Wheel < 0 {
				a.editor.PageUp(3)
			} else {
				a.editor.PageDown(3)
			}
		}
	case ViewCert:
		a.certScroll += step
		if a.certScroll < 0 {
			a.certScroll = 0
		}
	case ViewLog:
		a.logScroll += step
		if a.logScroll < 0 {
			a.logScroll = 0
		}
	case ViewHelp:
		a.helpScroll += step
		if a.helpScroll < 0 {
			a.helpScroll = 0
		}
	}
}
