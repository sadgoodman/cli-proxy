package core

import (
	"path/filepath"
	"testing"
)

func TestFilterSetMatchCombinesWithAnd(t *testing.T) {
	fs := NewFilterSet("")
	fs.Add("method:GET", "")
	fs.Add("status:2xx", "")

	flow := &Flow{Method: "GET", Status: 200, URL: "https://api.example.com/users"}
	if !fs.Match(flow) {
		t.Fatal("a flow satisfying both filters should match")
	}
	if fs.Match(&Flow{Method: "POST", Status: 200}) {
		t.Fatal("a POST must not pass a method:GET filter")
	}
	if fs.Match(&Flow{Method: "GET", Status: 500}) {
		t.Fatal("a 500 must not pass a status:2xx filter")
	}
}

func TestFilterSetToggleOnlyAffectsOne(t *testing.T) {
	fs := NewFilterSet("")
	keep := fs.Add("host:api", "")
	drop := fs.Add("status:5xx", "")

	if !fs.Match(&Flow{Host: "api.example.com", Status: 500}) {
		t.Fatal("both filters should pass a 500 from the api host")
	}
	if fs.Match(&Flow{Host: "api.example.com", Status: 200}) {
		t.Fatal("the 5xx filter should reject a 200")
	}
	if fs.Toggle(drop.ID) {
		t.Fatal("toggle should have disabled the second filter")
	}
	if !fs.Match(&Flow{Host: "api.example.com", Status: 200}) {
		t.Fatal("the disabled filter should no longer apply")
	}
	if fs.Match(&Flow{Host: "cdn.example.net", Status: 200}) {
		t.Fatal("the enabled filter must still apply")
	}
	if got := fs.EnabledExprs(); len(got) != 1 || got[0] != "host:api" {
		t.Fatalf("EnabledExprs = %v", got)
	}
	_ = keep
}

func TestFilterSetPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "filters.json")
	fs := NewFilterSet(path)
	fs.Add("method:GET", "reads")
	fs.Add("/orders$/", "orders only")
	fs.Toggle(fs.List()[1].ID)

	reloaded := NewFilterSet(path)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	list := reloaded.List()
	if len(list) != 2 {
		t.Fatalf("reloaded %d filters, want 2", len(list))
	}
	if !list[0].Enabled || list[1].Enabled {
		t.Fatalf("enabled flags were not preserved: %+v", list)
	}
	if list[0].Note != "reads" {
		t.Fatalf("note was not preserved: %+v", list[0])
	}
	if !reloaded.Match(&Flow{Method: "GET", URL: "https://h/orders"}) {
		t.Fatal("the enabled filter does not apply after a reload")
	}
	// /orders$/ is disabled, so a GET of something else must pass.
	if !reloaded.Match(&Flow{Method: "GET", URL: "https://h/other"}) {
		t.Fatal("a disabled filter is still being applied after a reload")
	}
	// Switching it back on restores the narrowing.
	reloaded.Toggle(list[1].ID)
	if reloaded.Match(&Flow{Method: "GET", URL: "https://h/other"}) {
		t.Fatal("re-enabling the filter had no effect")
	}
}

func TestFilterSetRemoveAndClear(t *testing.T) {
	fs := NewFilterSet("")
	a := fs.Add("method:GET", "")
	fs.Add("status:2xx", "")
	fs.Remove(a.ID)
	if fs.Len() != 1 {
		t.Fatalf("len = %d after remove", fs.Len())
	}
	fs.Clear()
	if fs.Len() != 0 {
		t.Fatal("clear failed")
	}
	if !fs.Match(&Flow{}) {
		t.Fatal("an empty filter set must match everything")
	}
}

func TestFilterSetReplace(t *testing.T) {
	fs := NewFilterSet("")
	f := fs.Add("method:GET", "")
	f.Expr = "method:POST"
	fs.Replace(f)
	if !fs.Match(&Flow{Method: "POST"}) {
		t.Fatal("the replaced expression was not applied")
	}
	if fs.Match(&Flow{Method: "GET"}) {
		t.Fatal("the old expression is still active")
	}
}

func TestFilterSetLoadMissingFileIsFine(t *testing.T) {
	fs := NewFilterSet(filepath.Join(t.TempDir(), "nope.json"))
	if err := fs.Load(); err != nil {
		t.Fatalf("a missing file must not be an error: %v", err)
	}
	if fs.Len() != 0 {
		t.Fatal("expected no filters")
	}
}
