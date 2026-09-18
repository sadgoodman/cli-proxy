// Package proxy implements the HTTP/HTTPS intercepting proxy: it terminates
// client connections, optionally man-in-the-middles TLS, applies rewrite and
// mock rules, honours breakpoints and records every exchange into a
// core.Store.
package proxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cliproxy/internal/ca"
	"cliproxy/internal/core"
)

// hopHeaders are stripped in both directions per RFC 7230.
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// Config configures a Proxy instance.
type Config struct {
	Addr       string
	TunnelOnly bool // never MITM, just pass CONNECT through
	MaxBody    int64
	CA         *ca.CA
	Store      *core.Store
	Rules      *core.RuleSet
	Breaker    *core.Breaker
	Opts       *core.Options
	Log        *core.Logger
	Version    string
	CAPath     string
	// InsecureUpstream disables verification of the origin certificate.
	InsecureUpstream bool
	// CABundle is an extra PEM file of roots trusted for upstream connections.
	CABundle string
	// KeepEncoding leaves Accept-Encoding untouched. By default the proxy
	// drops encodings it cannot decode (brotli, zstd) so captured bodies stay
	// readable, which is what a sniffer is for.
	KeepEncoding bool
}

// Proxy is the intercepting HTTP proxy server.
type Proxy struct {
	cfg       Config
	transport *http.Transport

	mu      sync.Mutex
	ln      net.Listener
	srv     *http.Server
	conns   map[net.Conn]struct{}
	started time.Time

	activeConns atomic.Int64
	totalConns  atomic.Int64
}

// New creates a proxy from cfg.
func New(cfg Config) *Proxy {
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = 8 << 20
	}
	if cfg.Log == nil {
		cfg.Log = core.NewLogger(400)
	}
	tlsClient := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.InsecureUpstream {
		tlsClient.InsecureSkipVerify = true
	}
	if cfg.CABundle != "" {
		if pool, err := loadCABundle(cfg.CABundle); err == nil {
			tlsClient.RootCAs = pool
		} else {
			cfg.Log.Addf("cannot load -ca-bundle %s: %v", cfg.CABundle, err)
		}
	}
	tr := &http.Transport{
		Proxy:           nil,
		TLSClientConfig: tlsClient,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		DisableCompression:    true,
	}
	return &Proxy{
		cfg:       cfg,
		transport: tr,
		conns:     make(map[net.Conn]struct{}),
	}
}

// Start binds the listener and begins serving in the background.
func (p *Proxy) Start() error {
	ln, err := net.Listen("tcp", p.cfg.Addr)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.ln = ln
	p.started = time.Now()
	p.mu.Unlock()

	p.serve(ln)
	p.cfg.Log.Addf("proxy listening on %s (mode=%s)", ln.Addr(), p.Mode())
	return nil
}

// serve installs a fresh http.Server on ln and starts accepting in the
// background.
func (p *Proxy) serve(ln net.Listener) {
	srv := &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 30 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	p.mu.Lock()
	p.srv = srv
	p.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			p.cfg.Log.Addf("proxy stopped: %v", err)
		}
	}()
}

// Rebind moves the proxy to a different listen address without losing the
// captured flows, rules or the certificate authority.
//
// The new address is bound first, so a failure (for example "address already
// in use") leaves the running listener untouched. Connections that are already
// open keep working until they finish; only new connections go to the new
// address.
func (p *Proxy) Rebind(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	p.mu.Lock()
	oldLn := p.ln
	p.ln = ln
	p.cfg.Addr = addr
	p.mu.Unlock()

	p.serve(ln)

	// Stop accepting on the old address. Existing connections are deliberately
	// left alone so in-flight traffic is not cut off.
	if oldLn != nil {
		_ = oldLn.Close()
	}
	p.cfg.Log.Addf("proxy moved to %s", ln.Addr())
	return nil
}

// DisplayAddr renders the listen address for humans, collapsing a wildcard
// bind address to "*".
func (p *Proxy) DisplayAddr() string {
	host, port, err := net.SplitHostPort(p.Addr())
	if err != nil {
		return p.Addr()
	}
	switch host {
	case "", "0.0.0.0", "::":
		return "*:" + port
	}
	return net.JoinHostPort(host, port)
}

// Port returns the port the proxy currently listens on.
func (p *Proxy) Port() int {
	_, port, err := net.SplitHostPort(p.Addr())
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return n
}

// decodableEncodings are the Content-Encoding values the sniffer can expand
// for display.
var decodableEncodings = map[string]bool{
	"gzip": true, "x-gzip": true, "deflate": true, "identity": true, "": true,
}

// sanitizeAcceptEncoding removes encodings whose response bodies the viewer
// cannot expand, so a phone that offers "gzip, deflate, br" does not get a
// brotli payload we can only show as a hex dump. If the client accepts nothing
// we can read, the original value is preserved rather than breaking it.
func sanitizeAcceptEncoding(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	parts := strings.Split(v, ",")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		base := name
		if i := strings.IndexByte(base, ';'); i >= 0 {
			base = base[:i]
		}
		if decodableEncodings[strings.ToLower(strings.TrimSpace(base))] {
			kept = append(kept, name)
		}
	}
	if len(kept) == 0 {
		for _, part := range parts {
			base := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
			if base == "*" {
				// "*" means "anything", and gzip is something we can read.
				return "gzip, deflate"
			}
		}
		// The client only accepts encodings we cannot expand; better to hand
		// the response through untouched than to break it.
		return v
	}
	return strings.Join(kept, ", ")
}

func loadCABundle(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("no certificates found")
	}
	return pool, nil
}

// CA exposes the certificate authority used for TLS interception.
func (p *Proxy) CA() *ca.CA { return p.cfg.CA }

// CAPath returns the on-disk directory holding the CA material.
func (p *Proxy) CAPath() string { return p.cfg.CAPath }

// Addr returns the bound listener address.
func (p *Proxy) Addr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln == nil {
		return p.cfg.Addr
	}
	return p.ln.Addr().String()
}

// Mode returns the interception mode.
func (p *Proxy) Mode() string {
	if p.cfg.TunnelOnly {
		return "tunnel"
	}
	return "mitm"
}

// Started returns the start time.
func (p *Proxy) Started() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started
}

// Stats returns live connection counters.
func (p *Proxy) Stats() (active, total int64) {
	return p.activeConns.Load(), p.totalConns.Load()
}

// Close shuts the proxy down.
func (p *Proxy) Close() error {
	p.mu.Lock()
	srv, ln := p.srv, p.ln
	conns := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()

	for _, c := range conns {
		_ = c.Close()
	}
	p.transport.CloseIdleConnections()
	if srv != nil {
		_ = srv.Close()
	}
	if ln != nil {
		return ln.Close()
	}
	return nil
}

func (p *Proxy) track(c net.Conn) {
	p.mu.Lock()
	p.conns[c] = struct{}{}
	p.mu.Unlock()
	p.activeConns.Add(1)
	p.totalConns.Add(1)
}

func (p *Proxy) untrack(c net.Conn) {
	p.mu.Lock()
	_, ok := p.conns[c]
	delete(p.conns, c)
	p.mu.Unlock()
	if ok {
		p.activeConns.Add(-1)
	}
}

// ServeHTTP is the entry point for plain HTTP proxying and CONNECT tunnels.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	if !r.URL.IsAbs() {
		// Origin-form request addressed directly to the proxy port: this is
		// how a device on the same machine downloads the CA certificate.
		p.serveLocal(w, r, "direct")
		return
	}
	if isLocalHost(r.URL.Host) {
		// A short URL such as http://cli.proxy/ssl routed through the proxy.
		p.serveLocal(w, r, "local-host")
		return
	}
	scheme := r.URL.Scheme
	if scheme == "" {
		scheme = "http"
	}
	p.handle(w, r, scheme, r.URL.Host, r.RemoteAddr)
}

// ---------------------------------------------------------------------------
// CONNECT / TLS interception
// ---------------------------------------------------------------------------

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		p.cfg.Log.Addf("connect hijack failed: %v", err)
		return
	}
	p.track(conn)
	defer func() {
		p.untrack(conn)
	}()

	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	if isLocalHost(host) {
		// https://cli.proxy can only work once our root is trusted, which is
		// exactly what the visitor is trying to install. Say so plainly.
		body := "cli-proxy: open http://" + LocalHostName + "/ssl in plain HTTP instead.\n" +
			"Serving HTTPS here would need the very certificate you are about to install.\n"
		_, _ = conn.Write([]byte(fmt.Sprintf(
			"HTTP/1.1 409 Conflict\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			len(body), body)))
		_ = conn.Close()
		return
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "443")
	}

	// Acknowledge the tunnel before doing anything else.
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = conn.Close()
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	first, err := brw.Reader.Peek(1)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return
	}

	if p.cfg.TunnelOnly || first[0] != 0x16 { // 0x16 = TLS handshake record
		p.tunnel(conn, brw.Reader, host, r)
		return
	}

	p.mitm(conn, brw.Reader, host, r)
}

// tunnel blindly forwards bytes in both directions (non-TLS or tunnel mode).
func (p *Proxy) tunnel(client net.Conn, buffered *bufio.Reader, host string, r *http.Request) {
	start := time.Now()
	flow := p.cfg.Store.Add(&core.Flow{
		Start:      start,
		Scheme:     "https",
		Method:     "CONNECT",
		URL:        "https://" + host,
		Host:       host,
		Path:       "-",
		Proto:      r.Proto,
		Client:     client.RemoteAddr().String(),
		State:      core.StatePending,
		ReqHeaders: core.MapToHeaders(r.Header),
	})
	flow.AddTag("tunnel")

	upstream, err := net.DialTimeout("tcp", host, 15*time.Second)
	if err != nil {
		p.finishFlow(flow.ID, func(f *core.Flow) {
			f.State = core.StateError
			f.Err = err.Error()
			f.End = time.Now()
			f.Duration = f.End.Sub(f.Start)
		})
		_ = client.Close()
		return
	}
	defer upstream.Close()

	p.finishFlow(flow.ID, func(f *core.Flow) {
		f.Status = 200
		f.Reason = "Connection Established"
		f.AddTag("tls")
	})

	var in, out atomic.Int64
	done := make(chan struct{}, 2)
	go func() {
		n, _ := io.Copy(upstream, buffered)
		in.Store(n)
		if tc, ok := upstream.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		n, _ := io.Copy(client, upstream)
		out.Store(n)
		_ = client.Close()
		done <- struct{}{}
	}()
	<-done
	<-done

	p.finishFlow(flow.ID, func(f *core.Flow) {
		f.State = core.StateComplete
		f.End = time.Now()
		f.Duration = f.End.Sub(f.Start)
		f.BytesIn = in.Load()
		f.BytesOut = out.Load()
	})
}

// bufferedConn lets a TLS server consume bytes that were already peeked.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

func (p *Proxy) mitm(client net.Conn, buffered *bufio.Reader, host string, r *http.Request) {
	start := time.Now()
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name = host
			}
			return p.cfg.CA.Leaf(name)
		},
	}
	tlsConn := tls.Server(&bufferedConn{Conn: client, r: buffered}, tlsCfg)
	_ = tlsConn.SetDeadline(time.Now().Add(20 * time.Second))
	if err := tlsConn.Handshake(); err != nil {
		p.cfg.Log.Addf("TLS handshake failed for %s: %v", host, err)
		flow := p.cfg.Store.Add(&core.Flow{
			Start:  start,
			Scheme: "https",
			Method: "CONNECT",
			URL:    "https://" + host,
			Host:   host,
			Path:   "-",
			Proto:  r.Proto,
			Client: client.RemoteAddr().String(),
			State:  core.StateError,
			Err:    "tls: " + err.Error(),
			End:    time.Now(),
		})
		p.finishFlow(flow.ID, func(f *core.Flow) {
			f.Duration = f.End.Sub(f.Start)
			f.AddTag("pinned?")
		})
		_ = client.Close()
		return
	}
	_ = tlsConn.SetDeadline(time.Time{})

	clientAddr := client.RemoteAddr().String()
	one := newOneShotListener(tlsConn)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p.handle(w, req, "https", host, clientAddr)
		}),
		ReadHeaderTimeout: 30 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
		ConnState: func(c net.Conn, s http.ConnState) {
			if s == http.StateClosed || s == http.StateHijacked {
				one.Close()
			}
		},
	}
	// Serve (not ServeTLS): TLS is already established, and with a nil
	// TLSConfig net/http still wires up the bundled HTTP/2 handler when the
	// connection negotiated "h2" via ALPN.
	_ = srv.Serve(one)
}

// oneShotListener yields a single connection to http.Server.Serve.
type oneShotListener struct {
	conn   net.Conn
	mu     sync.Mutex
	served bool
	done   chan struct{}
	closed sync.Once
}

func newOneShotListener(c net.Conn) *oneShotListener {
	return &oneShotListener{conn: c, done: make(chan struct{})}
}

func (l *oneShotListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.served {
		l.served = true
		c := l.conn
		l.mu.Unlock()
		return c, nil
	}
	l.mu.Unlock()
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneShotListener) Close() error {
	l.closed.Do(func() { close(l.done) })
	return nil
}

func (l *oneShotListener) Addr() net.Addr { return l.conn.LocalAddr() }

// ---------------------------------------------------------------------------
// Request handling
// ---------------------------------------------------------------------------

// plan accumulates everything the rules decided for one exchange.
type plan struct {
	mockFile       string
	statusOverride int
	redirect       string
	location       string
	delay          time.Duration
	breakReq       bool
	breakResp      bool
	blockStatus    int
	respBody       []byte
	setRespHeaders []headerKV
	delRespHeaders []string
	tags           []string
}

type headerKV struct{ Name, Value string }

func (p *Proxy) handle(w http.ResponseWriter, r *http.Request, scheme, authority, clientAddr string) {
	start := time.Now()
	host := authority
	if host == "" {
		host = r.Host
	}
	u := *r.URL
	if u.Scheme == "" {
		u.Scheme = scheme
	}
	if u.Host == "" {
		u.Host = host
	}
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}

	body, trunc, err := readCapped(r.Body, p.cfg.MaxBody)
	if closer, ok := r.Body.(io.Closer); ok && closer != nil {
		_ = closer.Close()
	}

	flow := &core.Flow{
		Start:    start,
		Scheme:   u.Scheme,
		Method:   r.Method,
		URL:      u.String(),
		Host:     u.Host,
		Path:     path,
		Proto:    r.Proto,
		Client:   clientAddr,
		State:    core.StatePending,
		ReqBody:  body,
		ReqTrunc: trunc,
		BytesIn:  int64(len(body)),
	}
	if err != nil {
		flow.Err = err.Error()
	}
	flow.ReqHeaders = requestHeaders(r, u.Host)

	// Rules only mutate the incoming request; the flow is published afterwards
	// so the UI never observes a partially written record.
	pl := p.applyRules(r, u, path)

	flow.ReqHeaders = requestHeaders(r, u.Host)
	p.cfg.Store.Add(flow)

	// ---- short circuits ------------------------------------------------------
	if pl.blockStatus != 0 {
		p.finishFlow(flow.ID, func(f *core.Flow) {
			f.State = core.StateBlocked
			f.AddTag("block")
		})
		p.writeSynthetic(w, flow, pl.blockStatus, "text/plain; charset=utf-8",
			[]byte(fmt.Sprintf("cli-proxy: request blocked\n%s %s\n", r.Method, flow.URL)))
		return
	}

	if pl.location != "" {
		p.writeMock(w, flow, http.StatusFound,
			[]headerKV{{"Location", pl.location}, {"Content-Type", "text/plain"}},
			[]byte("cli-proxy: redirecting to "+pl.location+"\n"))
		return
	}

	if pl.mockFile != "" {
		p.serveMockFile(w, flow, pl)
		return
	}

	if pl.delay > 0 {
		time.Sleep(pl.delay)
	}

	// ---- request breakpoint --------------------------------------------------
	if pl.breakReq || p.cfg.Opts.BreakRequests.Load() {
		if !p.breakRequest(flow, r, &body) {
			p.writeSynthetic(w, flow, 502, "text/plain; charset=utf-8",
				[]byte("cli-proxy: request dropped at breakpoint\n"))
			p.finishFlow(flow.ID, func(f *core.Flow) {
				f.State = core.StateError
				f.Err = "dropped at request breakpoint"
			})
			return
		}
		u = *r.URL
	}

	// ---- forward -------------------------------------------------------------
	target := u
	if pl.redirect != "" {
		if parsed, perr := url.Parse(pl.redirect); perr == nil {
			target = *parsed
			if target.Scheme == "" {
				target.Scheme = u.Scheme
			}
			if target.Host == "" {
				target.Host = u.Host
			}
			pl.tags = append(pl.tags, "redirect")
		} else {
			p.cfg.Log.Addf("invalid redirect %q: %v", pl.redirect, perr)
		}
	}

	if isUpgrade(r) {
		p.handleUpgrade(w, r, &target, body, flow, pl)
		return
	}

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		p.fail(w, flow, err)
		return
	}
	outReq.Header = r.Header.Clone()
	for _, h := range hopHeaders {
		outReq.Header.Del(h)
	}
	outReq.Header.Del("Expect")
	outReq.Header.Del("Content-Length")
	if !p.cfg.KeepEncoding {
		if enc := sanitizeAcceptEncoding(r.Header.Get("Accept-Encoding")); enc != "" {
			outReq.Header.Set("Accept-Encoding", enc)
		}
	}
	outReq.Host = target.Host
	outReq.ContentLength = int64(len(body))
	if len(body) == 0 {
		outReq.Body = nil
		outReq.ContentLength = 0
	}

	if len(pl.tags) > 0 || pl.redirect != "" {
		redirected := ""
		if pl.redirect != "" {
			redirected = target.String()
		}
		tags := append([]string(nil), pl.tags...)
		p.finishFlow(flow.ID, func(f *core.Flow) {
			for _, t := range tags {
				f.AddTag(t)
			}
			if redirected != "" {
				f.Redirected = redirected
			}
		})
	}

	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		p.fail(w, flow, err)
		return
	}
	defer resp.Body.Close()

	p.cfg.Store.Update(flow.ID, func(f *core.Flow) {
		f.Status = resp.StatusCode
		f.Reason = strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)+" ")
		f.RespHeaders = core.MapToHeaders(resp.Header)
	})

	for _, name := range pl.delRespHeaders {
		resp.Header.Del(name)
	}
	for _, kv := range pl.setRespHeaders {
		if kv.Name != "" {
			resp.Header.Set(kv.Name, kv.Value)
		}
	}

	needFull := pl.breakResp || p.cfg.Opts.BreakResponses.Load() ||
		pl.respBody != nil || pl.statusOverride != 0

	if !needFull {
		p.streamResponse(w, flow, resp)
		return
	}

	respBody, rtrunc, rerr := readCapped(resp.Body, p.cfg.MaxBody)
	if rerr != nil {
		p.cfg.Log.Addf("read response body: %v", rerr)
	}

	if pl.statusOverride != 0 && pl.statusOverride != resp.StatusCode {
		resp.StatusCode = pl.statusOverride
		resp.Status = fmt.Sprintf("%d %s", pl.statusOverride, http.StatusText(pl.statusOverride))
		pl.tags = append(pl.tags, "restatus")
	}
	if pl.respBody != nil {
		respBody = pl.respBody
		rtrunc = false
		pl.tags = append(pl.tags, "rebody")
	}
	if len(pl.tags) > 0 {
		tags := append([]string(nil), pl.tags...)
		p.finishFlow(flow.ID, func(f *core.Flow) {
			for _, t := range tags {
				f.AddTag(t)
			}
			f.RespBody = respBody
			f.RespTrunc = rtrunc
		})
	} else {
		p.finishFlow(flow.ID, func(f *core.Flow) {
			f.RespBody = respBody
			f.RespTrunc = rtrunc
		})
	}

	if pl.breakResp || p.cfg.Opts.BreakResponses.Load() {
		edited, ok := p.breakResponse(flow, resp, respBody)
		if !ok {
			p.finishFlow(flow.ID, func(f *core.Flow) {
				f.State = core.StateError
				f.Err = "dropped at response breakpoint"
			})
			return
		}
		if edited != nil {
			resp = edited
			if b, err := io.ReadAll(edited.Body); err == nil {
				respBody = b
			}
		}
	}

	p.writeFullResponse(w, flow, resp, respBody)
}

// applyRules evaluates the rule set against the incoming request and mutates
// it in place. It never touches the published flow.
func (p *Proxy) applyRules(r *http.Request, u url.URL, path string) *plan {
	pl := &plan{}
	rules := p.cfg.Rules.Match(r.Method, u.String())
	for _, rule := range rules {
		for _, a := range rule.Actions {
			switch a.Kind {
			case core.ActHeader:
				if name, val := splitPair(a.Arg); name != "" {
					r.Header.Set(name, val)
					pl.tags = append(pl.tags, "rewrite")
				}
			case core.ActDelHeader:
				if name, _ := splitPair(a.Arg); name != "" {
					r.Header.Del(name)
					pl.tags = append(pl.tags, "rewrite")
				}
			case core.ActFile:
				pl.mockFile = a.Arg
			case core.ActRedirect:
				pl.redirect = a.Arg
			case core.ActLocation:
				pl.location = a.Arg
			case core.ActStatus:
				if n, err := strconv.Atoi(strings.TrimSpace(a.Arg)); err == nil {
					pl.statusOverride = n
				}
			case core.ActBody:
				pl.respBody = []byte(a.Arg)
			case core.ActResHeader:
				if name, val := splitPair(a.Arg); name != "" {
					pl.setRespHeaders = append(pl.setRespHeaders, headerKV{name, val})
					pl.tags = append(pl.tags, "rewrite")
				}
			case core.ActDelResHeader:
				if name, _ := splitPair(a.Arg); name != "" {
					pl.delRespHeaders = append(pl.delRespHeaders, name)
					pl.tags = append(pl.tags, "rewrite")
				}
			case core.ActBreak:
				switch strings.ToLower(strings.TrimSpace(a.Arg)) {
				case "resp", "response":
					pl.breakResp = true
				case "both", "all", "":
					pl.breakReq, pl.breakResp = true, true
				default:
					pl.breakReq = true
				}
				pl.tags = append(pl.tags, "break")
			case core.ActBlock:
				pl.blockStatus = http.StatusForbidden
				if a.Arg != "" {
					if n, err := strconv.Atoi(strings.TrimSpace(a.Arg)); err == nil {
						pl.blockStatus = n
					}
				}
			case core.ActDelay:
				if d, err := time.ParseDuration(strings.TrimSpace(a.Arg)); err == nil {
					pl.delay += d
					pl.tags = append(pl.tags, "delay")
				}
			case core.ActMapLocal:
				if prefix, dir := splitPair(a.Arg); prefix != "" && strings.HasPrefix(path, prefix) {
					rest := strings.TrimPrefix(path, prefix)
					pl.mockFile = strings.TrimSuffix(dir, "/") + "/" + strings.TrimPrefix(rest, "/")
				}
			}
		}
	}
	return pl
}

func (p *Proxy) serveMockFile(w http.ResponseWriter, flow *core.Flow, pl *plan) {
	data, err := os.ReadFile(pl.mockFile)
	if err != nil {
		p.cfg.Log.Addf("mock file %s: %v", pl.mockFile, err)
		p.writeSynthetic(w, flow, http.StatusBadGateway, "text/plain; charset=utf-8",
			[]byte("cli-proxy: cannot read mock file "+pl.mockFile+": "+err.Error()+"\n"))
		p.finishFlow(flow.ID, func(f *core.Flow) {
			f.State = core.StateError
			f.Err = err.Error()
			f.AddTag("mock")
		})
		return
	}
	status := pl.statusOverride
	if status == 0 {
		status = http.StatusOK
	}
	headers := []headerKV{{"Content-Type", mimeFor(pl.mockFile)}}
	headers = append(headers, pl.setRespHeaders...)
	p.writeMock(w, flow, status, headers, data)
}

// ---------------------------------------------------------------------------
// Upgraded connections (WebSocket and friends)
// ---------------------------------------------------------------------------

func isUpgrade(r *http.Request) bool {
	for _, v := range r.Header.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}

func (p *Proxy) handleUpgrade(w http.ResponseWriter, r *http.Request, target *url.URL, body []byte, flow *core.Flow, pl *plan) {
	upstream, err := net.DialTimeout("tcp", target.Host, 15*time.Second)
	if err != nil {
		p.fail(w, flow, err)
		return
	}
	defer upstream.Close()

	outReq := r.Clone(r.Context())
	outReq.URL = target
	outReq.Host = target.Host
	outReq.RequestURI = ""
	outReq.Body = io.NopCloser(bytes.NewReader(body))
	outReq.ContentLength = int64(len(body))
	_ = outReq.Write(upstream)

	upstreamReader := bufio.NewReader(upstream)
	tp := textproto.NewReader(upstreamReader)
	statusLine, err := tp.ReadLine()
	if err != nil {
		p.fail(w, flow, err)
		return
	}
	rawHeaders, err := tp.ReadMIMEHeader()
	if err != nil {
		p.fail(w, flow, err)
		return
	}

	conn, clientBuf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		p.fail(w, flow, err)
		return
	}
	var head strings.Builder
	head.WriteString(statusLine)
	head.WriteString("\r\n")
	for k, vv := range rawHeaders {
		for _, v := range vv {
			head.WriteString(k)
			head.WriteString(": ")
			head.WriteString(v)
			head.WriteString("\r\n")
		}
	}
	head.WriteString("\r\n")
	if _, err := conn.Write([]byte(head.String())); err != nil {
		_ = conn.Close()
		return
	}

	status := 0
	if fields := strings.SplitN(statusLine, " ", 3); len(fields) >= 2 {
		status, _ = strconv.Atoi(fields[1])
	}
	p.finishFlow(flow.ID, func(f *core.Flow) {
		f.Status = status
		f.Reason = "Switching Protocols"
		f.RespHeaders = core.MapToHeaders(http.Header(rawHeaders))
		f.AddTag("upgrade")
	})

	var in, out atomic.Int64
	done := make(chan struct{}, 2)
	go func() {
		n, _ := io.Copy(upstream, clientBuf)
		in.Store(n)
		_ = upstream.Close()
		done <- struct{}{}
	}()
	go func() {
		n, _ := io.Copy(conn, upstreamReader)
		out.Store(n)
		_ = conn.Close()
		done <- struct{}{}
	}()
	<-done
	<-done

	p.finishFlow(flow.ID, func(f *core.Flow) {
		f.State = core.StateComplete
		f.End = time.Now()
		f.Duration = f.End.Sub(f.Start)
		f.BytesIn += in.Load()
		f.BytesOut += out.Load()
	})
}

// ---------------------------------------------------------------------------
// Response writing
// ---------------------------------------------------------------------------

func (p *Proxy) streamResponse(w http.ResponseWriter, flow *core.Flow, resp *http.Response) {
	copyHeaders(w.Header(), resp.Header)
	bodyForbidden := resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified
	if bodyForbidden {
		w.WriteHeader(resp.StatusCode)
		p.finishFlow(flow.ID, func(f *core.Flow) {
			f.State = core.StateComplete
			f.End = time.Now()
			f.Duration = f.End.Sub(f.Start)
		})
		return
	}
	if resp.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	w.WriteHeader(resp.StatusCode)

	capped := &cappedBuffer{max: p.cfg.MaxBody}
	counter := &countingWriter{}
	_, err := io.Copy(io.MultiWriter(w, capped, counter), resp.Body)

	p.finishFlow(flow.ID, func(f *core.Flow) {
		f.RespBody = capped.buf
		f.RespTrunc = capped.trunc
		f.BytesOut = counter.n
		f.End = time.Now()
		f.Duration = f.End.Sub(f.Start)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			f.State = core.StateError
			f.Err = err.Error()
		} else {
			f.State = core.StateComplete
		}
	})
}

func (p *Proxy) writeFullResponse(w http.ResponseWriter, flow *core.Flow, resp *http.Response, body []byte) {
	copyHeaders(w.Header(), resp.Header)
	w.Header().Del("Content-Length")
	bodyForbidden := resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified
	if !bodyForbidden {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.WriteHeader(resp.StatusCode)
	if !bodyForbidden && len(body) > 0 {
		_, _ = w.Write(body)
	}

	p.finishFlow(flow.ID, func(f *core.Flow) {
		f.Status = resp.StatusCode
		if t := http.StatusText(resp.StatusCode); t != "" {
			f.Reason = t
		}
		f.RespHeaders = core.MapToHeaders(resp.Header)
		f.RespBody = body
		f.BytesOut = int64(len(body))
		f.End = time.Now()
		f.Duration = f.End.Sub(f.Start)
		f.State = core.StateComplete
	})
}

// writeSynthetic emits a locally generated response and records it.
func (p *Proxy) writeSynthetic(w http.ResponseWriter, flow *core.Flow, status int, ctype string, body []byte) {
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-Cli-Proxy", "synthetic")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	p.finishFlow(flow.ID, func(f *core.Flow) {
		f.Status = status
		f.Reason = http.StatusText(status)
		f.RespHeaders = []core.Header{
			{Name: "Content-Type", Value: ctype},
			{Name: "Content-Length", Value: strconv.Itoa(len(body))},
			{Name: "X-Cli-Proxy", Value: "synthetic"},
		}
		f.RespBody = body
		f.RespTrunc = false
		f.BytesOut = int64(len(body))
		f.End = time.Now()
		f.Duration = f.End.Sub(f.Start)
		if f.State != core.StateBlocked && f.State != core.StateError {
			f.State = core.StateComplete
		}
	})
}

func (p *Proxy) writeMock(w http.ResponseWriter, flow *core.Flow, status int, headers []headerKV, body []byte) {
	for _, kv := range headers {
		if kv.Name == "" {
			continue
		}
		w.Header().Set(kv.Name, kv.Value)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-Cli-Proxy", "mock")
	w.WriteHeader(status)
	_, _ = w.Write(body)

	respHeaders := make([]core.Header, 0, len(headers)+2)
	for _, kv := range headers {
		respHeaders = append(respHeaders, core.Header{Name: kv.Name, Value: kv.Value})
	}
	respHeaders = append(respHeaders,
		core.Header{Name: "Content-Length", Value: strconv.Itoa(len(body))},
		core.Header{Name: "X-Cli-Proxy", Value: "mock"})

	p.finishFlow(flow.ID, func(f *core.Flow) {
		f.State = core.StateComplete
		f.Status = status
		f.Reason = http.StatusText(status)
		f.RespHeaders = respHeaders
		f.RespBody = body
		f.RespTrunc = false
		f.BytesOut = int64(len(body))
		f.End = time.Now()
		f.Duration = f.End.Sub(f.Start)
		f.AddTag("mock")
	})
}

func (p *Proxy) fail(w http.ResponseWriter, flow *core.Flow, err error) {
	msg := err.Error()
	p.cfg.Log.Addf("%s %s: %s", flow.Method, flow.URL, msg)
	body := []byte("cli-proxy: upstream error\n" + msg + "\n")
	p.writeSynthetic(w, flow, http.StatusBadGateway, "text/plain; charset=utf-8", body)
	p.finishFlow(flow.ID, func(f *core.Flow) {
		f.State = core.StateError
		f.Err = msg
	})
}

func (p *Proxy) finishFlow(id int64, fn func(*core.Flow)) {
	p.cfg.Store.Update(id, fn)
}

// ---------------------------------------------------------------------------
// Breakpoints
// ---------------------------------------------------------------------------

// breakRequest pauses on the request; it returns false when the operator
// dropped the exchange.
func (p *Proxy) breakRequest(flow *core.Flow, r *http.Request, body *[]byte) bool {
	p.cfg.Store.Update(flow.ID, func(f *core.Flow) {
		f.State = core.StateIntercepted
		f.Intercepted = "request"
	})
	snapshot, _ := p.cfg.Store.Get(flow.ID)
	if snapshot == nil {
		snapshot = flow.Clone()
	}
	v := p.cfg.Breaker.Hold(&core.Breakpoint{
		Flow:  snapshot,
		Phase: core.PhaseRequest,
		Raw:   snapshot.RawRequest(),
	})
	p.cfg.Store.Update(flow.ID, func(f *core.Flow) {
		f.Intercepted = ""
		if f.State == core.StateIntercepted {
			f.State = core.StatePending
		}
	})
	if v.Drop {
		return false
	}
	if strings.TrimSpace(v.Raw) == "" {
		return true
	}
	edited, newBody, err := parseRawRequest(v.Raw)
	if err != nil {
		p.cfg.Log.Addf("breakpoint edit invalid: %v", err)
		return true
	}
	r.Method = edited.Method
	r.URL = edited.URL
	r.Host = edited.URL.Host
	r.Header = edited.Header
	r.ContentLength = int64(len(newBody))
	*body = newBody
	p.cfg.Store.Update(flow.ID, func(f *core.Flow) {
		f.Method = edited.Method
		f.URL = edited.URL.String()
		f.Host = edited.URL.Host
		f.Path = edited.URL.RequestURI()
		f.ReqBody = newBody
		f.ReqTrunc = false
		f.BytesIn = int64(len(newBody))
		hdr := []core.Header{{Name: "Host", Value: edited.URL.Host}}
		hdr = append(hdr, core.MapToHeaders(edited.Header)...)
		f.ReqHeaders = hdr
		f.AddTag("edit")
	})
	return true
}

// breakResponse pauses on the response; it returns (edited, false) to drop.
func (p *Proxy) breakResponse(flow *core.Flow, resp *http.Response, body []byte) (*http.Response, bool) {
	p.cfg.Store.Update(flow.ID, func(f *core.Flow) {
		f.State = core.StateIntercepted
		f.Intercepted = "response"
	})
	snapshot, _ := p.cfg.Store.Get(flow.ID)
	if snapshot == nil {
		snapshot = flow.Clone()
	}
	v := p.cfg.Breaker.Hold(&core.Breakpoint{
		Flow:  snapshot,
		Phase: core.PhaseResponse,
		Raw:   snapshot.RawResponse(),
	})
	p.cfg.Store.Update(flow.ID, func(f *core.Flow) {
		f.Intercepted = ""
		if f.State == core.StateIntercepted {
			f.State = core.StatePending
		}
	})
	if v.Drop {
		return nil, false
	}
	if strings.TrimSpace(v.Raw) == "" {
		return nil, true
	}
	edited, newBody, err := parseRawResponse(v.Raw, resp)
	if err != nil {
		p.cfg.Log.Addf("breakpoint edit invalid: %v", err)
		return nil, true
	}
	p.cfg.Store.Update(flow.ID, func(f *core.Flow) {
		f.Status = edited.StatusCode
		f.Reason = strings.TrimPrefix(edited.Status, strconv.Itoa(edited.StatusCode)+" ")
		f.RespHeaders = core.MapToHeaders(edited.Header)
		f.RespBody = newBody
		f.RespTrunc = false
		f.BytesOut = int64(len(newBody))
		f.AddTag("edit")
	})
	return edited, true
}

func parseRawRequest(raw string) (*http.Request, []byte, error) {
	head, body := splitRawMessage(raw)
	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(head + "\r\n")))
	if err != nil {
		return nil, nil, err
	}
	if req.URL.Scheme == "" {
		req.URL.Scheme = "http"
	}
	if req.URL.Host == "" {
		req.URL.Host = req.Host
	}
	req.RequestURI = ""
	// The body is taken verbatim from what the operator typed; a stale
	// Content-Length must never truncate it.
	req.Header.Del("Content-Length")
	req.Header.Del("Transfer-Encoding")
	req.TransferEncoding = nil
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	return req, body, nil
}

func parseRawResponse(raw string, orig *http.Response) (*http.Response, []byte, error) {
	head, body := splitRawMessage(raw)
	resp, err := http.ReadResponse(bufio.NewReader(strings.NewReader(head+"\r\n")), &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, nil, err
	}
	resp.Header.Del("Content-Length")
	resp.Header.Del("Transfer-Encoding")
	resp.TransferEncoding = nil
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotModified {
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	return resp, body, nil
}

// splitRawMessage splits a hand-edited HTTP message into its head (ending with
// the final header newline) and its verbatim body.
func splitRawMessage(raw string) (string, []byte) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if i := strings.Index(raw, "\n\n"); i >= 0 {
		return raw[:i+1], []byte(raw[i+2:])
	}
	return raw, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func requestHeaders(r *http.Request, host string) []core.Header {
	hdr := make([]core.Header, 0, len(r.Header)+1)
	if host != "" {
		hdr = append(hdr, core.Header{Name: "Host", Value: host})
	}
	hdr = append(hdr, core.MapToHeaders(r.Header)...)
	return hdr
}

func copyHeaders(dst, src http.Header) {
	for _, h := range hopHeaders {
		src.Del(h)
	}
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func readCapped(r io.Reader, max int64) ([]byte, bool, error) {
	if r == nil {
		return nil, false, nil
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, max+1))
	if n > max {
		return buf.Bytes()[:max], true, err
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return buf.Bytes(), false, err
	}
	return buf.Bytes(), false, nil
}

type cappedBuffer struct {
	buf   []byte
	max   int64
	trunc bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if int64(len(c.buf)) < c.max {
		room := c.max - int64(len(c.buf))
		if room > int64(len(p)) {
			room = int64(len(p))
		}
		c.buf = append(c.buf, p[:room]...)
		if room < int64(len(p)) {
			c.trunc = true
		}
	} else if len(p) > 0 {
		c.trunc = true
	}
	return len(p), nil
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

func splitPair(s string) (string, string) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "="); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

func mimeFor(path string) string {
	ext := strings.ToLower(path)
	if i := strings.LastIndexByte(ext, '.'); i >= 0 {
		ext = ext[i:]
	}
	switch ext {
	case ".json":
		return "application/json"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".js":
		return "application/javascript"
	case ".css":
		return "text/css"
	case ".xml":
		return "application/xml"
	case ".txt", ".log":
		return "text/plain; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".crt", ".pem", ".cer":
		return "application/x-x509-ca-cert"
	}
	return "application/octet-stream"
}
