package proxy

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// LocalHostName is the short hostname a device can use instead of the LAN
// address. Because the device already sends its HTTP traffic through us, a
// request for this name never needs DNS: the proxy answers it itself.
const LocalHostName = "cli.proxy"

// localHostNames are all the magic names answered locally. They are only ever
// reached through the proxy, so they cost nothing when unused.
var localHostNames = map[string]bool{
	"cli.proxy":      true,
	"cliproxy":       true,
	"cliproxy.local": true,
	"cert.local":     true,
	"proxy.man":      true,
}

// isLocalHost reports whether a host[:port] refers to the proxy's own pages.
func isLocalHost(hostport string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	h = strings.Trim(strings.ToLower(h), "[]")
	if i := strings.IndexByte(h, '%'); i >= 0 {
		h = h[:i]
	}
	return localHostNames[h]
}

// ShortCertURL is the shortest URL a device can open to install the root.
func (p *Proxy) ShortCertURL() string { return "http://" + LocalHostName + "/ssl" }

// LocalIPs returns the non-loopback IPv4 addresses of this machine, so the UI
// can tell the operator where to point a phone or tablet.
func LocalIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	sort.Strings(out)
	return out
}

// serveLocal handles the proxy's own pages: either a direct hit on the
// listening port, or a request for one of the magic hostnames routed through
// the proxy. This is the channel devices use to fetch the CA certificate.
func (p *Proxy) serveLocal(w http.ResponseWriter, r *http.Request, origin string) {
	path := strings.ToLower(strings.TrimSuffix(r.URL.Path, "/"))
	if path == "" {
		path = "/"
	}
	p.cfg.Log.Addf("%s request %s %s from %s", origin, r.Method, path, r.RemoteAddr)
	switch path {
	case "/", "/index.html", "/help":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(p.indexHTML()))
	case "/cert", "/cert.pem", "/ca", "/ca.crt", "/ca.pem", "/cli-proxy.crt",
		"/cliproxy.crt", "/cli-proxy-ca.crt", "/ssl", "/crt":
		w.Header().Set("Content-Type", "application/x-x509-ca-cert")
		w.Header().Set("Content-Disposition", `attachment; filename="cli-proxy-ca.crt"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(p.cfg.CA.CertPEM)
	case "/cert.der", "/ca.der":
		block, _ := pem.Decode(p.cfg.CA.CertPEM)
		if block == nil {
			http.Error(w, "corrupt CA", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-x509-ca-cert")
		w.Header().Set("Content-Disposition", `attachment; filename="cli-proxy-ca.der"`)
		_, _ = w.Write(block.Bytes)
	case "/ca.mobileconfig", "/ssl.mobileconfig", "/profile", "/ios":
		w.Header().Set("Content-Type", "application/x-apple-aspen-config")
		w.Header().Set("Content-Disposition", `attachment; filename="cli-proxy-ca.mobileconfig"`)
		_, _ = w.Write([]byte(p.mobileconfig()))
	case "/status", "/status.json":
		active, total := p.Stats()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"proxy":          p.Addr(),
			"mode":           p.Mode(),
			"version":        p.cfg.Version,
			"uptime":         time.Since(p.Started()).Round(time.Second).String(),
			"flows":          p.cfg.Store.Len(),
			"rules":          p.cfg.Rules.Len(),
			"breakpoints":    p.cfg.Breaker.Len(),
			"active_conns":   active,
			"total_conns":    total,
			"ca_fingerprint": p.cfg.CA.Fingerprint(),
		})
	default:
		http.Error(w, "not found\n\navailable: /  /ssl  /cert  /cert.der  /ca.mobileconfig  /status", http.StatusNotFound)
	}
}

func (p *Proxy) BaseURL() string {
	port := "8080"
	if _, pn, err := net.SplitHostPort(p.Addr()); err == nil {
		port = pn
	}
	ips := LocalIPs()
	host := "127.0.0.1"
	if len(ips) > 0 {
		host = ips[0]
	}
	return "http://" + net.JoinHostPort(host, port)
}

// DeviceHint returns the short "point your phone here" instruction.
func (p *Proxy) DeviceHint() string {
	return fmt.Sprintf("proxy %s  |  cert %s/cert", p.DisplayAddr(), p.BaseURL())
}

const certStepsText = `Certificate installation
=======================

Short URL — works on any device already pointed at this proxy
  %[1]s              -> this page (no IP address needed)
  %[1]s/cert         -> download the root certificate

iOS / iPadOS
  1. Open Safari and go to  %[2]s   (or %[1]s/cert)
  2. Settings > Profile Downloaded > Install   (enter passcode)
  3. Settings > General > About > Certificate Trust Settings
     -> enable full trust for "cli-proxy Root CA"
  4. Wi-Fi settings -> HTTP Proxy -> Manual
     Server: %[3]s   Port: %[4]s

Android
  1. Open %[2]s in Chrome and install the downloaded file
     (or use the shorter %[1]s/cert)
  2. Settings > Security > Encryption & credentials > Install a certificate
     -> CA certificate -> pick the downloaded file
     (Android 7+ : apps that opt out of user CAs cannot be intercepted)
  3. Wi-Fi > Modify network > Advanced > Proxy > Manual
     Hostname: %[3]s   Port: %[4]s

macOS
  # -O keeps only the last path segment, which is why the file must be named
  # explicitly with -o here.
  curl -o cli-proxy-ca.crt %[5]s/cli-proxy-ca.crt
  sudo security add-trusted-cert -d -r trustRoot \
       -k /Library/Keychains/System.keychain cli-proxy-ca.crt

Windows
  curl -o cli-proxy-ca.crt %[5]s/cli-proxy-ca.crt
  certutil -addstore -f ROOT cli-proxy-ca.crt
  Settings > Network > Proxy > Manual setup > %[3]s : %[4]s

Firefox (keeps its own trust store)
  Settings > Privacy & Security > Certificates > View Certificates
  > Authorities > Import -> select cli-proxy-ca.crt -> "Trust this CA"

Local machine (programs running on this computer)
  export HTTP_PROXY=http://127.0.0.1:%[4]s
  export HTTPS_PROXY=http://127.0.0.1:%[4]s

  curl -o cli-proxy-ca.crt http://127.0.0.1:%[4]s/cli-proxy-ca.crt
  curl -x http://127.0.0.1:%[4]s --cacert cli-proxy-ca.crt https://example.com
  curl -x http://127.0.0.1:%[4]s -k https://example.com     # skip trust, no install

  GUI applications and browsers use the system proxy instead of the
  environment:
    macOS  System Settings > Network > <interface> > Details > Proxies
           -> enable "Web Proxy" and "Secure Web Proxy" -> %[3]s : %[4]s
    Windows Settings > Network & Internet > Proxy > Manual setup
    Linux  GNOME Settings > Network > Network Proxy

  NOTE: both systems bypass the proxy for 127.0.0.1 / localhost / *.local by
  default, so requests to services on THIS machine are not captured. Remove
  those entries from the bypass list if you need to see them.
`

// CertSteps renders the step by step install guide.
func (p *Proxy) CertSteps() string {
	_, port, err := net.SplitHostPort(p.Addr())
	if err != nil {
		port = "8080"
	}
	ips := LocalIPs()
	host := "127.0.0.1"
	if len(ips) > 0 {
		host = ips[0]
	}
	return fmt.Sprintf(certStepsText,
		"http://"+LocalHostName, // 1 short base
		p.BaseURL()+"/cert",     // 2 full certificate URL
		host,                    // 3 LAN host
		port,                    // 4 port
		p.BaseURL(),             // 5 full base
	)
}

func (p *Proxy) indexHTML() string {
	_, port, _ := net.SplitHostPort(p.Addr())
	ips := LocalIPs()
	var hosts strings.Builder
	for _, ip := range ips {
		fmt.Fprintf(&hosts, "<code>%s</code> ", net.JoinHostPort(ip, port))
	}
	if hosts.Len() == 0 {
		hosts.WriteString("<em>no LAN address detected</em>")
	}
	return `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>cli-proxy</title>
<style>
 :root{color-scheme:dark light}
 body{font:15px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace;margin:0;padding:2rem;background:#12141a;color:#d6dae3}
 main{max-width:44rem;margin:0 auto}
 h1{font-size:1.3rem;margin:0 0 .2rem} h2{font-size:1rem;margin:1.6rem 0 .4rem;color:#8fd3ff}
 a.btn{display:inline-block;margin:.4rem .5rem .4rem 0;padding:.55rem .9rem;border-radius:.6rem;
   background:#1e2530;color:#9be3a0;text-decoration:none;border:1px solid #2c3648}
 a.btn:hover{background:#27303e;border-color:#3d4a61}
 code{background:#1a2029;padding:.1rem .35rem;border-radius:.3rem}
 pre{background:#171c24;border:1px solid #232b36;border-radius:.6rem;padding:.8rem;overflow:auto}
 .dim{color:#8b95a7} .box{border:1px solid #232b36;border-radius:.9rem;padding:1rem 1.2rem;background:#151a21}
</style></head><body><main>
<h1>cli-proxy</h1>
<div class="dim">lightweight HTTP/HTTPS intercepting proxy &mdash; ` + p.cfg.Version + `</div>
<div class="box" style="margin-top:1rem">
  <div>Proxy address: <code>` + p.DisplayAddr() + `</code></div>
  <div>LAN addresses: ` + hosts.String() + `</div>
  <div>Mode: <code>` + p.Mode() + `</code></div>
  <div>CA: <code>` + p.cfg.CA.Summary() + `</code></div>
</div>
<h2>Short URL</h2>
<div>Any device whose proxy already points here can use
  <code>` + p.ShortCertURL() + `</code> &mdash; no IP address to type.
  <div class="dim">(=` + p.BaseURL() + `/cert)</div>
</div>
<h2>1. Install the certificate</h2>
<a class="btn" href="/cert">Download CA certificate (.crt)</a>
<a class="btn" href="/cert.der">.der</a>
<a class="btn" href="/ca.mobileconfig">iOS profile</a>
<h2>2. Configure the proxy</h2>
<pre>` + p.CertSteps() + `</pre>
<h2>3. Watch the traffic</h2>
<pre>curl -o cli-proxy-ca.crt ` + p.BaseURL() + `/cli-proxy-ca.crt
curl -x ` + p.BaseURL() + ` --cacert cli-proxy-ca.crt https://example.com</pre>
<p class="dim">This page is served by the proxy itself on its listening port.</p>
</main></body></html>`
}

func (p *Proxy) mobileconfig() string {
	uuid := "cli-proxy-ca-root"
	block, _ := pem.Decode(p.cfg.CA.CertPEM)
	der := ""
	if block != nil {
		der = base64Std(block.Bytes)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
 <key>PayloadContent</key><array><dict>
   <key>PayloadCertificateFileName</key><string>cli-proxy-ca.crt</string>
   <key>PayloadDescription</key><string>cli-proxy interception root</string>
   <key>PayloadDisplayName</key><string>cli-proxy Root CA</string>
   <key>PayloadIdentifier</key><string>` + uuid + `.cert</string>
   <key>PayloadType</key><string>com.apple.security.root</string>
   <key>PayloadUUID</key><string>` + uuid + `-0001</string>
   <key>PayloadVersion</key><integer>1</integer>
   <key>PayloadContent</key><data>` + der + `</data>
 </dict></array>
 <key>PayloadDisplayName</key><string>cli-proxy Root CA</string>
 <key>PayloadIdentifier</key><string>` + uuid + `</string>
 <key>PayloadRemovalDisallowed</key><false/>
 <key>PayloadType</key><string>Configuration</string>
 <key>PayloadUUID</key><string>` + uuid + `-0000</string>
 <key>PayloadVersion</key><integer>1</integer>
</dict></plist>
`
}

func base64Std(b []byte) string {
	enc := base64.StdEncoding.EncodeToString(b)
	var out strings.Builder
	for i := 0; i < len(enc); i += 64 {
		end := i + 64
		if end > len(enc) {
			end = len(enc)
		}
		out.WriteString(enc[i:end])
		out.WriteByte('\n')
	}
	return out.String()
}
