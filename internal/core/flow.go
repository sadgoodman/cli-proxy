// Package core holds the transport independent domain model shared by the
// proxy engine and the terminal UI: captured flows, the flow store, the
// rewrite/mock rule engine and the breakpoint queue.
package core

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// State describes where a flow is in its lifecycle.
type State int

const (
	StatePending State = iota
	StateComplete
	StateError
	StateIntercepted
	StateBlocked
)

func (s State) String() string {
	switch s {
	case StatePending:
		return "pending"
	case StateComplete:
		return "done"
	case StateError:
		return "error"
	case StateIntercepted:
		return "break"
	case StateBlocked:
		return "block"
	}
	return "?"
}

// Header is an ordered header pair used for display and editing. Go's
// http.Header is a map, so we keep our own ordered view for fidelity.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Flow is a single captured request/response exchange.
type Flow struct {
	ID     int64
	Start  time.Time
	End    time.Time
	Scheme string
	Method string
	URL    string
	Host   string
	Path   string
	Proto  string
	Client string

	ReqHeaders []Header
	ReqBody    []byte
	ReqTrunc   bool

	Status      int
	Reason      string
	RespHeaders []Header
	RespBody    []byte
	RespTrunc   bool

	Duration time.Duration
	BytesIn  int64
	BytesOut int64

	State       State
	Err         string
	Tags        []string
	Redirected  string
	Intercepted string // request | response while paused at a breakpoint
}

// Clone returns a deep copy safe to hand to the UI.
func (f *Flow) Clone() *Flow {
	if f == nil {
		return nil
	}
	c := *f
	c.ReqHeaders = append([]Header(nil), f.ReqHeaders...)
	c.RespHeaders = append([]Header(nil), f.RespHeaders...)
	c.ReqBody = append([]byte(nil), f.ReqBody...)
	c.RespBody = append([]byte(nil), f.RespBody...)
	c.Tags = append([]string(nil), f.Tags...)
	return &c
}

// AddTag records a processing marker once.
func (f *Flow) AddTag(tag string) {
	for _, t := range f.Tags {
		if t == tag {
			return
		}
	}
	f.Tags = append(f.Tags, tag)
}

// HasTag reports whether the flow carries the given marker.
func (f *Flow) HasTag(tag string) bool {
	for _, t := range f.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// StatusClass returns a compact status bucket such as "2xx" or "err".
func (f *Flow) StatusClass() string {
	if f.Status == 0 {
		if f.Err != "" {
			return "err"
		}
		return "..."
	}
	return fmt.Sprintf("%dxx", f.Status/100)
}

// StatusText renders the status code for tables.
func (f *Flow) StatusText() string {
	if f.Status == 0 {
		return "---"
	}
	return fmt.Sprintf("%d", f.Status)
}

// Size renders the captured payload size.
func (f *Flow) Size() int64 { return f.BytesIn + f.BytesOut }

// URLShort trims the scheme for table display.
func (f *Flow) URLShort() string {
	u := f.URL
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimPrefix(u, "https://")
	if i := strings.IndexByte(u, '/'); i >= 0 {
		return u[i:]
	}
	return "/"
}

// HeaderValue returns the first value for name (case-insensitive).
func (f *Flow) HeaderValue(list []Header, name string) string {
	for _, h := range list {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// RequestHeader returns the first request header value for name.
func (f *Flow) RequestHeader(name string) string { return f.HeaderValue(f.ReqHeaders, name) }

// ResponseHeader returns the first response header value for name.
func (f *Flow) ResponseHeader(name string) string { return f.HeaderValue(f.RespHeaders, name) }

// HeadersToMap converts an ordered header list into an http.Header.
func HeadersToMap(list []Header) http.Header {
	h := make(http.Header, len(list))
	for _, kv := range list {
		if kv.Name == "" {
			continue
		}
		h.Add(kv.Name, kv.Value)
	}
	return h
}

// MapToHeaders converts an http.Header into a deterministic, ordered list.
func MapToHeaders(h http.Header) []Header {
	if len(h) == 0 {
		return nil
	}
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]Header, 0, len(h))
	for _, n := range names {
		for _, v := range h[n] {
			out = append(out, Header{Name: n, Value: v})
		}
	}
	return out
}

// RawRequest renders the request as an editable HTTP/1.1 message using LF
// line endings so it can be edited in a terminal.
func (f *Flow) RawRequest() string {
	var b strings.Builder
	path := f.Path
	if path == "" {
		path = "/"
	}
	fmt.Fprintf(&b, "%s %s HTTP/1.1\n", f.Method, path)
	if f.Host != "" {
		fmt.Fprintf(&b, "Host: %s\n", f.Host)
	}
	for _, h := range f.ReqHeaders {
		if strings.EqualFold(h.Name, "Host") {
			continue
		}
		if h.Value == "" {
			fmt.Fprintf(&b, "%s:\n", h.Name)
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", h.Name, h.Value)
	}
	b.WriteString("\n")
	b.Write(f.ReqBody)
	return b.String()
}

// RawResponse renders the response as an editable HTTP/1.1 message.
func (f *Flow) RawResponse() string {
	var b strings.Builder
	reason := f.Reason
	if reason == "" {
		reason = http.StatusText(f.Status)
	}
	status := f.Status
	if status == 0 {
		status = 200
	}
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\n", status, reason)
	for _, h := range f.RespHeaders {
		if h.Value == "" {
			fmt.Fprintf(&b, "%s:\n", h.Name)
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", h.Name, h.Value)
	}
	b.WriteString("\n")
	b.Write(f.RespBody)
	return b.String()
}

// FormatBytes renders a byte count compactly.
func FormatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fK", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1fM", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1fG", float64(n)/(1024*1024*1024))
	}
}

// FormatDuration renders a duration compactly.
func FormatDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "-"
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}
