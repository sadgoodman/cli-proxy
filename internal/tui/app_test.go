package tui

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cliproxy/internal/ca"
	"cliproxy/internal/core"
	"cliproxy/internal/proxy"
	"cliproxy/internal/term"
	"cliproxy/internal/trust"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	return newTestAppAt(t, "127.0.0.1:8080")
}

func newTestAppAt(t *testing.T, addr string) *App {
	t.Helper()
	authority, err := ca.Load(t.TempDir())
	if err != nil {
		t.Fatalf("ca.Load: %v", err)
	}
	store := core.NewStore(100)
	ruleset := core.NewRuleSet("")
	brk := core.NewBreaker()
	opts := core.DefaultOptions()
	elog := core.NewLogger(100)

	px := proxy.New(proxy.Config{
		Addr:    addr,
		CA:      authority,
		Store:   store,
		Rules:   ruleset,
		Breaker: brk,
		Opts:    opts,
		Log:     elog,
		Version: "test",
		CAPath:  "/tmp/ca",
	})
	// Deliberately not started: rendering must not depend on a live listener.

	a := &App{
		cfg:      Config{Proxy: px, Store: store, Rules: ruleset, Breaker: brk, Opts: opts, Log: elog},
		proxy:    px,
		store:    store,
		ruleset:  ruleset,
		brk:      brk,
		opts:     opts,
		elog:     elog,
		theme:    DefaultTheme(),
		hoverTab: -1,
		hoverRow: -1,
		hoverBtn: -1,
		started:  time.Now(),
		follow:   true,
	}
	a.W, a.H = 120, 36
	a.screen = NewScreen(a.W, a.H)
	a.prev = NewScreen(a.W, a.H)
	return a
}

// press routes a key through the real dispatch path, including prompts.
func press(a *App, k term.Key) {
	a.handle(term.Event{Kind: term.EvKey, Key: k})
}

func seedFlows(t *testing.T, a *App) {
	t.Helper()
	a.store.Add(&core.Flow{
		Method: "GET", URL: "https://api.example.com/v1/users", Host: "api.example.com",
		Path: "/v1/users?limit=10", Scheme: "https", Proto: "HTTP/1.1", Status: 200, Reason: "OK",
		Start: time.Now(), Duration: 42 * time.Millisecond, BytesOut: 512,
		ReqHeaders:  []core.Header{{Name: "Host", Value: "api.example.com"}, {Name: "Accept", Value: "application/json"}},
		RespHeaders: []core.Header{{Name: "Content-Type", Value: "application/json"}},
		RespBody:    []byte(`{"users":[{"id":1}]}`),
		State:       core.StateComplete,
	})
	a.store.Add(&core.Flow{
		Method: "POST", URL: "http://cdn.example.net/upload", Host: "cdn.example.net",
		Path: "/upload", Scheme: "http", Status: 500, Reason: "Server Error",
		Start: time.Now(), Duration: 3 * time.Second, BytesIn: 2048,
		ReqHeaders: []core.Header{{Name: "Host", Value: "cdn.example.net"}},
		ReqBody:    []byte(`{"payload":"data"}`),
		Tags:       []string{"mock"},
		State:      core.StateComplete,
	})
	a.store.Add(&core.Flow{
		Method: "GET", URL: "https://pinned.example.com/api", Host: "pinned.example.com",
		Path: "/api", Scheme: "https", State: core.StateError, Err: "tls: bad certificate",
		Start: time.Now(), Tags: []string{"pinned?"},
	})
	a.refresh()
}

func TestRenderAllViews(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)

	views := []struct {
		name string
		view ViewKind
		want []string
	}{
		{"flows", ViewFlows, []string{"╭", "╮", "╰", "╯", "flows", "api.example.com", "127.0.0.1:8080", "GET", "POST"}},
		{"filters", ViewFilters, []string{"saved filters", "Expression"}},
		{"rules", ViewRules, []string{"rules", "URL pattern", "Actions"}},
		{"cert", ViewCert, []string{"certificates", "SHA-256", "authority", "status"}},
		{"log", ViewLog, []string{"engine log"}},
		{"help", ViewHelp, []string{"keyboard & mouse reference", "hover", "Flows", "Filter syntax"}},
	}
	for _, tc := range views {
		a.view = tc.view
		a.render()
		frame := a.screen.Render(nil, true)
		for _, want := range tc.want {
			if !strings.Contains(frame, want) {
				t.Errorf("view %s is missing %q", tc.name, want)
			}
		}
		if !strings.Contains(frame, "╭") || !strings.Contains(frame, "╯") {
			t.Errorf("view %s is not framed with rounded corners", tc.name)
		}
	}
}

func TestHelpViewScrollsToTheRuleDSL(t *testing.T) {
	a := newTestApp(t)
	a.view = ViewHelp
	a.render()
	if strings.Contains(a.screen.Render(nil, true), "Rule DSL") {
		t.Fatal("the rule DSL section should need scrolling to reach")
	}
	a.helpScroll = 1 << 20
	a.render()
	if !strings.Contains(a.screen.Render(nil, true), "Rule DSL") {
		t.Fatal("scrolling to the end did not reveal the rule DSL section")
	}
}

func TestRenderDetailAndEditor(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)

	f := a.store.Snapshot()[0]
	a.openDetail(f)
	a.render()
	frame := a.screen.Render(nil, true)
	for _, want := range []string{"request ·", "response ·", "headers", "body", "raw"} {
		if !strings.Contains(frame, want) {
			t.Errorf("detail view missing %q", want)
		}
	}
	// The body lives behind its own tab now.
	a.reqTab, a.respTab = TabBody, TabBody
	a.render()
	frame = a.screen.Render(nil, true)
	if !strings.Contains(frame, `"users"`) {
		t.Errorf("body tab does not show the payload:\n%s", frame)
	}

	bp := &core.Breakpoint{ID: 7, Phase: core.PhaseRequest, Flow: f, Raw: f.RawRequest()}
	a.openEditor(bp)
	a.render()
	frame = a.screen.Render(nil, true)
	for _, want := range []string{"breakpoint #7", "REQUEST", "GET /v1/users?limit=10 HTTP/1.1"} {
		if !strings.Contains(frame, want) {
			t.Errorf("editor view missing %q", want)
		}
	}
	if !a.cursorVisible {
		t.Error("editor should place a visible cursor")
	}
}

func TestNavigationKeys(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)

	if len(a.shown) != 3 {
		t.Fatalf("shown = %d", len(a.shown))
	}
	a.sel = 0
	a.follow = false
	press(a, term.Key{Name: "down"})
	if a.sel != 1 {
		t.Fatalf("down did not move the selection: %d", a.sel)
	}
	press(a, term.Key{Name: "end"})
	if a.sel != len(a.shown)-1 || !a.follow {
		t.Fatalf("end did not jump to the tail: %d", a.sel)
	}
	press(a, term.Key{Name: "home"})
	if a.sel != 0 {
		t.Fatalf("home did not jump to the head: %d", a.sel)
	}

	// Enter opens the detail view.
	press(a, term.Key{Name: "enter"})
	if a.view != ViewDetail {
		t.Fatalf("enter did not open detail: %v", a.view)
	}
	press(a, term.Key{Name: "esc"})
	if a.view != ViewFlows {
		t.Fatalf("esc did not return to flows: %v", a.view)
	}

	// 'f' opens the live filter prompt.
	press(a, term.Key{Rune: 'f'})
	if a.p == nil || !a.p.IsFilter {
		t.Fatal("f did not open the filter prompt")
	}
	for _, r := range "cdn" {
		press(a, term.Key{Rune: r})
	}
	if len(a.shown) != 1 || !strings.Contains(a.shown[0].URL, "cdn.example.net") {
		t.Fatalf("live filter did not apply: %d flows", len(a.shown))
	}
	press(a, term.Key{Name: "enter"})
	if a.p != nil {
		t.Fatal("enter did not close the prompt")
	}

	// '?' opens help, tab cycles views.
	press(a, term.Key{Rune: '?'})
	if a.view != ViewHelp {
		t.Fatalf("? did not open help: %v", a.view)
	}
	press(a, term.Key{Name: "tab"})
	if a.view == ViewHelp {
		t.Fatal("tab did not leave the help view")
	}
}

func TestFlowActions(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)

	// b / B toggle the global breakpoint switches.
	if a.opts.BreakRequests.Load() {
		t.Fatal("break requests should start disabled")
	}
	press(a, term.Key{Rune: 'b'})
	if !a.opts.BreakRequests.Load() {
		t.Fatal("b did not enable request breakpoints")
	}
	press(a, term.Key{Rune: 'B'})
	if !a.opts.BreakResponses.Load() {
		t.Fatal("B did not enable response breakpoints")
	}
	press(a, term.Key{Rune: 'b'})
	press(a, term.Key{Rune: 'B'})
	if a.opts.BreakRequests.Load() || a.opts.BreakResponses.Load() {
		t.Fatal("toggles did not switch back off")
	}

	// i builds an intercept rule from the selected flow.
	a.sel = 0
	a.follow = false
	press(a, term.Key{Rune: 'i'})
	if a.ruleset.Len() != 1 {
		t.Fatalf("intercept rule not created: %d", a.ruleset.Len())
	}
	rule := a.ruleset.List()[0]
	if !rule.Has(core.ActBreak) {
		t.Fatalf("unexpected rule: %s", rule.String())
	}
	if !rule.Match("GET", a.shown[0].URL) {
		t.Fatalf("generated rule does not match the flow: %s", rule.String())
	}

	// c clears the capture (the intercept action jumped to the rules view).
	a.view = ViewFlows
	press(a, term.Key{Rune: 'c'})
	if a.store.Len() != 0 {
		t.Fatal("c did not clear the flows")
	}
}

func TestMouseSelectionHoverAndTabs(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)
	a.view = ViewFlows
	a.render()

	if len(a.lay.tabs) != len(tabNames) {
		t.Fatalf("tab hit areas = %d", len(a.lay.tabs))
	}
	// Hovering a tab highlights it.
	filtersTab := a.lay.tabs[1]
	a.handleMouse(term.Mouse{X: filtersTab.X + 1, Y: filtersTab.Y, Motion: true})
	if a.hoverTab != 1 {
		t.Fatalf("hoverTab = %d, want 1", a.hoverTab)
	}
	// Clicking the tab switches view.
	a.handleMouse(term.Mouse{X: filtersTab.X + 1, Y: filtersTab.Y, Press: true, Button: 0})
	if a.view != ViewFilters {
		t.Fatalf("click did not switch to the filters view: %v", a.view)
	}

	a.view = ViewFlows
	a.render()
	// The table body rows are hit-testable.
	rowY := a.lay.tableY + 1
	a.handleMouse(term.Mouse{X: 4, Y: rowY, Motion: true})
	if a.hoverRow != 1 {
		t.Fatalf("hoverRow = %d, want 1", a.hoverRow)
	}
	a.handleMouse(term.Mouse{X: 4, Y: rowY, Press: true, Button: 0})
	if a.sel != 1 {
		t.Fatalf("click did not select row 1: sel=%d", a.sel)
	}
	// Double click opens the detail view.
	a.handleMouse(term.Mouse{X: 4, Y: rowY, Press: true, Button: 0})
	if a.view != ViewDetail {
		t.Fatalf("double click did not open detail: %v", a.view)
	}
}

func TestMouseButtonsAreClickable(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)
	a.view = ViewFlows
	a.render()

	var helpBtn *button
	for i := range a.lay.buttons {
		if a.lay.buttons[i].Label == "help" {
			helpBtn = &a.lay.buttons[i]
		}
	}
	if helpBtn == nil {
		t.Fatalf("help button not laid out: %+v", a.lay.buttons)
	}
	a.handleMouse(term.Mouse{X: helpBtn.X + 1, Y: helpBtn.row, Motion: true})
	if a.hoverBtn < 0 || a.lay.buttons[a.hoverBtn].Label != "help" {
		t.Fatalf("button hover not detected: hoverBtn=%d", a.hoverBtn)
	}
	a.handleMouse(term.Mouse{X: helpBtn.X + 1, Y: helpBtn.row, Press: true, Button: 0})
	if a.view != ViewHelp {
		t.Fatalf("clicking the help button did not open help: %v", a.view)
	}
}

func TestWheelScrollsTheFlowList(t *testing.T) {
	a := newTestApp(t)
	for i := 0; i < 60; i++ {
		a.store.Add(&core.Flow{
			Method: "GET", URL: "http://example.com/" + strings.Repeat("x", i%5),
			Host: "example.com", Path: "/", Status: 200, State: core.StateComplete,
		})
	}
	a.refresh()
	a.sel = 0
	a.follow = false
	a.view = ViewFlows
	a.render()
	a.handleMouse(term.Mouse{Wheel: 1})
	if a.sel == 0 {
		t.Fatalf("wheel down did not move the selection: %d", a.sel)
	}
	a.handleMouse(term.Mouse{Wheel: -1})
	a.handleMouse(term.Mouse{Wheel: -1})
	if a.sel < 0 {
		t.Fatalf("selection went negative: %d", a.sel)
	}
}

func TestEditorSubmitReleasesBreakpoint(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)
	f := a.store.Snapshot()[0]

	result := make(chan core.Verdict, 1)
	bp := &core.Breakpoint{ID: 1, Phase: core.PhaseRequest, Flow: f, Raw: f.RawRequest()}
	go func() { result <- a.brk.Hold(bp) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(a.brk.Pending()) == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	a.openEditor(a.brk.Pending()[0])
	a.editor.End()
	a.editor.Insert('!')
	press(a, term.Key{Ctrl: true, Name: "s"})

	select {
	case v := <-result:
		if !strings.Contains(v.Raw, "HTTP/1.1!") {
			t.Fatalf("edited text not submitted: %q", v.Raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("breakpoint was never released")
	}
	if a.view != ViewFlows {
		t.Fatalf("editor did not close: %v", a.view)
	}
}

func TestArrowKeysDriveTheMenu(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)
	a.view = ViewFlows
	a.render()

	press(a, term.Key{Name: "right"})
	if a.view != ViewFilters {
		t.Fatalf("right arrow did not select the next menu item: %v", a.view)
	}
	press(a, term.Key{Name: "right"})
	if a.view != ViewRules {
		t.Fatalf("right arrow did not advance again: %v", a.view)
	}
	press(a, term.Key{Name: "left"})
	if a.view != ViewFilters {
		t.Fatalf("left arrow did not go back: %v", a.view)
	}
}

// The objective explicitly requires hover highlighting, so assert that the
// rendered cells actually change style when the pointer is over an element.
func TestHoverHighlightIsRendered(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)
	a.view = ViewFlows
	// Keep the selection away from the row we hover so the two highlights
	// cannot mask each other.
	a.sel = 0
	a.follow = false
	a.render()

	rowY := a.lay.tableY + 2
	cell := func() Style { return a.screen.cells[rowY*a.W+12].st }

	a.hoverRow = -1
	a.render()
	plain := cell()

	a.hoverRow = 2
	a.render()
	hovered := cell()
	if hovered == plain {
		t.Fatal("hovering a table row did not change its rendering")
	}
	if hovered.Bg == ColDefault {
		t.Fatalf("hovered row should be filled with a background colour, got %+v", hovered)
	}

	// Tabs
	tabX := a.lay.tabs[1].X + 1
	styleAt := func() Style { return a.screen.cells[1*a.W+tabX].st }
	a.hoverTab = -1
	a.render()
	plainTab := styleAt()
	a.hoverTab = 1
	a.render()
	hoveredTab := styleAt()
	if hoveredTab == plainTab {
		t.Fatal("hovering a menu item did not change its rendering")
	}
	if hoveredTab.Bg == ColDefault {
		t.Fatalf("hovered menu item should be filled, got %+v", hoveredTab)
	}

	// Buttons in the bottom bar
	var btn *button
	btnIndex := -1
	for i := range a.lay.buttons {
		if a.lay.buttons[i].Label == "mock" {
			btn, btnIndex = &a.lay.buttons[i], i
		}
	}
	if btn == nil {
		t.Fatalf("mock button not laid out: %+v", a.lay.buttons)
	}
	btnStyle := func() Style { return a.screen.cells[btn.row*a.W+btn.X].st }
	a.hoverBtn = -1
	a.render()
	plainBtn := btnStyle()
	a.hoverBtn = btnIndex
	a.render()
	hoveredBtn := btnStyle()
	if hoveredBtn == plainBtn {
		t.Fatal("hovering a button did not change its rendering")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestChangePortFromTheUI(t *testing.T) {
	a := newTestAppAt(t, "127.0.0.1:0")
	if err := a.proxy.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.proxy.Close()
	seedFlows(t, a)
	a.view = ViewFlows

	target := freeAddr(t)
	wantPort := target[strings.LastIndex(target, ":")+1:]

	press(a, term.Key{Rune: 'p'})
	if a.p == nil {
		t.Fatal("p did not open the port prompt")
	}
	if got, want := a.p.text(), strconv.Itoa(a.proxy.Port()); got != want {
		t.Fatalf("prompt prefilled with %q, want the current port %q", got, want)
	}
	a.p.Value = []rune(wantPort)
	a.p.CX = len(a.p.Value)
	press(a, term.Key{Name: "enter"})

	if got := a.proxy.Addr(); got != target {
		t.Fatalf("Addr() = %q, want %q", got, target)
	}
	if a.store.Len() != 3 {
		t.Fatalf("captured flows were lost across the port change: %d", a.store.Len())
	}
	if a.statusKind != 1 || !strings.Contains(a.statusText, target) {
		t.Fatalf("status = %q (kind %d), want a success message naming %s", a.statusText, a.statusKind, target)
	}
	if !strings.Contains(a.proxy.BaseURL(), wantPort) {
		t.Fatalf("certificate URL was not updated: %s", a.proxy.BaseURL())
	}
}

func TestChangePortRejectsBadInput(t *testing.T) {
	a := newTestAppAt(t, "127.0.0.1:0")
	if err := a.proxy.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.proxy.Close()
	a.view = ViewFlows
	before := a.proxy.Addr()

	for _, bad := range []string{"70000", "0", "-5", "http"} {
		press(a, term.Key{Rune: 'p'})
		a.p.Value = []rune(bad)
		a.p.CX = len(a.p.Value)
		press(a, term.Key{Name: "enter"})
		if a.proxy.Addr() != before {
			t.Fatalf("input %q moved the listener to %s", bad, a.proxy.Addr())
		}
		if a.statusKind != 2 {
			t.Fatalf("input %q should report an error, got %q", bad, a.statusText)
		}
	}
}

func TestChangePortToOccupiedPortKeepsOldListener(t *testing.T) {
	a := newTestAppAt(t, "127.0.0.1:0")
	if err := a.proxy.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.proxy.Close()
	a.view = ViewFlows
	before := a.proxy.Addr()

	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	busy := blocker.Addr().String()

	press(a, term.Key{Rune: 'p'})
	a.p.Value = []rune(busy)
	a.p.CX = len(a.p.Value)
	press(a, term.Key{Name: "enter"})

	if a.proxy.Addr() != before {
		t.Fatalf("a failed rebind must not move the listener: %s -> %s", before, a.proxy.Addr())
	}
	if a.statusKind != 2 {
		t.Fatalf("expected an error status, got %q", a.statusText)
	}
}

func TestPortButtonIsAlwaysReachable(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)
	a.view = ViewFlows

	// The port button matters most on small terminals, so it must survive the
	// button-bar trimming at every realistic width, and help/quit are pinned.
	for _, w := range []int{160, 140, 120, 100, 90, 80} {
		a.W, a.H = w, 30
		a.screen.Resize(w, 30)
		a.prev.Resize(w, 30)
		a.render()
		var btn *button
		for i := range a.lay.buttons {
			if a.lay.buttons[i].Label == "port" {
				btn = &a.lay.buttons[i]
			}
		}
		if btn == nil {
			t.Fatalf("port button missing at %d columns: %+v", w, a.lay.buttons)
		}
		for _, want := range []string{"help", "quit"} {
			found := false
			for i := range a.lay.buttons {
				if a.lay.buttons[i].Label == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("%q button missing at %d columns: %+v", want, w, a.lay.buttons)
			}
		}
		a.p = nil
		a.handleMouse(term.Mouse{X: btn.X + 1, Y: btn.row, Press: true, Button: 0})
		if a.p == nil {
			t.Fatalf("clicking the port button at %d columns did not open the prompt", w)
		}
	}
}

// Regression: a real "motion, no button" report (SGR code 35) used to be
// decoded as a button release, so hovering silently did nothing.
func TestHoverWorksThroughTheRealInputPath(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)
	a.sel = 0
	a.follow = false
	a.view = ViewFlows
	a.render()

	rowY := a.lay.tableY // 0-based row of the first data row
	if rowY <= 0 {
		t.Fatalf("table body was not laid out: %+v", a.lay.table)
	}
	seq := fmt.Sprintf("\x1b[<35;%d;%dM", 6, rowY+1) // SGR coordinates are 1-based
	r := term.NewReader(strings.NewReader(seq))
	defer r.Close()

	select {
	case ev := <-r.Events():
		if ev.Kind != term.EvMouse || !ev.Mouse.Motion || ev.Mouse.Release {
			t.Fatalf("hover decoded as %+v", ev)
		}
		a.handle(ev)
	case <-time.After(2 * time.Second):
		t.Fatal("no mouse event was decoded")
	}
	if a.hoverRow != 0 {
		t.Fatalf("hoverRow = %d, want 0 after a real hover report", a.hoverRow)
	}
}

func TestFilterPromptKeepsTheListNavigable(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)
	a.view = ViewFlows
	a.render()

	press(a, term.Key{Rune: 'f'})
	if a.p == nil || !a.p.IsFilter {
		t.Fatal("f did not open the filter prompt")
	}
	a.sel = 0
	a.follow = false

	press(a, term.Key{Name: "down"})
	if a.sel != 1 {
		t.Fatalf("down arrow did not move the list while filtering: sel=%d", a.sel)
	}
	press(a, term.Key{Name: "pgdn"})
	if a.sel != len(a.shown)-1 {
		t.Fatalf("pgdn did not reach the tail while filtering: sel=%d", a.sel)
	}
	press(a, term.Key{Name: "pgup"})
	if a.sel != 0 {
		t.Fatalf("pgup did not return to the head while filtering: sel=%d", a.sel)
	}
	if a.p.text() != "" {
		t.Fatalf("navigation keys leaked into the filter text: %q", a.p.text())
	}
	// Home/End belong to the single-line prompt, not to the list.
	press(a, term.Key{Name: "end"})
	if a.p.CX != len(a.p.Value) {
		t.Fatalf("end did not move the caret to the end of the filter text")
	}

	// Clicking a row still selects it while the prompt is open.
	a.render()
	a.handleMouse(term.Mouse{X: 5, Y: a.lay.tableY + 1, Press: true, Button: 0})
	if a.sel != 1 {
		t.Fatalf("clicking a row while filtering did not select it: sel=%d", a.sel)
	}

	// Typing keeps applying the filter live.
	for _, r := range "cdn" {
		press(a, term.Key{Rune: r})
	}
	if len(a.shown) != 1 || !strings.Contains(a.shown[0].URL, "cdn.example.net") {
		t.Fatalf("live filter did not apply: %d flows", len(a.shown))
	}
	if a.p.text() != "cdn" {
		t.Fatalf("filter text = %q", a.p.text())
	}

	// Horizontal keys move the caret, not the selection.
	press(a, term.Key{Name: "left"})
	if a.p.CX != 2 {
		t.Fatalf("left did not move the caret: %d", a.p.CX)
	}

	press(a, term.Key{Name: "enter"})
	if a.p != nil {
		t.Fatal("enter did not close the prompt")
	}
	if a.filterText != "cdn" {
		t.Fatalf("filter was lost on submit: %q", a.filterText)
	}
}

// flatLines renders styled lines to plain text, one per logical line.
func flatLines(lines []tline) string {
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l.plainText())
	}
	return b.String()
}

func TestBodyViewExplainsUndecodableEncoding(t *testing.T) {
	a := newTestApp(t)
	headers := []core.Header{{Name: "Content-Encoding", Value: "br"}}
	body := []byte{0x1b, 0x00, 0xff, 0xfe, 0x01, 0x02, 0x80}
	lines := bodyLines(a.theme, headers, body, false, a.theme.Base)
	got := flatLines(lines)
	if !strings.Contains(got, "br-compressed") {
		t.Fatalf("undecodable body was not explained:\n%s", got)
	}
	if !strings.Contains(got, "keep-encoding") {
		t.Fatalf("explanation should point at -keep-encoding:\n%s", got)
	}

	// A gzip body must still be expanded rather than hex dumped.
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(`{"hello":"world"}`))
	_ = zw.Close()
	lines = bodyLines(a.theme, []core.Header{{Name: "Content-Encoding", Value: "gzip"}}, buf.Bytes(), false, a.theme.Base)
	got2 := flatLines(lines)
	if !strings.Contains(got2, `"hello"`) || !strings.Contains(got2, `"world"`) {
		t.Fatalf("gzip body was not decoded:\n%s", got2)
	}
	if !strings.Contains(got2, "\n") {
		t.Fatalf("JSON body was not pretty printed:\n%s", got2)
	}
}

type fakeSysProxy struct {
	supported bool
	active    bool
	target    string
	enableErr error
	applied   []string
	disables  int
}

func (f *fakeSysProxy) Supported() bool { return f.supported }
func (f *fakeSysProxy) Active() bool    { return f.active }
func (f *fakeSysProxy) Target() string  { return f.target }
func (f *fakeSysProxy) EnableAddr(addr string) error {
	if f.enableErr != nil {
		return f.enableErr
	}
	f.applied = append(f.applied, addr)
	f.active = true
	f.target = "127.0.0.1:65000"
	return nil
}
func (f *fakeSysProxy) Disable() error {
	f.disables++
	f.active = false
	f.target = ""
	return nil
}

func TestSystemProxyToggleFromCertView(t *testing.T) {
	a := newTestApp(t)
	fake := &fakeSysProxy{supported: true}
	a.sysProxy = fake
	a.view = ViewCert
	a.render()

	certBody := func() string {
		out := ""
		for y := 3; y < 30; y++ {
			out += rowText(a.screen, y)
		}
		return out
	}
	if !strings.Contains(certBody(), "Proxy") {
		t.Fatalf("the certificate view should report the system proxy state:\n%s", certBody())
	}

	press(a, term.Key{Rune: 's'})
	if !fake.active {
		t.Fatalf("s did not enable the system proxy (status %q)", a.statusText)
	}
	if len(fake.applied) != 1 || fake.applied[0] != a.proxy.Addr() {
		t.Fatalf("enabled with %v, want the listen address %s", fake.applied, a.proxy.Addr())
	}
	a.render()
	if !strings.Contains(certBody(), "on ->") {
		t.Fatalf("the certificate view should show the proxy as on:\n%s", certBody())
	}

	press(a, term.Key{Rune: 's'})
	if fake.active || fake.disables != 1 {
		t.Fatalf("second s did not disable it: active=%v disables=%d", fake.active, fake.disables)
	}
}

func TestSystemProxyRepointsWhenThePortChanges(t *testing.T) {
	a := newTestAppAt(t, "127.0.0.1:0")
	if err := a.proxy.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.proxy.Close()
	fake := &fakeSysProxy{supported: true}
	a.sysProxy = fake
	a.view = ViewCert

	press(a, term.Key{Rune: 's'})
	if !fake.active {
		t.Fatal("could not enable the fake system proxy")
	}

	target := freeAddr(t)
	port := target[strings.LastIndex(target, ":")+1:]
	press(a, term.Key{Rune: 'p'})
	a.p.Value = []rune(port)
	a.p.CX = len(a.p.Value)
	press(a, term.Key{Name: "enter"})

	if a.proxy.Addr() != target {
		t.Fatalf("port change failed: %s", a.proxy.Addr())
	}
	if len(fake.applied) != 2 || fake.applied[1] != target {
		t.Fatalf("the system proxy was not re-pointed: %v", fake.applied)
	}
}

func TestSystemProxyUnsupportedPlatform(t *testing.T) {
	a := newTestApp(t)
	a.sysProxy = &fakeSysProxy{supported: false}
	a.view = ViewCert
	press(a, term.Key{Rune: 's'})
	if a.statusKind != 2 || !strings.Contains(a.statusText, "not available") {
		t.Fatalf("expected an explanatory error, got %q", a.statusText)
	}
}

func TestSystemProxyEnableFailureIsReported(t *testing.T) {
	a := newTestApp(t)
	fake := &fakeSysProxy{supported: true, enableErr: errors.New("networksetup refused")}
	a.sysProxy = fake
	a.view = ViewCert
	press(a, term.Key{Rune: 's'})
	if fake.active {
		t.Fatal("a failed enable must not report success")
	}
	if a.statusKind != 2 || !strings.Contains(a.statusText, "networksetup refused") {
		t.Fatalf("failure was not surfaced: %q", a.statusText)
	}
}

// rowText reads a rendered row straight out of the cell buffer, so assertions
// are not confused by the same word appearing in a button label.
func rowText(s *Screen, y int) string {
	var b strings.Builder
	for x := 0; x < s.W; x++ {
		c := s.cells[y*s.W+x]
		if c.cont {
			continue
		}
		b.WriteRune(c.r)
	}
	return b.String()
}

func TestSystemProxyIndicatorInStatusBar(t *testing.T) {
	a := newTestApp(t)
	fake := &fakeSysProxy{supported: true}
	a.sysProxy = fake
	a.view = ViewCert
	a.render()
	if strings.Contains(rowText(a.screen, 0), "sys-proxy") {
		t.Fatalf("the indicator should be hidden while the system proxy is off: %q", rowText(a.screen, 0))
	}
	press(a, term.Key{Rune: 's'})
	a.render()
	if !strings.Contains(rowText(a.screen, 0), "sys-proxy") {
		t.Fatalf("the status bar should mark an active system proxy: %q", rowText(a.screen, 0))
	}
	// The certificate view itself must say so too.
	body := ""
	for y := 3; y < 30; y++ {
		body += rowText(a.screen, y)
	}
	if !strings.Contains(body, "on ->") {
		t.Fatalf("certificate view does not report the active system proxy: %q", body)
	}
}

type fakeTrust struct {
	available  bool
	status     trust.Status
	checkErr   error
	where      string
	installErr error
	removeErr  error
	installs   []bool
	removes    int
}

func (f *fakeTrust) Available() bool { return f.available }
func (f *fakeTrust) Check() (trust.Status, error) {
	return f.status, f.checkErr
}
func (f *fakeTrust) Install(system bool) (string, error) {
	f.installs = append(f.installs, system)
	if f.installErr != nil {
		return "", f.installErr
	}
	f.status = trust.Status{Installed: true, Detail: "fake store"}
	return f.where, nil
}
func (f *fakeTrust) Uninstall(bool) error {
	f.removes++
	if f.removeErr != nil {
		return f.removeErr
	}
	f.status = trust.Status{Detail: "not installed"}
	return nil
}

func TestInstallCertificateFromCertView(t *testing.T) {
	a := newTestApp(t)
	fake := &fakeTrust{available: true, where: "login keychain (this user)"}
	a.trust = fake
	a.view = ViewCert
	a.refreshTrust()

	press(a, term.Key{Rune: 'i'})
	if len(fake.installs) != 1 || fake.installs[0] {
		t.Fatalf("i should install for this user, got %v", fake.installs)
	}
	if a.statusKind != 1 || !strings.Contains(a.statusText, "login keychain") {
		t.Fatalf("status = %q", a.statusText)
	}
	if !a.trustStatus.Installed {
		t.Fatal("the trust state was not refreshed after installing")
	}
	a.render()
	body := ""
	for y := 3; y < 30; y++ {
		body += rowText(a.screen, y)
	}
	if !strings.Contains(body, "Trusted") || !strings.Contains(body, "trusted") {
		t.Fatalf("certificate view does not show the trust state: %q", body)
	}

	// Shift-I targets every user.
	press(a, term.Key{Rune: 'I'})
	if len(fake.installs) != 2 || !fake.installs[1] {
		t.Fatalf("I should install system-wide, got %v", fake.installs)
	}
}

func TestUninstallCertificateFromCertView(t *testing.T) {
	a := newTestApp(t)
	fake := &fakeTrust{available: true, status: trust.Status{Installed: true}}
	a.trust = fake
	a.view = ViewCert
	a.refreshTrust()
	if !a.trustStatus.Installed {
		t.Fatal("precondition: should start trusted")
	}

	press(a, term.Key{Rune: 'u'})
	if fake.removes != 1 {
		t.Fatalf("u did not uninstall: %d", fake.removes)
	}
	if a.trustStatus.Installed {
		t.Fatal("the trust state was not refreshed after removing")
	}
	if a.statusKind != 1 {
		t.Fatalf("status = %q (%d)", a.statusText, a.statusKind)
	}
}

func TestCertificateInstallUnavailableOrFailing(t *testing.T) {
	a := newTestApp(t)
	a.trust = &fakeTrust{available: false}
	a.view = ViewCert
	press(a, term.Key{Rune: 'i'})
	if a.statusKind != 2 || !strings.Contains(a.statusText, "not available") {
		t.Fatalf("status = %q", a.statusText)
	}

	a2 := newTestApp(t)
	a2.trust = &fakeTrust{available: true, installErr: errors.New("security refused")}
	a2.view = ViewCert
	press(a2, term.Key{Rune: 'i'})
	if a2.statusKind != 2 || !strings.Contains(a2.statusText, "security refused") {
		t.Fatalf("failure not surfaced: %q", a2.statusText)
	}
}

// The real application performs the install in the background so a macOS
// authorisation dialog cannot freeze the UI.
func TestCertificateInstallRunsInTheBackground(t *testing.T) {
	a := newTestApp(t)
	a.trust = &fakeTrust{available: true, where: "login keychain"}
	a.certCh = make(chan certResult, 1)
	a.view = ViewCert

	press(a, term.Key{Rune: 'i'})
	if !a.certPending {
		t.Fatal("the operation should be marked pending right away")
	}
	if !strings.Contains(a.statusText, "installing") {
		t.Fatalf("status should say it is working: %q", a.statusText)
	}
	a.render()
	if !strings.Contains(rowText(a.screen, 0), "cert…") {
		t.Fatalf("status bar should show progress: %q", rowText(a.screen, 0))
	}

	select {
	case res := <-a.certCh:
		a.finishCert(res)
	case <-time.After(2 * time.Second):
		t.Fatal("the background install never reported back")
	}
	if a.certPending {
		t.Fatal("pending flag not cleared")
	}
	if a.statusKind != 1 || !strings.Contains(a.statusText, "installed") {
		t.Fatalf("status = %q", a.statusText)
	}
}

// --- pane tabs, collapsing and wrapping -----------------------------------

func TestPaneTabsSwitchIndependently(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)
	a.openDetail(a.store.Snapshot()[0])
	a.view = ViewDetail
	a.render()

	if got := a.paneTab(0); got != TabHeaders {
		t.Fatalf("request pane starts on %v", got)
	}
	a.focus = 0
	press(a, term.Key{Name: "shift-tab"})
	if a.reqTab != TabBody {
		t.Fatalf("shift-tab did not cycle the request pane: %v", a.reqTab)
	}
	if a.respTab != TabHeaders {
		t.Fatalf("the response pane changed too: %v", a.respTab)
	}

	// Tab swaps which pane the keys affect.
	press(a, term.Key{Name: "tab"})
	if a.focus != 1 {
		t.Fatalf("tab did not swap panes: focus=%d", a.focus)
	}
	press(a, term.Key{Rune: 'r'})
	if a.respTab != TabRaw || a.reqTab != TabBody {
		t.Fatalf("raw applied to the wrong pane: req=%v resp=%v", a.reqTab, a.respTab)
	}
	press(a, term.Key{Rune: 'h'})
	if a.respTab != TabHeaders {
		t.Fatalf("h did not select headers: %v", a.respTab)
	}
	// In the detail view 'b' is free and selects the body tab.
	press(a, term.Key{Rune: 'b'})
	if a.respTab != TabBody {
		t.Fatalf("b did not select the body tab in the detail view: %v", a.respTab)
	}
	// On the flow list 'b' keeps its old meaning (toggle request breakpoints).
	a.view = ViewFlows
	before := a.opts.BreakRequests.Load()
	press(a, term.Key{Rune: 'b'})
	if a.opts.BreakRequests.Load() == before {
		t.Fatal("b stopped toggling request breakpoints on the flow list")
	}
}

func TestPaneTabsRenderAndAreClickable(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)
	a.openDetail(a.store.Snapshot()[0])
	a.view = ViewDetail
	a.render()

	if len(a.lay.paneTabsL) != int(paneTabCount) || len(a.lay.paneTabsR) != int(paneTabCount) {
		t.Fatalf("pane tab hit areas missing: L=%v R=%v", a.lay.paneTabsL, a.lay.paneTabsR)
	}
	rawRect := a.lay.paneTabsR[TabRaw]
	a.handleMouse(term.Mouse{X: rawRect.X, Y: rawRect.Y, Motion: true})
	if a.hoverPaneTab != 1 || a.hoverPaneTabIndex != int(TabRaw) {
		t.Fatalf("hovering the raw tab did not register: %d/%d", a.hoverPaneTab, a.hoverPaneTabIndex)
	}
	a.handleMouse(term.Mouse{X: rawRect.X, Y: rawRect.Y, Press: true, Button: 0})
	if a.respTab != TabRaw {
		t.Fatalf("clicking the raw tab did not select it: %v", a.respTab)
	}
	if a.focus != 1 {
		t.Fatalf("clicking a pane tab should focus that pane: %d", a.focus)
	}

	// The tab labels are drawn in the pane border.
	frame := a.screen.Render(nil, true)
	for _, want := range []string{"headers", "body", "raw"} {
		if !strings.Contains(frame, want) {
			t.Errorf("pane tab %q is not rendered", want)
		}
	}
}

func TestCollapsePanes(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)
	a.openDetail(a.store.Snapshot()[0])
	a.view = ViewDetail

	press(a, term.Key{Rune: '['})
	if !a.collapseL || a.collapseR {
		t.Fatalf("hiding the left pane failed: %v/%v", a.collapseL, a.collapseR)
	}
	a.render()
	frame := a.screen.Render(nil, true)
	if !strings.Contains(frame, "request hidden") {
		t.Fatalf("the collapsed state is not explained:\\n%s", frame)
	}
	if strings.Contains(frame, "request ·") {
		t.Fatal("the request pane is still drawn")
	}

	press(a, term.Key{Rune: ']'})
	if a.collapseL {
		t.Fatal("hiding the right pane should reveal the left one again")
	}
	press(a, term.Key{Rune: '\\'})
	if a.collapseL || a.collapseR {
		t.Fatal("backslash should show both panes")
	}
}

func TestJSONBodyIsPrettyPrintedAndWrapped(t *testing.T) {
	a := newTestApp(t)
	body := []byte(`{"user":{"id":42,"name":"a rather long value that will not fit on one line at all","tags":["a","b"]},"ok":true}`)
	lines := bodyLines(a.theme, nil, body, false, a.theme.Base)

	text := flatLines(lines)
	if !strings.Contains(text, "\n  ") {
		t.Fatalf("JSON was not indented:\n%s", text)
	}
	if !strings.Contains(text, `"user"`) {
		t.Fatalf("JSON content lost:\n%s", text)
	}

	// Long lines must wrap rather than be cut off.
	visual := wrapLines(lines, 40)
	for _, l := range visual {
		w := 0
		for _, r := range l {
			w += TextWidth(r.text)
		}
		if w > 40 {
			t.Fatalf("line exceeds the pane width after wrapping: %d cells", w)
		}
	}
	if len(visual) <= len(lines) {
		t.Fatal("wrapping did not produce extra visual lines")
	}
}

func TestWrapTextKeepsIndentation(t *testing.T) {
	got := wrapText("      "+strings.Repeat("x", 60), 30, 6)
	if len(got) < 3 {
		t.Fatalf("expected several lines, got %v", got)
	}
	for i, l := range got[1:] {
		if !strings.HasPrefix(l, "      ") {
			t.Fatalf("continuation line %d lost its indent: %q", i+1, l)
		}
	}
	for _, l := range got {
		if TextWidth(l) > 30 {
			t.Fatalf("line too wide: %q", l)
		}
	}
}

// --- saved filters --------------------------------------------------------

func TestSavedFiltersApplyAndPersist(t *testing.T) {
	a := newTestApp(t)
	a.filterset = core.NewFilterSet(filepath.Join(t.TempDir(), "filters.json"))
	seedFlows(t, a)
	a.view = ViewFilters
	a.refresh()

	if len(a.shown) != 3 {
		t.Fatalf("expected all flows before filtering, got %d", len(a.shown))
	}
	f := a.filterset.Add("status:5xx", "server errors")
	a.refresh()
	if len(a.shown) != 1 || a.shown[0].Status != 500 {
		t.Fatalf("saved filter did not apply: %d flows", len(a.shown))
	}

	// Saved filters survive switching tabs.
	a.view = ViewFlows
	a.refresh()
	if len(a.shown) != 1 {
		t.Fatalf("saved filter stopped applying after a tab switch: %d", len(a.shown))
	}

	// Disabling it brings everything back.
	a.filterset.Toggle(f.ID)
	a.refresh()
	if len(a.shown) != 3 {
		t.Fatalf("disabling the filter had no effect: %d", len(a.shown))
	}
}

func TestSavedFiltersCombineWithTheFilterBar(t *testing.T) {
	a := newTestApp(t)
	a.filterset = core.NewFilterSet("")
	seedFlows(t, a)
	a.filterset.Add("status:5xx", "")
	a.filterText = "upload"
	a.refresh()

	if len(a.shown) != 1 {
		t.Fatalf("bar + saved filters should intersect: %d flows", len(a.shown))
	}
	if !strings.Contains(a.shown[0].URL, "cdn.example.net") {
		t.Fatalf("wrong flow survived: %s", a.shown[0].URL)
	}

	// A bar filter that matches but violates the saved one must yield nothing.
	a.filterText = "health"
	a.refresh()
	if len(a.shown) != 0 {
		t.Fatalf("saved filter was ignored: %d flows", len(a.shown))
	}
}

func TestFiltersViewKeys(t *testing.T) {
	a := newTestApp(t)
	a.filterset = core.NewFilterSet("")
	a.view = ViewFilters
	a.refresh()

	// a opens the prompt, Enter stores the filter.
	press(a, term.Key{Rune: 'a'})
	if a.p == nil {
		t.Fatal("a did not open the add-filter prompt")
	}
	for _, r := range "status:4xx" {
		press(a, term.Key{Rune: r})
	}
	press(a, term.Key{Name: "enter"})
	if a.filterset.Len() != 1 {
		t.Fatalf("filter was not saved: %d", a.filterset.Len())
	}
	a.refresh()
	if len(a.filterList) != 1 {
		t.Fatalf("filter list not refreshed: %d", len(a.filterList))
	}

	// space toggles it.
	press(a, term.Key{Rune: ' '})
	if a.filterList[0].Enabled {
		t.Fatal("space did not disable the filter")
	}

	// p pins whatever is in the filter bar.
	a.filterText = "host:api"
	press(a, term.Key{Rune: 'p'})
	if a.filterset.Len() != 2 {
		t.Fatalf("pinning failed: %d", a.filterset.Len())
	}

	// d removes the selected one.
	a.filterSel = 0
	press(a, term.Key{Rune: 'd'})
	if a.filterset.Len() != 1 {
		t.Fatalf("delete failed: %d", a.filterset.Len())
	}
}

func TestFilterPromptCommitsWhenLeavingTheList(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)
	a.view = ViewFlows
	press(a, term.Key{Rune: 'f'})
	for _, r := range "cdn" {
		press(a, term.Key{Rune: r})
	}
	if a.p == nil {
		t.Fatal("the filter prompt should still be open")
	}
	// Double-clicking a row drills into the detail view; the prompt must not
	// keep swallowing the keyboard afterwards.
	a.render()
	rowY := a.lay.tableY
	a.handle(term.Event{Kind: term.EvMouse, Mouse: term.Mouse{X: 6, Y: rowY, Press: true, Button: 0}})
	a.handle(term.Event{Kind: term.EvMouse, Mouse: term.Mouse{X: 6, Y: rowY, Press: true, Button: 0}})
	if a.view != ViewDetail {
		t.Fatalf("double click did not open the detail view: %v", a.view)
	}
	if a.p != nil {
		t.Fatal("the filter prompt is still open in the detail view")
	}
	// Esc must now leave the detail view, and the filter stays applied.
	press(a, term.Key{Name: "esc"})
	if a.view != ViewFlows {
		t.Fatalf("esc did not return to the flows list: %v", a.view)
	}
	if a.filterText != "cdn" {
		t.Fatalf("the filter was lost: %q", a.filterText)
	}
	if len(a.shown) != 1 {
		t.Fatalf("the filter is no longer applied: %d flows", len(a.shown))
	}
}

func TestHelpIsTwoColumnsWhenWide(t *testing.T) {
	a := newTestApp(t)
	a.W, a.H = 150, 40
	a.screen.Resize(a.W, a.H)
	a.prev.Resize(a.W, a.H)
	a.view = ViewHelp
	a.render()

	// With two columns the last section sits in the right half of the screen.
	right := ""
	for y := 3; y < a.H-2; y++ {
		line := rowText(a.screen, y)
		if len(line) > a.W/2 {
			right += line[a.W/2:]
		}
	}
	if !strings.Contains(right, "Rule DSL") {
		t.Fatalf("the right column does not carry the later sections:\n%s", right)
	}
	left := ""
	for y := 3; y < a.H-2; y++ {
		line := rowText(a.screen, y)
		if len(line) > a.W/2 {
			left += line[:a.W/2]
		}
	}
	if !strings.Contains(left, "Menu & panes") {
		t.Fatalf("the left column does not start with the first section:\n%s", left)
	}
}

func TestHelpNavigationKeysWorkFromTheHelpTab(t *testing.T) {
	a := newTestApp(t)
	seedFlows(t, a)
	a.view = ViewHelp
	a.render()
	press(a, term.Key{Name: "right"})
	if a.view != ViewFlows {
		t.Fatalf("right arrow did not leave the help tab: %v", a.view)
	}
	press(a, term.Key{Name: "left"})
	if a.view != ViewHelp {
		t.Fatalf("left arrow did not come back to help: %v", a.view)
	}
}
