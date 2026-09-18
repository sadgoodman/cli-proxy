package core

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Query is a compiled traffic filter.
//
// Tokens are combined with an implicit AND; `|` (or the word `OR`) combines
// alternatives and parentheses group them:
//
//	api/v1                        substring of the URL
//	/regex/i                      Go regexp against the URL
//	method:GET,POST               HTTP method
//	status:2xx                    status class, exact code, or >=400 / <300
//	host:example.com              substring of the host
//	body:token                    substring of the request or response body
//	tag:mock                      flow marker
//	scheme:https                  http or https
//	/orders/ | /carts/            OR
//	(method:GET | method:POST) status:5xx
//	!host:cdn.example.com         negation
type Query struct {
	Raw  string
	root qNode
	err  error
}

// ---------------------------------------------------------------------------
// expression tree
// ---------------------------------------------------------------------------

type qNode interface{ match(*Flow) bool }

type qAnd struct{ kids []qNode }

func (n qAnd) match(f *Flow) bool {
	for _, k := range n.kids {
		if !k.match(f) {
			return false
		}
	}
	return true
}

type qOr struct{ kids []qNode }

func (n qOr) match(f *Flow) bool {
	for _, k := range n.kids {
		if k.match(f) {
			return true
		}
	}
	return false
}

type qNot struct{ kid qNode }

func (n qNot) match(f *Flow) bool { return !n.kid.match(f) }

type qAtom struct{ t qToken }

func (n qAtom) match(f *Flow) bool { return n.t.matchFlow(f) }

// ---------------------------------------------------------------------------
// tokenizer
// ---------------------------------------------------------------------------

// tokenizeQuery splits an expression into atoms, `(`, `)`, `|` and `!`,
// keeping `/regex/` bodies and quoted strings intact.
func tokenizeQuery(s string) []string {
	var out []string
	rs := []rune(s)
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			i++
		case c == '(' || c == ')' || c == '|':
			out = append(out, string(c))
			i++
		case c == '!':
			out = append(out, "!")
			i++
		case c == '/':
			j := i + 1
			for j < len(rs) {
				if rs[j] == '\\' && j+1 < len(rs) {
					j += 2
					continue
				}
				if rs[j] == '/' || rs[j] == ' ' || rs[j] == '\t' {
					break
				}
				j++
			}
			// Only swallow the slash when we actually stopped on one; stopping
			// on whitespace means the regex was left unterminated.
			if j < len(rs) && rs[j] == '/' {
				j++
			}
			out = append(out, string(rs[i:j]))
			i = j
		case c == '"':
			j := i + 1
			for j < len(rs) && rs[j] != '"' {
				j++
			}
			out = append(out, string(rs[i+1:min(j, len(rs))]))
			if j < len(rs) {
				j++
			}
			i = j
		default:
			j := i
			for j < len(rs) && !strings.ContainsRune(" \t\n()|!", rs[j]) {
				j++
			}
			tok := string(rs[i:j])
			if strings.EqualFold(tok, "or") {
				tok = "|"
			}
			out = append(out, tok)
			i = j
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// parser
// ---------------------------------------------------------------------------

type qParser struct {
	toks []string
	pos  int
	err  error
}

func (p *qParser) peek() string {
	if p.pos >= len(p.toks) {
		return ""
	}
	return p.toks[p.pos]
}

func (p *qParser) next() string {
	t := p.peek()
	p.pos++
	return t
}

func (p *qParser) parseOr() qNode {
	kids := []qNode{p.parseAnd()}
	for p.peek() == "|" {
		p.next()
		kids = append(kids, p.parseAnd())
	}
	if len(kids) == 1 {
		return kids[0]
	}
	return qOr{kids: kids}
}

func (p *qParser) parseAnd() qNode {
	var kids []qNode
	for {
		t := p.peek()
		if t == "" || t == "|" || t == ")" {
			break
		}
		kids = append(kids, p.parseUnary())
	}
	switch len(kids) {
	case 0:
		// An empty group matches everything, so "a |" behaves like "a" OR all.
		return qAnd{}
	case 1:
		return kids[0]
	}
	return qAnd{kids: kids}
}

func (p *qParser) parseUnary() qNode {
	t := p.next()
	switch t {
	case "!":
		return qNot{kid: p.parseUnary()}
	case "(":
		inner := p.parseOr()
		if p.peek() == ")" {
			p.next()
		} else if p.err == nil {
			p.err = fmt.Errorf("missing )")
		}
		return inner
	case ")":
		if p.err == nil {
			p.err = fmt.Errorf("unexpected )")
		}
		return qAnd{}
	}
	tok := compileToken(t)
	if tok.err != nil && p.err == nil {
		p.err = tok.err
	}
	return qAtom{t: tok}
}

// ---------------------------------------------------------------------------
// tokens
// ---------------------------------------------------------------------------

type qToken struct {
	neg    bool
	kind   string
	value  string
	re     *regexp.Regexp
	status func(int) bool
	err    error
}

// CompileQuery parses a filter expression. Invalid input never aborts the UI:
// a broken regex falls back to a substring match and the reason is reported
// through Err.
func CompileQuery(raw string) *Query {
	q := &Query{Raw: raw}
	toks := tokenizeQuery(raw)
	if len(toks) == 0 {
		return q
	}
	p := &qParser{toks: toks}
	q.root = p.parseOr()
	if p.err == nil && p.pos < len(p.toks) {
		p.err = fmt.Errorf("unexpected %q", p.toks[p.pos])
	}
	q.err = p.err
	return q
}

func compileToken(field string) qToken {
	tok := qToken{}
	if strings.HasPrefix(field, "!") {
		tok.neg = true
		field = field[1:]
	}
	if field == "" {
		tok.kind = "url"
		return tok
	}
	if len(field) > 2 && strings.HasPrefix(field, "/") && strings.HasSuffix(field, "/") {
		body := field[1 : len(field)-1]
		if re, err := regexp.Compile(body); err == nil {
			tok.kind, tok.re = "regex", re
		} else {
			// Degrade to a substring match so the UI keeps working while the
			// expression is still being typed, but report why.
			tok.kind, tok.value, tok.err = "url", strings.ToLower(body), err
		}
		return tok
	}
	if i := strings.IndexByte(field, ':'); i > 0 {
		key := strings.ToLower(field[:i])
		val := field[i+1:]
		switch key {
		case "method", "m":
			tok.kind, tok.value = "method", strings.ToUpper(val)
		case "status", "s", "code":
			tok.kind, tok.value = "status", strings.ToLower(val)
			tok.status = statusPredicate(val)
		case "host", "h":
			tok.kind, tok.value = "host", strings.ToLower(val)
		case "url", "u":
			tok.kind, tok.value = "url", strings.ToLower(val)
		case "body", "b":
			tok.kind, tok.value = "body", strings.ToLower(val)
		case "tag", "t":
			tok.kind, tok.value = "tag", strings.ToLower(val)
		case "scheme":
			tok.kind, tok.value = "scheme", strings.ToLower(val)
		default:
			tok.kind, tok.value = "url", strings.ToLower(field)
		}
		return tok
	}
	tok.kind, tok.value = "url", strings.ToLower(field)
	return tok
}

func statusPredicate(val string) func(int) bool {
	v := strings.ToLower(strings.TrimSpace(val))
	switch {
	case strings.HasSuffix(v, "xx"):
		if d, err := strconv.Atoi(v[:1]); err == nil {
			lo, hi := d*100, d*100+99
			return func(code int) bool { return code >= lo && code <= hi }
		}
	case strings.HasPrefix(v, ">="):
		if n, err := strconv.Atoi(v[2:]); err == nil {
			return func(code int) bool { return code >= n }
		}
	case strings.HasPrefix(v, "<="):
		if n, err := strconv.Atoi(v[2:]); err == nil {
			return func(code int) bool { return code <= n }
		}
	case strings.HasPrefix(v, ">"):
		if n, err := strconv.Atoi(v[1:]); err == nil {
			return func(code int) bool { return code > n }
		}
	case strings.HasPrefix(v, "<"):
		if n, err := strconv.Atoi(v[1:]); err == nil {
			return func(code int) bool { return code < n }
		}
	}
	if n, err := strconv.Atoi(v); err == nil {
		return func(code int) bool { return code == n }
	}
	return func(code int) bool { return false }
}

// Empty reports whether the query matches everything.
func (q *Query) Empty() bool { return q == nil || q.root == nil }

// Err returns the most recent parse problem, if any.
func (q *Query) Err() error {
	if q == nil {
		return nil
	}
	return q.err
}

// Match reports whether a flow satisfies the expression.
func (q *Query) Match(f *Flow) bool {
	if q == nil || q.root == nil {
		return true
	}
	return q.root.match(f)
}

func (t qToken) matchFlow(f *Flow) bool {
	ok := t.matchPositive(f)
	if t.neg {
		return !ok
	}
	return ok
}

func (t qToken) matchPositive(f *Flow) bool {
	switch t.kind {
	case "regex":
		if t.re == nil {
			return false
		}
		return t.re.MatchString(f.URL)
	case "method":
		for _, m := range strings.Split(t.value, ",") {
			if strings.TrimSpace(m) == f.Method {
				return true
			}
		}
		return false
	case "status":
		if f.Status == 0 {
			// Only an explicit "status:0" / "status:err" selects flows that
			// never produced a response.
			return t.value == "0" || t.value == "err" || t.value == "error"
		}
		return t.status(f.Status)
	case "host":
		return strings.Contains(strings.ToLower(f.Host), t.value)
	case "url":
		return strings.Contains(strings.ToLower(f.URL), t.value)
	case "body":
		return strings.Contains(strings.ToLower(string(f.ReqBody)), t.value) ||
			strings.Contains(strings.ToLower(string(f.RespBody)), t.value)
	case "tag":
		for _, tag := range f.Tags {
			if strings.Contains(strings.ToLower(tag), t.value) {
				return true
			}
		}
		return false
	case "scheme":
		return strings.EqualFold(f.Scheme, t.value)
	}
	return true
}
