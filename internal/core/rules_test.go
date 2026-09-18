package core

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRuleDSL(t *testing.T) {
	cases := []struct {
		line   string
		method string
		url    string
		kinds  []ActionKind
	}{
		{"* ^https://api\\.example\\.com/v1/user :: file=/tmp/u.json", "*", `^https://api\.example\.com/v1/user`, []ActionKind{ActFile}},
		{"GET .*/legacy$ :: redirect=https://staging/legacy", "GET", `.*/legacy$`, []ActionKind{ActRedirect}},
		{"POST,PUT ^/x :: status=503 :: body={\"a\":1}", "POST,PUT", "^/x", []ActionKind{ActStatus, ActBody}},
		{"* example\\.com :: header=X-Debug=1 :: break=req", "*", `example\.com`, []ActionKind{ActHeader, ActBreak}},
		{"* ^http://y/ :: block=403", "*", "^http://y/", []ActionKind{ActBlock}},
		{"* ^http://z/ :: delay=750ms", "*", "^http://z/", []ActionKind{ActDelay}},
	}
	for _, tc := range cases {
		r, err := ParseRule(tc.line)
		if err != nil {
			t.Fatalf("ParseRule(%q): %v", tc.line, err)
		}
		if r.Method != tc.method {
			t.Errorf("%q: method = %q, want %q", tc.line, r.Method, tc.method)
		}
		if r.URL != tc.url {
			t.Errorf("%q: url = %q, want %q", tc.line, r.URL, tc.url)
		}
		if len(r.Actions) != len(tc.kinds) {
			t.Fatalf("%q: %d actions, want %d", tc.line, len(r.Actions), len(tc.kinds))
		}
		for i, k := range tc.kinds {
			if r.Actions[i].Kind != k {
				t.Errorf("%q: action %d = %q, want %q", tc.line, i, r.Actions[i].Kind, k)
			}
		}
	}
}

func TestParseRuleRejectsBadInput(t *testing.T) {
	for _, line := range []string{
		"",
		"# comment",
		"* ^/x",
		"* ^/x :: notanaction=1",
		"* [ :: file=/tmp/x",
	} {
		if _, err := ParseRule(line); err == nil {
			t.Errorf("ParseRule(%q) unexpectedly succeeded", line)
		}
	}
}

func TestRuleStringRoundTrip(t *testing.T) {
	line := `GET ^https://api\.example\.com/v1/user$ :: header=X-Debug=1 :: file=/tmp/u.json`
	r, err := ParseRule(line)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseRule(r.String())
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if again.String() != r.String() {
		t.Fatalf("round trip changed the rule: %q vs %q", again.String(), r.String())
	}
}

func TestRuleMatching(t *testing.T) {
	r, _ := ParseRule(`GET ^https://api\.example\.com/v1/.* :: file=/tmp/x`)
	if !r.Match("GET", "https://api.example.com/v1/users") {
		t.Fatal("expected match")
	}
	if r.Match("POST", "https://api.example.com/v1/users") {
		t.Fatal("method should not match")
	}
	if r.Match("GET", "https://other.example.com/v1/users") {
		t.Fatal("url should not match")
	}
	r.Enabled = false
	if r.Match("GET", "https://api.example.com/v1/users") {
		t.Fatal("disabled rule matched")
	}
}

func TestHeaderArgParsing(t *testing.T) {
	r, _ := ParseRule(`* ^/x :: header=X-Trace=abc=def`)
	if len(r.Actions) != 1 {
		t.Fatalf("actions = %+v", r.Actions)
	}
	name, value := splitPairForTest(r.Actions[0].Arg)
	if name != "X-Trace" || value != "abc=def" {
		t.Fatalf("header arg = %q / %q", name, value)
	}
}

func splitPairForTest(s string) (string, string) {
	if i := strings.Index(s, "="); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

func TestRuleSetPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	rs := NewRuleSet(path)
	if err := rs.Load(); err != nil {
		t.Fatalf("load missing file: %v", err)
	}
	rs.Add(mustRule(t, `* ^/one :: file=/tmp/1`))
	rs.Add(mustRule(t, `GET ^/two$ :: block`))
	if rs.Len() != 2 {
		t.Fatalf("len = %d", rs.Len())
	}

	reloaded := NewRuleSet(path)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Len() != 2 {
		t.Fatalf("reloaded len = %d", reloaded.Len())
	}
	got := reloaded.Match("GET", "http://host/two")
	if len(got) != 1 {
		t.Fatalf("reloaded rule did not match: %+v", got)
	}
	if !got[0].Has(ActBlock) {
		t.Fatalf("actions lost in persistence: %+v", got[0].Actions)
	}
}

func TestRuleToggleAndRemove(t *testing.T) {
	rs := NewRuleSet("")
	a := rs.Add(mustRule(t, `* ^/a$ :: block`))
	if got := rs.Match("GET", "http://h/a"); len(got) != 1 {
		t.Fatal("rule should match while enabled")
	}
	if rs.Toggle(a.ID) {
		t.Fatal("toggle should disable")
	}
	if got := rs.Match("GET", "http://h/a"); len(got) != 0 {
		t.Fatal("disabled rule matched")
	}
	rs.Remove(a.ID)
	if rs.Len() != 0 {
		t.Fatal("remove failed")
	}
}

func TestImportExportText(t *testing.T) {
	rs := NewRuleSet("")
	doc := `
# a comment
* ^/one :: file=/tmp/1
GET ^/two :: block=403
this line is invalid
`
	added, errs := rs.ImportText(doc)
	if len(added) != 2 {
		t.Fatalf("added = %d, errs = %v", len(added), errs)
	}
	if len(errs) != 1 {
		t.Fatalf("expected exactly one error, got %v", errs)
	}
	out := rs.ExportText()
	if !strings.Contains(out, "file=/tmp/1") || !strings.Contains(out, "block=403") {
		t.Fatalf("export missing rules:\n%s", out)
	}
}

func mustRule(t *testing.T, line string) *Rule {
	t.Helper()
	r, err := ParseRule(line)
	if err != nil {
		t.Fatalf("ParseRule(%q): %v", line, err)
	}
	return r
}
