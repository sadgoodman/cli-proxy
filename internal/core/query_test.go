package core

import (
	"testing"
	"time"
)

func flowForFilter() []*Flow {
	return []*Flow{
		{ID: 1, Method: "GET", URL: "https://api.example.com/v1/users", Host: "api.example.com",
			Path: "/v1/users", Scheme: "https", Status: 200, RespBody: []byte(`{"users":[]}`)},
		{ID: 2, Method: "POST", URL: "https://api.example.com/v1/users", Host: "api.example.com",
			Path: "/v1/users", Scheme: "https", Status: 500, ReqBody: []byte(`{"name":"bob"}`)},
		{ID: 3, Method: "GET", URL: "http://cdn.example.net/logo.png", Host: "cdn.example.net",
			Path: "/logo.png", Scheme: "http", Status: 404, Tags: []string{"mock"}},
		{ID: 4, Method: "GET", URL: "https://api.example.com/health", Host: "api.example.com",
			Path: "/health", Scheme: "https", Status: 0, Err: "dial tcp: refused"},
	}
}

func TestQueryMatching(t *testing.T) {
	flows := flowForFilter()
	cases := []struct {
		query string
		want  []int64
	}{
		{"", []int64{1, 2, 3, 4}},
		{"users", []int64{1, 2}},
		{"logo", []int64{3}},
		{"method:GET", []int64{1, 3, 4}},
		{"method:get,post", []int64{1, 2, 3, 4}},
		{"status:2xx", []int64{1}},
		{"status:500", []int64{2}},
		{"status:>=400", []int64{2, 3}},
		{"status:<300", []int64{1}},
		{"host:cdn", []int64{3}},
		{"scheme:http", []int64{3}},
		{"body:bob", []int64{2}},
		{"body:users", []int64{1}},
		{"tag:mock", []int64{3}},
		{"method:GET status:2xx", []int64{1}},
		{"method:GET !host:cdn", []int64{1, 4}},
		{"/users$/", []int64{1, 2}},
		{"!api.example.com", []int64{3}},
	}
	for _, tc := range cases {
		q := CompileQuery(tc.query)
		var got []int64
		for _, f := range flows {
			if q.Match(f) {
				got = append(got, f.ID)
			}
		}
		if len(got) != len(tc.want) {
			t.Errorf("query %q matched %v, want %v", tc.query, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("query %q matched %v, want %v", tc.query, got, tc.want)
				break
			}
		}
	}
}

func TestQueryEmptyAndInvalid(t *testing.T) {
	if !CompileQuery("").Empty() {
		t.Fatal("empty query should be empty")
	}
	if CompileQuery("api").Empty() {
		t.Fatal("non-empty query reported empty")
	}
	// An invalid regex degrades to a substring match rather than failing.
	q := CompileQuery("/[unclosed/")
	if q.Err() == nil {
		t.Fatal("expected a parse error to be reported")
	}
	if !q.Match(&Flow{URL: "[unclosed"}) {
		t.Fatal("invalid regex should fall back to substring matching")
	}
}

func TestStoreRingAndNotify(t *testing.T) {
	s := NewStore(3)
	ch, unsub := s.Subscribe()
	defer unsub()

	for i := 0; i < 5; i++ {
		s.Add(&Flow{Method: "GET", URL: "http://x/"})
	}
	if s.Len() != 3 {
		t.Fatalf("len = %d, want the ring to hold 3", s.Len())
	}
	snap := s.Snapshot()
	if snap[0].ID != 3 || snap[2].ID != 5 {
		t.Fatalf("ring kept the wrong flows: %d..%d", snap[0].ID, snap[2].ID)
	}
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("subscriber was not notified")
	}

	s.Update(snap[0].ID, func(f *Flow) { f.Status = 204 })
	if got, ok := s.Get(snap[0].ID); !ok || got.Status != 204 {
		t.Fatalf("update did not apply: %+v", got)
	}
	s.Clear()
	if s.Len() != 0 {
		t.Fatal("clear failed")
	}
}

func TestFlowCloneIsDeep(t *testing.T) {
	f := &Flow{
		ReqHeaders: []Header{{Name: "A", Value: "1"}},
		RespBody:   []byte("hello"),
		Tags:       []string{"x"},
	}
	c := f.Clone()
	c.ReqHeaders[0].Value = "2"
	c.RespBody[0] = 'H'
	c.Tags[0] = "y"
	if f.ReqHeaders[0].Value != "1" || f.RespBody[0] != 'h' || f.Tags[0] != "x" {
		t.Fatal("Clone is not deep")
	}
}

func TestRawMessages(t *testing.T) {
	f := &Flow{
		Method:      "POST",
		Path:        "/api?x=1",
		Host:        "example.com",
		ReqHeaders:  []Header{{Name: "Content-Type", Value: "application/json"}},
		ReqBody:     []byte(`{"a":1}`),
		Status:      201,
		Reason:      "Created",
		RespHeaders: []Header{{Name: "X-A", Value: "b"}},
		RespBody:    []byte("ok"),
	}
	req := f.RawRequest()
	for _, want := range []string{"POST /api?x=1 HTTP/1.1", "Host: example.com", "Content-Type: application/json", `{"a":1}`} {
		if !contains(req, want) {
			t.Errorf("RawRequest missing %q:\n%s", want, req)
		}
	}
	resp := f.RawResponse()
	for _, want := range []string{"HTTP/1.1 201 Created", "X-A: b", "ok"} {
		if !contains(resp, want) {
			t.Errorf("RawResponse missing %q:\n%s", want, resp)
		}
	}
}

func TestBreakpointQueue(t *testing.T) {
	b := NewBreaker()
	done := make(chan Verdict, 1)
	go func() {
		done <- b.Hold(&Breakpoint{Phase: PhaseRequest, Raw: "x"})
	}()
	deadline := time.Now().Add(2 * time.Second)
	var bp *Breakpoint
	for time.Now().Before(deadline) {
		if p := b.Pending(); len(p) > 0 {
			bp = p[0]
			break
		}
		time.Sleep(time.Millisecond)
	}
	if bp == nil {
		t.Fatal("breakpoint never queued")
	}
	if !b.Submit(bp.ID, Verdict{Raw: "edited"}) {
		t.Fatal("submit failed")
	}
	select {
	case v := <-done:
		if v.Raw != "edited" {
			t.Fatalf("verdict = %+v", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Hold never returned")
	}
	if b.Len() != 0 {
		t.Fatal("queue not drained")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func TestQueryOrAndGrouping(t *testing.T) {
	flows := flowForFilter()
	cases := []struct {
		query string
		want  []int64
	}{
		{"users | logo", []int64{1, 2, 3}},
		{"users OR logo", []int64{1, 2, 3}},
		{"host:api.example.com | host:cdn.example.net", []int64{1, 2, 3, 4}},
		{"method:GET status:2xx | method:POST", []int64{1, 2}},
		{"(method:GET | method:POST) status:5xx", []int64{2}},
		{"((method:GET) | (method:POST)) status:>=400", []int64{2, 3}},
		{"!host:api.example.com | status:2xx", []int64{1, 3}},
		{"!(host:cdn.example.net)", []int64{1, 2, 4}},
		{"method:GET !(status:2xx | status:404)", []int64{4}},
		{"(logo | health) scheme:http", []int64{3}},
		{"method:GET method:GET", []int64{1, 3, 4}},
		{"/users$/ | /health$/", []int64{1, 2, 4}},
		{"status:404 | status:500 | status:0", []int64{2, 3, 4}},
		// A pipe inside a regex must not split the expression.
		{"/users|logo/", []int64{1, 2, 3}},
		// Trailing operator while typing must not blow up or hide everything.
		{"users |", []int64{1, 2, 3, 4}},
	}
	for _, tc := range cases {
		q := CompileQuery(tc.query)
		if q.Err() != nil {
			t.Errorf("query %q reported an error: %v", tc.query, q.Err())
		}
		var got []int64
		for _, f := range flows {
			if q.Match(f) {
				got = append(got, f.ID)
			}
		}
		if len(got) != len(tc.want) {
			t.Errorf("query %q matched %v, want %v", tc.query, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("query %q matched %v, want %v", tc.query, got, tc.want)
				break
			}
		}
	}
}

func TestQueryUnbalancedParenthesisIsReported(t *testing.T) {
	q := CompileQuery("(users | logo")
	if q.Err() == nil {
		t.Fatal("an unbalanced parenthesis should be reported")
	}
	// It must still filter something rather than hide the whole list.
	if !q.Match(&Flow{URL: "https://api.example.com/v1/users"}) {
		t.Fatal("a broken-but-usable query should still match")
	}
}
