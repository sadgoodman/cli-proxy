package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// ActionKind enumerates the operations a rule can perform.
type ActionKind string

const (
	ActFile      ActionKind = "file"      // reply with the contents of a local file
	ActRedirect  ActionKind = "redirect"  // fetch the response from another URL
	ActStatus    ActionKind = "status"    // override the response status code
	ActBody      ActionKind = "body"      // override the response body
	ActHeader    ActionKind = "header"    // set/add a request header
	ActDelHeader ActionKind = "delheader" // remove a request header
	ActBreak     ActionKind = "break"     // pause for editing: req | resp | both
	ActBlock     ActionKind = "block"     // refuse the request
	ActDelay     ActionKind = "delay"     // sleep before forwarding
	ActMapLocal  ActionKind = "maplocal"  // serve a local file under a URL prefix

	ActResHeader    ActionKind = "resheader"    // set/add a response header
	ActDelResHeader ActionKind = "delresheader" // remove a response header
	ActLocation     ActionKind = "location"     // reply with a 302 to another address
)

// Action is one operation inside a rule.
type Action struct {
	Kind ActionKind `json:"kind"`
	Arg  string     `json:"arg,omitempty"`
}

// Rule couples a matcher (method + URL regexp) with an ordered action list.
type Rule struct {
	ID      int      `json:"id"`
	Enabled bool     `json:"enabled"`
	Method  string   `json:"method,omitempty"`
	URL     string   `json:"url"`
	Actions []Action `json:"actions"`
	Note    string   `json:"note,omitempty"`

	re  *regexp.Regexp
	err string
}

var knownMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	"HEAD": true, "OPTIONS": true, "TRACE": true, "CONNECT": true,
}

// Compile validates and precompiles the rule's URL matcher.
func (r *Rule) Compile() error {
	r.re, r.err = nil, ""
	if strings.TrimSpace(r.URL) == "" {
		r.URL = ".*"
	}
	re, err := regexp.Compile(r.URL)
	if err != nil {
		r.err = err.Error()
		return err
	}
	r.re = re
	return nil
}

// Err returns the compile error, if any.
func (r *Rule) Err() string { return r.err }

// Match reports whether the rule applies to a method/url pair. The pattern is
// tested against the full URL and against the path+query, so both
// "^https://api\\.example\\.com/v1" and "^/v1" work as expected.
func (r *Rule) Match(method, url string) bool {
	if !r.Enabled {
		return false
	}
	if !methodMatches(r.Method, method) {
		return false
	}
	if r.re == nil {
		if err := r.Compile(); err != nil {
			return false
		}
	}
	if r.re.MatchString(url) {
		return true
	}
	if p := URLPath(url); p != url {
		return r.re.MatchString(p)
	}
	return false
}

// URLPath extracts "/path?query" from an absolute URL without allocating a
// full url.URL.
func URLPath(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			return rest[j:]
		}
		return "/"
	}
	return u
}

func methodMatches(spec, method string) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "*" {
		return true
	}
	for _, m := range strings.Split(spec, ",") {
		m = strings.ToUpper(strings.TrimSpace(m))
		if m == method || m == "*" {
			return true
		}
	}
	return false
}

// Has reports whether the rule carries an action of the given kind.
func (r *Rule) Has(kind ActionKind) bool {
	for _, a := range r.Actions {
		if a.Kind == kind {
			return true
		}
	}
	return false
}

// String renders the rule in the compact one-line DSL.
func (r *Rule) String() string {
	method := strings.TrimSpace(r.Method)
	if method == "" {
		method = "*"
	}
	var b strings.Builder
	b.WriteString(method)
	b.WriteString(" ")
	b.WriteString(r.URL)
	for _, a := range r.Actions {
		b.WriteString(" :: ")
		b.WriteString(string(a.Kind))
		if a.Arg != "" {
			b.WriteString("=")
			b.WriteString(a.Arg)
		}
	}
	return b.String()
}

// ParseRule parses the compact DSL:
//
//	[METHOD] URL_REGEX :: action[=arg] :: action[=arg] ...
//
// Examples:
//
//   - ^https://api\.example\.com/v1/user   :: file=/tmp/user.json
//     GET .*/legacy$                          :: redirect=https://staging/legacy
//   - example\.com                         :: header=X-Debug=1 :: break=req
//   - ^http://x/logo\.png                  :: location=https://cdn/logo.png
func ParseRule(line string) (*Rule, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil, fmt.Errorf("empty rule")
	}
	parts := strings.Split(line, "::")
	head := strings.TrimSpace(parts[0])

	method := "*"
	urlPart := head
	if fields := strings.Fields(head); len(fields) > 1 {
		first := strings.ToUpper(fields[0])
		if first == "*" || knownMethods[first] || isMethodList(first) {
			method = first
			urlPart = strings.TrimSpace(strings.TrimPrefix(head, fields[0]))
		}
	}
	if strings.TrimSpace(urlPart) == "" {
		urlPart = ".*"
	}
	r := &Rule{Enabled: true, Method: method, URL: urlPart}
	for _, raw := range parts[1:] {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		kind, arg := raw, ""
		if i := strings.Index(raw, "="); i >= 0 {
			kind, arg = strings.TrimSpace(raw[:i]), strings.TrimSpace(raw[i+1:])
		}
		k := ActionKind(strings.ToLower(kind))
		switch k {
		case ActFile, ActRedirect, ActStatus, ActBody, ActHeader, ActDelHeader,
			ActBreak, ActBlock, ActDelay, ActMapLocal,
			ActResHeader, ActDelResHeader, ActLocation:
		default:
			return nil, fmt.Errorf("unknown action %q", kind)
		}
		r.Actions = append(r.Actions, Action{Kind: k, Arg: arg})
	}
	if len(r.Actions) == 0 {
		return nil, fmt.Errorf("rule has no actions")
	}
	if err := r.Compile(); err != nil {
		return nil, err
	}
	return r, nil
}

func isMethodList(s string) bool {
	if !strings.Contains(s, ",") {
		return false
	}
	for _, m := range strings.Split(s, ",") {
		if !knownMethods[strings.TrimSpace(m)] {
			return false
		}
	}
	return true
}

// RuleSet is a concurrency-safe, optionally persisted collection of rules.
type RuleSet struct {
	mu     sync.RWMutex
	rules  []*Rule
	nextID int
	path   string
}

// NewRuleSet creates an empty rule set optionally backed by a JSON file.
func NewRuleSet(path string) *RuleSet {
	return &RuleSet{path: path}
}

// Path returns the backing file location.
func (rs *RuleSet) Path() string { return rs.path }

// List returns copies of the rules.
func (rs *RuleSet) List() []Rule {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	out := make([]Rule, len(rs.rules))
	for i, r := range rs.rules {
		c := *r
		c.Actions = append([]Action(nil), r.Actions...)
		out[i] = c
	}
	return out
}

// Len returns the number of rules.
func (rs *RuleSet) Len() int {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return len(rs.rules)
}

// Add inserts a rule, assigning an id.
func (rs *RuleSet) Add(r *Rule) *Rule {
	rs.mu.Lock()
	rs.nextID++
	r.ID = rs.nextID
	rs.rules = append(rs.rules, r)
	rs.mu.Unlock()
	_ = rs.Save()
	return r
}

// Replace swaps the rule with the same id, or appends it.
func (rs *RuleSet) Replace(r *Rule) {
	rs.mu.Lock()
	found := false
	for i, old := range rs.rules {
		if old.ID == r.ID {
			rs.rules[i] = r
			found = true
			break
		}
	}
	if !found {
		rs.nextID++
		r.ID = rs.nextID
		rs.rules = append(rs.rules, r)
	}
	rs.mu.Unlock()
	_ = rs.Save()
}

// Remove deletes the rule with the given id.
func (rs *RuleSet) Remove(id int) {
	rs.mu.Lock()
	for i, r := range rs.rules {
		if r.ID == id {
			rs.rules = append(rs.rules[:i], rs.rules[i+1:]...)
			break
		}
	}
	rs.mu.Unlock()
	_ = rs.Save()
}

// Toggle flips the enabled flag of a rule and returns the new value.
func (rs *RuleSet) Toggle(id int) bool {
	rs.mu.Lock()
	var enabled bool
	for _, r := range rs.rules {
		if r.ID == id {
			r.Enabled = !r.Enabled
			enabled = r.Enabled
			break
		}
	}
	rs.mu.Unlock()
	_ = rs.Save()
	return enabled
}

// Match returns the enabled rules matching a method/url pair, in order.
func (rs *RuleSet) Match(method, url string) []*Rule {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	var out []*Rule
	for _, r := range rs.rules {
		if r.Match(method, url) {
			out = append(out, r)
		}
	}
	return out
}

// Clear removes every rule.
func (rs *RuleSet) Clear() {
	rs.mu.Lock()
	rs.rules = nil
	rs.mu.Unlock()
	_ = rs.Save()
}

// Load reads rules from the backing JSON file. A missing file is not an error.
func (rs *RuleSet) Load() error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.path == "" {
		return nil
	}
	data, err := os.ReadFile(rs.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var rules []*Rule
	if err := json.Unmarshal(data, &rules); err != nil {
		return err
	}
	rs.rules = rules
	for _, r := range rs.rules {
		_ = r.Compile()
		if r.ID > rs.nextID {
			rs.nextID = r.ID
		}
	}
	return nil
}

// Save writes the rules to the backing JSON file.
func (rs *RuleSet) Save() error {
	rs.mu.RLock()
	path := rs.path
	data, err := json.MarshalIndent(rs.rules, "", "  ")
	rs.mu.RUnlock()
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ImportText parses a multi-line DSL document and appends every valid rule.
// It returns the accepted rules plus per-line errors.
func (rs *RuleSet) ImportText(text string) ([]*Rule, []string) {
	var added []*Rule
	var errs []string
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		r, err := ParseRule(trimmed)
		if err != nil {
			errs = append(errs, fmt.Sprintf("line %d: %v", i+1, err))
			continue
		}
		added = append(added, rs.Add(r))
	}
	return added, errs
}

// ExportText renders the rule set as DSL lines.
func (rs *RuleSet) ExportText() string {
	list := rs.List()
	var b strings.Builder
	for _, r := range list {
		b.WriteString(r.String())
		b.WriteString("\n")
	}
	return b.String()
}
