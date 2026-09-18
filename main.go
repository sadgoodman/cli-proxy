// Command cli-proxy is a lightweight, dependency-free HTTP/HTTPS intercepting
// proxy with a keyboard- and mouse-driven terminal UI.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cliproxy/internal/ca"
	"cliproxy/internal/core"
	"cliproxy/internal/proxy"
	"cliproxy/internal/sysproxy"
	"cliproxy/internal/term"
	"cliproxy/internal/trust"
	"cliproxy/internal/tui"
)

const version = "0.1.1"

func main() {
	home := ca.Dir()

	var (
		addr        = flag.String("addr", "0.0.0.0:8080", "address the proxy listens on (0.0.0.0 exposes it to your LAN for devices)")
		port        = flag.Int("port", 0, "port to listen on; overrides the port part of -addr")
		tunnel      = flag.Bool("tunnel", false, "do not intercept TLS; only tunnel CONNECT and show the endpoints")
		caDir       = flag.String("ca-dir", home, "directory holding the CA certificate and key")
		rulesPath   = flag.String("rules", filepath.Join(home, "rules.json"), "rule file (JSON), loaded at start and saved on every change")
		filtersPath = flag.String("filters", filepath.Join(home, "filters.json"), "file holding the saved display filters")
		maxBody     = flag.Int64("max-body", 8<<20, "maximum bytes captured per request or response body")
		flowLimit   = flag.Int("flows", 2000, "how many flows to keep in memory")
		insecure    = flag.Bool("insecure", false, "do not verify origin certificates for upstream connections")
		caBundle    = flag.String("ca-bundle", "", "extra PEM bundle of roots trusted for upstream connections")
		keepEnc     = flag.Bool("keep-encoding", false, "leave Accept-Encoding untouched (brotli/zstd bodies will show as binary)")
		useSysProxy = flag.Bool("system-proxy", false, "point the operating system proxy at cli-proxy, and put it back on exit")
		installCert = flag.Bool("install-cert", false, "install the root certificate into the trust store and exit")
		removeCert  = flag.Bool("uninstall-cert", false, "remove the root certificate from the trust store and exit")
		certSystem  = flag.Bool("cert-system", false, "with -install-cert/-uninstall-cert, target the machine-wide trust store")
		headless    = flag.Bool("headless", false, "do not start the TUI; stream captured flows to stdout")
		showVersion = flag.Bool("version", false, "print the version and exit")
		importRules = flag.String("import-rules", "", "import a file of DSL rules at startup and exit the flag's work")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Printf("cli-proxy %s\n", version)
		return
	}

	listenAddr, err := resolveAddr(*addr, *port)
	if err != nil {
		fatal("%v", err)
	}

	authority, err := ca.Load(*caDir)
	if err != nil {
		fatal("certificate authority: %v", err)
	}

	store := core.NewStore(*flowLimit)
	ruleset := core.NewRuleSet(*rulesPath)
	if err := ruleset.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "cli-proxy: cannot load rules from %s: %v\n", *rulesPath, err)
	}
	if *importRules != "" {
		data, err := os.ReadFile(*importRules)
		if err != nil {
			fatal("import rules: %v", err)
		}
		added, errs := ruleset.ImportText(string(data))
		fmt.Printf("imported %d rule(s)\n", len(added))
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "  "+e)
		}
		return
	}

	// Certificate installation is a standalone action; do it and stop.
	trustMgr := trust.New(authority.CertPath(), authority.CommonName())
	if *installCert || *removeCert {
		if err := certAction(trustMgr, *installCert, *certSystem); err != nil {
			fatal("%v", err)
		}
		return
	}

	filterset := core.NewFilterSet(*filtersPath)
	if err := filterset.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "cli-proxy: cannot load saved filters from %s: %v\n", *filtersPath, err)
	}

	breaker := core.NewBreaker()
	opts := core.DefaultOptions()
	elog := core.NewLogger(1000)

	px := proxy.New(proxy.Config{
		Addr:             listenAddr,
		TunnelOnly:       *tunnel,
		MaxBody:          *maxBody,
		CA:               authority,
		Store:            store,
		Rules:            ruleset,
		Breaker:          breaker,
		Opts:             opts,
		Log:              elog,
		Version:          version,
		CAPath:           *caDir,
		InsecureUpstream: *insecure,
		CABundle:         *caBundle,
		KeepEncoding:     *keepEnc,
	})
	if err := px.Start(); err != nil {
		fatal("listen on %s: %v", listenAddr, err)
	}

	// System proxy control. A leftover backup means an earlier run was killed
	// before it could restore anything, so undo that first.
	sysCtrl := sysproxy.New(*caDir, func(format string, args ...any) { elog.Addf(format, args...) })
	// Restore on every exit path: the normal return, a fatal() abort, and a
	// panic. A hard kill is covered by the recovery copy on disk.
	defer sysCtrl.Restore()
	onExit(func() { _ = sysCtrl.Restore() })
	sysCtrl.RecoverStale()
	if *useSysProxy {
		if err := sysCtrl.EnableAddr(px.Addr()); err != nil {
			fmt.Fprintf(os.Stderr, "cli-proxy: system proxy: %v\n", err)
		}
	}

	// Turn SIGINT/SIGTERM into a clean shutdown so the terminal and the system
	// proxy are always put back.
	stop := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		close(stop)
	}()

	interactive := !*headless && term.IsTerminal(os.Stdin) && term.IsTerminal(os.Stdout)
	if interactive {
		printBanner(px, authority, *caDir, *rulesPath, *filtersPath, sysCtrl)
		err := tui.Run(tui.Config{
			Proxy:       px,
			Store:       store,
			Rules:       ruleset,
			Breaker:     breaker,
			Opts:        opts,
			Log:         elog,
			Version:     version,
			SystemProxy: sysCtrl,
			Trust:       trustMgr,
			Filters:     filterset,
			Stop:        stop,
		})
		_ = px.Close()
		if err != nil {
			fatal("%v", err)
		}
		return
	}

	if !*headless {
		fmt.Fprintf(os.Stderr, "cli-proxy: stdin/stdout is not a terminal, falling back to --headless\n")
	}
	runHeadless(px, store, opts, breaker)
}

// certAction installs or removes the root certificate from the trust store.
func certAction(m *trust.Manager, install, system bool) error {
	if !m.Available() {
		return fmt.Errorf("certificate installation is not available on this platform; " +
			"download the certificate and install it manually instead")
	}
	if install {
		where, err := m.Install(system)
		if err != nil {
			return err
		}
		fmt.Printf("cli-proxy root certificate installed into %s\n", where)
		return nil
	}
	if err := m.Uninstall(system); err != nil {
		return err
	}
	fmt.Println("cli-proxy root certificate removed from the trust store")
	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, `cli-proxy %s — lightweight HTTP/HTTPS intercepting proxy

usage: cli-proxy [flags]

flags:
`, version)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
examples:
  cli-proxy                          start the TUI on 0.0.0.0:8080
  cli-proxy -addr 127.0.0.1:9000     bind elsewhere
  cli-proxy -port 9090               keep the host, change the port
  cli-proxy -addr 127.0.0.1 -port 9090   explicit host and port
  cli-proxy -tunnel                  observe CONNECT targets only, no TLS interception
  cli-proxy -headless                no UI, stream flows to stdout
  cli-proxy -system-proxy            also route this machine's own traffic through it
  cli-proxy -install-cert            trust the root certificate for this user
  cli-proxy -install-cert -cert-system   ... for every user (opens a terminal)
  cli-proxy -import-rules rules.txt  add DSL rules to the saved rule set

rule DSL (one per line):
  [METHOD] URL_REGEX :: action[=arg] :: action[=arg] ...

  file=/tmp/mock.json     reply with a local file
  redirect=https://other  fetch the response from another address
  location=https://other  reply with a 302 to another address
  status=503              override the status code
  body={"a":1}            override the response body
  header=X-Debug=1        set a request header
  delheader=Cookie        drop a request header
  resheader=X-Env=test    set a response header
  delresheader=Set-Cookie drop a response header
  break=req|resp|both     pause for interactive editing
  block=403               refuse the request
  delay=500ms             add latency
  maplocal=/static/=/dir  serve a directory under a URL prefix

go to %s/cert from a device to install the interception certificate.
`, "http://<your-lan-ip>:8080")
}

// resolveAddr applies the -port override on top of -addr, keeping the host
// part of -addr intact.
func resolveAddr(addr string, port int) (string, error) {
	if port == 0 {
		return addr, nil
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("-port %d is out of range (1-65535)", port)
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// -addr had no port at all; treat it as a bare host.
		host = strings.Trim(addr, "[]")
	}
	if host == "" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

var exitHooks []func()

// onExit registers a cleanup that must run even when fatal() exits directly.
func onExit(fn func()) { exitHooks = append(exitHooks, fn) }

func fatal(format string, args ...any) {
	for i := len(exitHooks) - 1; i >= 0; i-- {
		exitHooks[i]()
	}
	fmt.Fprintf(os.Stderr, "cli-proxy: "+format+"\n", args...)
	os.Exit(1)
}

func printBanner(px *proxy.Proxy, authority *ca.CA, caDir, rulesPath, filtersPath string, sysCtrl *sysproxy.Controller) {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  cli-proxy %s\n", version)
	fmt.Fprintf(&b, "  ├ listening on   %s  (%s)\n", px.DisplayAddr(), strings.ToUpper(px.Mode()))
	for _, ip := range proxy.LocalIPs() {
		fmt.Fprintf(&b, "  ├ devices use    %s\n", ip)
	}
	fmt.Fprintf(&b, "  ├ certificate    %s\n", caDir)
	fmt.Fprintf(&b, "  ├ install from   %s/cert\n", px.BaseURL())
	fmt.Fprintf(&b, "  ├ rules          %s\n", rulesPath)
	fmt.Fprintf(&b, "  ├ filters        %s\n", filtersPath)
	if sysCtrl.Active() {
		fmt.Fprintf(&b, "  └ system proxy   ON -> %s (restored on exit)\n\n", sysCtrl.Target())
	} else {
		fmt.Fprintf(&b, "  └ system proxy   off (toggle it in the Cert view with s)\n\n")
	}
	fmt.Fprint(os.Stderr, b.String())
}

// runHeadless streams flows to stdout so the proxy can be used over ssh, in
// scripts, or on a machine without a terminal.
func runHeadless(px *proxy.Proxy, store *core.Store, opts *core.Options, breaker *core.Breaker) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	fmt.Printf("cli-proxy %s headless — listening on %s (%s)\n", version, px.DisplayAddr(), px.Mode())
	fmt.Printf("install the CA from %s/cert\n", px.BaseURL())
	fmt.Printf("%-6s %-8s %-7s %-28s %-9s %s\n", "id", "time", "method", "host", "status", "url")

	seen := map[int64]bool{}
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-sig:
			fmt.Println("\nshutting down")
			_ = px.Close()
			return
		case <-tick.C:
			for _, f := range store.Snapshot() {
				if seen[f.ID] || f.State == core.StatePending {
					continue
				}
				seen[f.ID] = true
				status := f.StatusText()
				if f.Err != "" {
					status = "ERR"
				}
				fmt.Printf("%-6d %-8s %-7s %-28s %-9s %s%s\n",
					f.ID, f.Start.Format("15:04:05"), f.Method,
					truncate(f.Host, 28), status, f.URL, tags(f))
			}
		}
	}
}

func tags(f *core.Flow) string {
	if len(f.Tags) == 0 {
		return ""
	}
	return "  [" + strings.Join(f.Tags, ",") + "]"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
