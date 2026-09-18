package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"cliproxy/internal/ca"
	"cliproxy/internal/core"
)

type harness struct {
	t        *testing.T
	px       *Proxy
	store    *core.Store
	rules    *core.RuleSet
	brk      *core.Breaker
	opts     *core.Options
	origin   *httptest.Server
	secure   *httptest.Server
	plain    *http.Client
	proxyURL *url.URL
}

func newHarness(t *testing.T, configure func(cfg *Config)) *harness {
	t.Helper()

	origin := httptest.NewServer(http.HandlerFunc(echoHandler))
	secure := httptest.NewTLSServer(http.HandlerFunc(echoHandler))

	authority, err := ca.Load(t.TempDir())
	if err != nil {
		t.Fatalf("ca.Load: %v", err)
	}
	store := core.NewStore(500)
	ruleset := core.NewRuleSet("")
	brk := core.NewBreaker()
	opts := core.DefaultOptions()
	logger := core.NewLogger(100)

	cfg := Config{
		Addr:             "127.0.0.1:0",
		CA:               authority,
		Store:            store,
		Rules:            ruleset,
		Breaker:          brk,
		Opts:             opts,
		Log:              logger,
		Version:          "test",
		InsecureUpstream: true, // httptest TLS origins use self-signed certificates
	}
	if configure != nil {
		configure(&cfg)
	}
	px := New(cfg)
	if err := px.Start(); err != nil {
		t.Fatalf("proxy start: %v", err)
	}
	t.Cleanup(func() {
		_ = px.Close()
		origin.Close()
		secure.Close()
	})

	pxURL, err := url.Parse("http://" + px.Addr())
	if err != nil {
		t.Fatalf("parse proxy url: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(authority.Cert)

	h := &harness{
		t: t, px: px, store: store, rules: ruleset, brk: brk, opts: opts,
		origin: origin, secure: secure, proxyURL: pxURL,
	}
	h.plain = &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(pxURL),
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
	}
	return h
}

func echoHandler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("X-Origin", "yes")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"method":  r.Method,
		"path":    r.URL.Path,
		"query":   r.URL.RawQuery,
		"headers": r.Header,
		"body":    string(body),
		"host":    r.Host,
	})
}

func (h *harness) get(target string) (*http.Response, map[string]any) {
	h.t.Helper()
	resp, err := h.plain.Get(target)
	if err != nil {
		h.t.Fatalf("GET %s: %v", target, err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	return resp, payload
}

func (h *harness) waitFlows(n int) []*core.Flow {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		flows := h.store.Snapshot()
		done := 0
		for _, f := range flows {
			if f.State == core.StateComplete || f.State == core.StateError || f.State == core.StateBlocked {
				done++
			}
		}
		if done >= n {
			return flows
		}
		time.Sleep(10 * time.Millisecond)
	}
	return h.store.Snapshot()
}

func addRule(t *testing.T, rs *core.RuleSet, dsl string) *core.Rule {
	t.Helper()
	r, err := core.ParseRule(dsl)
	if err != nil {
		t.Fatalf("ParseRule(%q): %v", dsl, err)
	}
	return rs.Add(r)
}

func TestPlainHTTPCapture(t *testing.T) {
	h := newHarness(t, nil)

	resp, payload := h.get(h.origin.URL + "/hello?a=1")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if payload["path"] != "/hello" {
		t.Fatalf("origin path = %v", payload["path"])
	}

	flows := h.waitFlows(1)
	var flow *core.Flow
	for _, f := range flows {
		if strings.HasSuffix(f.Path, "/hello?a=1") {
			flow = f
		}
	}
	if flow == nil {
		t.Fatalf("flow not captured, got %d flows", len(flows))
	}
	if flow.Method != "GET" || flow.Scheme != "http" {
		t.Fatalf("unexpected flow: %+v", flow)
	}
	if flow.Status != 200 {
		t.Fatalf("captured status = %d", flow.Status)
	}
	if flow.RequestHeader("Host") == "" {
		t.Fatal("Host header not captured")
	}
	if flow.ResponseHeader("X-Origin") != "yes" {
		t.Fatalf("response header not captured: %+v", flow.RespHeaders)
	}
}

func TestPostBodyCapture(t *testing.T) {
	h := newHarness(t, nil)
	resp, err := h.plain.Post(h.origin.URL+"/submit", "application/json", strings.NewReader(`{"n":42}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `{\"n\":42}`) && !strings.Contains(string(body), `{"n":42}`) {
		t.Fatalf("origin did not receive the body: %s", body)
	}
	flows := h.waitFlows(1)
	found := false
	for _, f := range flows {
		if f.Method == "POST" && string(f.ReqBody) == `{"n":42}` {
			found = true
		}
	}
	if !found {
		t.Fatalf("POST body not captured: %+v", flows)
	}
}

func TestHTTPSInterception(t *testing.T) {
	h := newHarness(t, nil)
	resp, payload := h.get(h.secure.URL + "/secure")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if payload["path"] != "/secure" {
		t.Fatalf("origin path = %v", payload["path"])
	}
	flows := h.waitFlows(1)
	var flow *core.Flow
	for _, f := range flows {
		if f.Scheme == "https" && strings.HasSuffix(f.Path, "/secure") {
			flow = f
		}
	}
	if flow == nil {
		t.Fatalf("https flow not captured: %+v", flows)
	}
	if flow.Status != 200 {
		t.Fatalf("status = %d", flow.Status)
	}
}

func TestMockFileRule(t *testing.T) {
	h := newHarness(t, nil)
	dir := t.TempDir()
	path := dir + "/mock.json"
	if err := os.WriteFile(path, []byte(`{"mocked":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	addRule(t, h.rules, `GET ^`+regexpQuote(h.origin.URL)+`/mocked$ :: file=`+path)

	resp, err := h.plain.Get(h.origin.URL + "/mocked")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(body)) != `{"mocked":true}` {
		t.Fatalf("body = %q", body)
	}
	if resp.Header.Get("X-Cli-Proxy") != "mock" {
		t.Fatalf("missing mock marker: %v", resp.Header)
	}
	flows := h.waitFlows(1)
	if !flows[len(flows)-1].HasTag("mock") {
		t.Fatalf("flow not tagged as mock: %+v", flows[len(flows)-1].Tags)
	}
}

func TestRedirectRule(t *testing.T) {
	h := newHarness(t, nil)
	addRule(t, h.rules, `GET ^`+regexpQuote(h.origin.URL)+`/from-other$ :: redirect=`+h.origin.URL+`/other-source`)

	_, payload := h.get(h.origin.URL + "/from-other")
	if payload["path"] != "/other-source" {
		t.Fatalf("expected the substituted path, got %v", payload["path"])
	}
	flows := h.waitFlows(1)
	last := flows[len(flows)-1]
	if !last.HasTag("redirect") || last.Redirected == "" {
		t.Fatalf("redirect not recorded: %+v", last)
	}
}

func TestLocationRule(t *testing.T) {
	h := newHarness(t, nil)
	addRule(t, h.rules, `GET ^`+regexpQuote(h.origin.URL)+`/moved$ :: location=https://example.com/new`)

	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(h.proxyURL)},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(h.origin.URL + "/moved")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "https://example.com/new" {
		t.Fatalf("location = %q", got)
	}
}

func TestStatusAndBodyOverride(t *testing.T) {
	h := newHarness(t, nil)
	addRule(t, h.rules, `GET ^`+regexpQuote(h.origin.URL)+`/broken$ :: status=503 :: body={"maintenance":true}`)

	resp, err := h.plain.Get(h.origin.URL + "/broken")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 503 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if strings.TrimSpace(string(body)) != `{"maintenance":true}` {
		t.Fatalf("body = %q", body)
	}
}

func TestBlockRule(t *testing.T) {
	h := newHarness(t, nil)
	addRule(t, h.rules, `GET ^`+regexpQuote(h.origin.URL)+`/forbidden$ :: block=403`)

	resp, err := h.plain.Get(h.origin.URL + "/forbidden")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	flows := h.waitFlows(1)
	last := flows[len(flows)-1]
	if last.State != core.StateBlocked {
		t.Fatalf("state = %v", last.State)
	}
}

func TestHeaderRewrite(t *testing.T) {
	h := newHarness(t, nil)
	addRule(t, h.rules, `GET ^`+regexpQuote(h.origin.URL)+`/rewrite$ :: header=X-Injected=cli-proxy :: delheader=X-Remove-Me :: resheader=X-Proxy-Env=test`)

	req, _ := http.NewRequest(http.MethodGet, h.origin.URL+"/rewrite", nil)
	req.Header.Set("X-Remove-Me", "please")
	resp, err := h.plain.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)

	hdrs, _ := payload["headers"].(map[string]any)
	if got := headerGet(hdrs, "X-Injected"); got != "cli-proxy" {
		t.Fatalf("request header not injected: %v", hdrs)
	}
	if got := headerGet(hdrs, "X-Remove-Me"); got != "" {
		t.Fatalf("request header not removed: %v", got)
	}
	if resp.Header.Get("X-Proxy-Env") != "test" {
		t.Fatalf("response header not rewritten: %v", resp.Header)
	}
}

func TestDelayRule(t *testing.T) {
	h := newHarness(t, nil)
	addRule(t, h.rules, `GET ^`+regexpQuote(h.origin.URL)+`/slow$ :: delay=250ms`)
	start := time.Now()
	resp, err := h.plain.Get(h.origin.URL + "/slow")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("delay not applied, took %s", elapsed)
	}
}

func TestBreakpointRequestEditing(t *testing.T) {
	h := newHarness(t, nil)
	addRule(t, h.rules, `GET ^`+regexpQuote(h.origin.URL)+`/intercept$ :: break=req`)

	type result struct {
		payload map[string]any
	}
	done := make(chan result, 1)
	go func() {
		_, payload := h.get(h.origin.URL + "/intercept")
		done <- result{payload}
	}()

	bp := h.awaitBreakpoint()
	if bp.Phase != core.PhaseRequest {
		t.Fatalf("phase = %v", bp.Phase)
	}
	edited := strings.Replace(bp.Raw, "GET /intercept HTTP/1.1", "POST /edited-by-breakpoint HTTP/1.1", 1)
	edited = strings.Replace(edited, "Host: ", "X-Breakpoint: yes\nHost: ", 1)
	if !h.brk.Submit(bp.ID, core.Verdict{Raw: edited}) {
		t.Fatal("submit failed")
	}

	select {
	case res := <-done:
		if res.payload["path"] != "/edited-by-breakpoint" {
			t.Fatalf("origin saw path %v", res.payload["path"])
		}
		if res.payload["method"] != "POST" {
			t.Fatalf("origin saw method %v", res.payload["method"])
		}
		hdrs, _ := res.payload["headers"].(map[string]any)
		if headerGet(hdrs, "X-Breakpoint") != "yes" {
			t.Fatalf("edited header missing: %v", hdrs)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("request never completed")
	}
}

func TestBreakpointResponseEditing(t *testing.T) {
	h := newHarness(t, nil)
	addRule(t, h.rules, `GET ^`+regexpQuote(h.origin.URL)+`/resp-break$ :: break=resp`)

	type result struct {
		status int
		body   string
		hdr    string
	}
	done := make(chan result, 1)
	go func() {
		resp, err := h.plain.Get(h.origin.URL + "/resp-break")
		if err != nil {
			done <- result{status: -1, body: err.Error()}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		done <- result{status: resp.StatusCode, body: string(b), hdr: resp.Header.Get("X-Edited")}
	}()

	bp := h.awaitBreakpoint()
	if bp.Phase != core.PhaseResponse {
		t.Fatalf("phase = %v", bp.Phase)
	}
	idx := strings.Index(bp.Raw, "\n\n")
	if idx < 0 {
		t.Fatalf("unexpected raw response: %q", bp.Raw)
	}
	head := strings.Replace(bp.Raw[:idx], "HTTP/1.1 200 OK", "HTTP/1.1 418 Teapot", 1)
	edited := head + "\nX-Edited: indeed\n\nrewritten-body"
	if !h.brk.Submit(bp.ID, core.Verdict{Raw: edited}) {
		t.Fatal("submit failed")
	}

	select {
	case res := <-done:
		if res.status != 418 {
			t.Fatalf("status = %d", res.status)
		}
		if res.hdr != "indeed" {
			t.Fatalf("header = %q", res.hdr)
		}
		if strings.TrimSpace(res.body) != "rewritten-body" {
			t.Fatalf("body = %q", res.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("response never completed")
	}
}

func TestBreakpointDrop(t *testing.T) {
	h := newHarness(t, nil)
	addRule(t, h.rules, `GET ^`+regexpQuote(h.origin.URL)+`/dropped$ :: break=req`)

	done := make(chan int, 1)
	go func() {
		resp, err := h.plain.Get(h.origin.URL + "/dropped")
		if err != nil {
			done <- -1
			return
		}
		defer resp.Body.Close()
		done <- resp.StatusCode
	}()

	bp := h.awaitBreakpoint()
	h.brk.Submit(bp.ID, core.Verdict{Drop: true})

	select {
	case status := <-done:
		if status != 502 {
			t.Fatalf("dropped request should answer 502, got %d", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("request never completed")
	}
}

func TestGlobalBreakpointToggles(t *testing.T) {
	h := newHarness(t, nil)
	h.opts.BreakRequests.Store(true)

	done := make(chan struct{}, 1)
	go func() {
		resp, err := h.plain.Get(h.origin.URL + "/anything")
		if err == nil {
			resp.Body.Close()
		}
		done <- struct{}{}
	}()
	bp := h.awaitBreakpoint()
	if bp.Flow.Method != "GET" {
		t.Fatalf("unexpected flow: %+v", bp.Flow)
	}
	h.brk.Submit(bp.ID, core.Verdict{})
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("request never completed")
	}
}

func TestTunnelMode(t *testing.T) {
	h := newHarness(t, func(cfg *Config) { cfg.TunnelOnly = true })
	// Without interception the client sees the origin certificate itself, so
	// it must not expect our CA to have signed anything.
	tunnelClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(h.proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := tunnelClient.Get(h.secure.URL + "/tunnelled")
	if err != nil {
		t.Fatalf("GET through tunnel: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	flows := h.waitFlows(1)
	found := false
	for _, f := range flows {
		if f.Method == "CONNECT" && f.HasTag("tunnel") {
			found = true
		}
	}
	if !found {
		t.Fatalf("tunnel flow not recorded: %+v", flows)
	}
}

func TestLocalCertificateEndpoint(t *testing.T) {
	h := newHarness(t, nil)
	// A direct request to the proxy port (not through it) serves the CA.
	resp, err := http.Get("http://" + h.px.Addr() + "/cert")
	if err != nil {
		t.Fatalf("GET /cert: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "BEGIN CERTIFICATE") {
		t.Fatalf("certificate not served: %q", body[:min(80, len(body))])
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-x509-ca-cert" {
		t.Fatalf("content-type = %q", ct)
	}

	page, err := http.Get("http://" + h.px.Addr() + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer page.Body.Close()
	html, _ := io.ReadAll(page.Body)
	if !strings.Contains(string(html), "install") && !strings.Contains(string(html), "Install") {
		t.Fatalf("index page missing install instructions")
	}
}

func TestLeafCertificateHasSAN(t *testing.T) {
	h := newHarness(t, nil)
	leaf, err := h.px.CA().Leaf("example.test")
	if err != nil {
		t.Fatalf("Leaf: %v", err)
	}
	if leaf.Leaf == nil {
		t.Fatal("leaf not parsed")
	}
	if len(leaf.Leaf.DNSNames) == 0 || leaf.Leaf.DNSNames[0] != "example.test" {
		t.Fatalf("DNS SAN missing: %+v", leaf.Leaf.DNSNames)
	}
	if !leaf.Leaf.NotAfter.Before(time.Now().Add(399 * 24 * time.Hour)) {
		t.Fatalf("leaf validity %s exceeds the Apple 398 day limit", leaf.Leaf.NotAfter)
	}
	// Certificates must chain to our CA.
	if err := leaf.Leaf.CheckSignatureFrom(h.px.CA().Cert); err != nil {
		t.Fatalf("leaf not signed by the CA: %v", err)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (h *harness) awaitBreakpoint() *core.Breakpoint {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pending := h.brk.Pending(); len(pending) > 0 {
			return pending[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatal("no breakpoint was raised")
	return nil
}

func headerGet(h map[string]any, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			switch vv := v.(type) {
			case string:
				return vv
			case []any:
				if len(vv) > 0 {
					s, _ := vv[0].(string)
					return s
				}
			}
		}
	}
	return ""
}

func regexpQuote(s string) string { return regexp.QuoteMeta(s) }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestRebindMovesTheListenerAndKeepsCaptures(t *testing.T) {
	h := newHarness(t, nil)
	if _, payload := h.get(h.origin.URL + "/before"); payload["path"] != "/before" {
		t.Fatalf("initial request failed: %v", payload)
	}
	h.waitFlows(1)
	before := h.store.Len()
	oldAddr := h.px.Addr()
	if h.px.Port() == 0 {
		t.Fatalf("Port() = 0 for %q", oldAddr)
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	newAddr := probe.Addr().String()
	_ = probe.Close()

	if err := h.px.Rebind(newAddr); err != nil {
		t.Fatalf("Rebind: %v", err)
	}
	if got := h.px.Addr(); got != newAddr {
		t.Fatalf("Addr() = %q, want %q", got, newAddr)
	}

	// The new address serves traffic and keeps the captured history.
	pxURL, err := url.Parse("http://" + newAddr)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(pxURL),
			TLSClientConfig: &tls.Config{RootCAs: poolWith(h.px)},
		},
	}
	resp, err := client.Get(h.origin.URL + "/after")
	if err != nil {
		t.Fatalf("GET via the new address: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "/after") {
		t.Fatalf("new address did not proxy correctly: %d %s", resp.StatusCode, body)
	}
	if h.store.Len() <= before {
		t.Fatalf("captured flows were lost across the rebind: %d -> %d", before, h.store.Len())
	}

	// The old address stops accepting new connections.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, derr := net.DialTimeout("tcp", oldAddr, 200*time.Millisecond)
		if derr != nil {
			return
		}
		_ = c.Close()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("old address %s still accepts connections after a rebind", oldAddr)
}

func TestRebindFailureLeavesTheProxyRunning(t *testing.T) {
	h := newHarness(t, nil)
	oldAddr := h.px.Addr()

	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()

	if err := h.px.Rebind(blocker.Addr().String()); err == nil {
		t.Fatal("rebinding onto an occupied address should fail")
	}
	if got := h.px.Addr(); got != oldAddr {
		t.Fatalf("a failed rebind moved the listener: %s -> %s", oldAddr, got)
	}
	if _, payload := h.get(h.origin.URL + "/still-alive"); payload["path"] != "/still-alive" {
		t.Fatalf("proxy stopped serving after a failed rebind: %v", payload)
	}
}

func poolWith(p *Proxy) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(p.CA().Cert)
	return pool
}

func TestDisplayAddrCollapsesWildcard(t *testing.T) {
	h := newHarness(t, nil)
	// The harness binds 127.0.0.1:0, so the concrete address must be preserved.
	if got := h.px.DisplayAddr(); !strings.HasPrefix(got, "127.0.0.1:") {
		t.Fatalf("DisplayAddr() = %q, want the concrete address", got)
	}

	_, port, err := net.SplitHostPort(h.px.Addr())
	if err != nil {
		t.Fatal(err)
	}
	for _, wildcard := range []string{"0.0.0.0", "::", ""} {
		if err := h.px.Rebind(net.JoinHostPort(wildcard, port)); err != nil {
			// "" needs a bare port; retry with the colon form.
			if err := h.px.Rebind(":" + port); err != nil {
				t.Fatalf("rebind to wildcard %q: %v", wildcard, err)
			}
		}
		if got, want := h.px.DisplayAddr(), "*:"+port; got != want {
			t.Fatalf("DisplayAddr() for %q = %q, want %q", wildcard, got, want)
		}
		if err := h.px.Rebind("127.0.0.1:" + port); err != nil {
			t.Fatalf("rebind back: %v", err)
		}
	}
}

func TestSanitizeAcceptEncoding(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"gzip, deflate, br":       "gzip, deflate",
		"gzip, deflate, br, zstd": "gzip, deflate",
		"gzip":                    "gzip",
		"identity":                "identity",
		"deflate, br":             "deflate",
		"gzip;q=1.0, br;q=0.8":    "gzip;q=1.0",
		"*":                       "gzip, deflate",
		"br, *":                   "gzip, deflate",
		"br":                      "br",
		"zstd":                    "zstd",
		"  gzip ,  br ":           "gzip",
	}
	for in, want := range cases {
		if got := sanitizeAcceptEncoding(in); got != want {
			t.Errorf("sanitizeAcceptEncoding(%q) = %q, want %q", in, got, want)
		}
	}
}

// A phone sending "gzip, deflate, br" must not be served brotli, otherwise the
// captured body is unreadable. The captured headers still show what the client
// actually sent.
func TestUndecodableEncodingIsNotRequested(t *testing.T) {
	h := newHarness(t, nil)
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:              http.ProxyURL(h.proxyURL),
			DisableCompression: true,
		},
	}
	req, _ := http.NewRequest(http.MethodGet, h.origin.URL+"/compressed", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	hdrs, _ := payload["headers"].(map[string]any)
	if got := headerGet(hdrs, "Accept-Encoding"); got != "gzip, deflate" {
		t.Fatalf("origin saw Accept-Encoding %q, want %q", got, "gzip, deflate")
	}

	flows := h.waitFlows(1)
	var flow *core.Flow
	for _, f := range flows {
		if strings.HasSuffix(f.Path, "/compressed") {
			flow = f
		}
	}
	if flow == nil {
		t.Fatal("flow not captured")
	}
	if got := flow.RequestHeader("Accept-Encoding"); got != "gzip, deflate, br" {
		t.Fatalf("captured request should show what the client sent, got %q", got)
	}
}

func TestKeepEncodingLeavesTheHeaderAlone(t *testing.T) {
	h := newHarness(t, func(cfg *Config) { cfg.KeepEncoding = true })
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:              http.ProxyURL(h.proxyURL),
			DisableCompression: true,
		},
	}
	req, _ := http.NewRequest(http.MethodGet, h.origin.URL+"/raw", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	hdrs, _ := payload["headers"].(map[string]any)
	if got := headerGet(hdrs, "Accept-Encoding"); got != "gzip, deflate, br" {
		t.Fatalf("-keep-encoding should pass the header through, origin saw %q", got)
	}
}

func TestShortCertificateHost(t *testing.T) {
	h := newHarness(t, nil)
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(h.proxyURL)},
	}

	urls := []string{
		"http://cli.proxy/ssl",
		"http://cli.proxy/cert",
		"http://cli.proxy/cli-proxy-ca.crt",
		"http://cliproxy.local/cert",
		"http://proxy.man/ssl",
	}
	for _, u := range urls {
		resp, err := client.Get(u)
		if err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("GET %s: status %d", u, resp.StatusCode)
			continue
		}
		if resp.Header.Get("Content-Type") != "application/x-x509-ca-cert" {
			t.Errorf("GET %s: content-type %q", u, resp.Header.Get("Content-Type"))
		}
		if !strings.Contains(string(body), "BEGIN CERTIFICATE") {
			t.Errorf("GET %s: body is not a certificate: %q", u, body[:min(60, len(body))])
		}
	}

	page, err := client.Get("http://cli.proxy/")
	if err != nil {
		t.Fatalf("GET index: %v", err)
	}
	defer page.Body.Close()
	html, _ := io.ReadAll(page.Body)
	if !strings.Contains(string(html), "cli.proxy") {
		t.Fatal("index page does not advertise the short URL")
	}
}

// https://cli.proxy cannot work before the root is trusted, so answer with a
// readable explanation instead of a baffling handshake failure.
func TestShortHostOverHTTPSExplainsItself(t *testing.T) {
	h := newHarness(t, nil)
	conn, err := net.DialTimeout("tcp", h.px.Addr(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("CONNECT cli.proxy:443 HTTP/1.1\r\nHost: cli.proxy:443\r\n\r\n"))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1024)
	n, _ := conn.Read(buf)
	got := string(buf[:n])
	if !strings.Contains(got, "409") {
		t.Fatalf("expected a 409 explanation, got %q", got)
	}
	if !strings.Contains(got, "http://cli.proxy/ssl") {
		t.Fatalf("explanation should point at the plain HTTP URL, got %q", got)
	}
}

func TestCertStepsMentionsShortURLAndLocalSetup(t *testing.T) {
	h := newHarness(t, nil)
	steps := h.px.CertSteps()
	for _, want := range []string{
		"http://cli.proxy", "-o cli-proxy-ca.crt", "HTTP_PROXY", "127.0.0.1",
		"bypass", "certutil", "security add-trusted-cert",
	} {
		if !strings.Contains(steps, want) {
			t.Errorf("certificate guide is missing %q", want)
		}
	}
	// The old curl -O advice saved the file under the wrong name.
	if strings.Contains(steps, "curl -O") {
		t.Error("guide still suggests curl -O, which names the file after the URL path")
	}
}
