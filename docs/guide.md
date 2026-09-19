# cli-proxy — full guide

[![CI](https://github.com/sadgoodman/cli-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/sadgoodman/cli-proxy/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](../LICENSE)

**English** · [Русский](guide.ru.md)

The full reference: every flag, the rule and filter syntax, key bindings,
certificate and system proxy handling, and how the project is put together.

For a quick overview, see the [README](../README.md).

---

A deliberately lightweight CLI proxy for intercepting, inspecting and rewriting
HTTP/HTTPS traffic in real time. One static binary, **zero external
dependencies** — the Go standard library only.

```
╭─ cli-proxy ─────────────────────────────────────── 0.0.0.0:8080 · MITM · 128 flows ─╮
│  Flows   Rules   Cert   Log   Help                                                  │
├─────────────────────────────────────────────────────────────────────────────────────┤
│╭─ flows · 128 shown / 128 captured ─────────┬───────────────────┬────────┬────────╮ │
││    # │ Time     │ Method │ Host            │ Path              │ Status │   Size │ │
│├──────┼──────────┼────────┼─────────────────┼───────────────────┼────────┼────────┤ │
││    1 │ 12:04:11 │ GET    │ api.example.com │ /v1/users         │    200 │   1.2K │ │
││    2 │ 12:04:11 │ POST   │ api.example.com │ /v1/orders        │    201 │    84B │ │
││    3 │ 12:04:12 │ GET    │ cdn.example.net │ /logo.png         │    404 │    0B  │ │
│╰──────┴──────────┴────────┴─────────────────┴───────────────────┴────────┴────────╯ │
╰──[↵]open──[f]filter──[b]brk-req──[m]mock──[M]redir──[i]break──[s]save──[q]quit──────╯
```

## Features

* **Real-time interception** — every HTTP request and response, including
  bodies, headers, sizes, response time and status. The list updates as data
  arrives.
* **Transparent MITM for HTTPS** — its own root CA; leaf certificates are
  generated on the fly for each host (SAN + ECDSA P-256, 365-day validity,
  which satisfies the Apple/Android constraints). **HTTP/2** is supported
  (ALPN `h2`).
* **Breakpoints** — pause a request or a response, edit it on the fly right in
  the raw HTTP message (method, URL, headers, body, status) and send it on its
  way.
* **Response substitution** — from a local file (`file=`), from another address
  (`redirect=` — the proxy fetches that address itself) or with a real redirect
  (`location=`).
* **Rewriting** — request and response headers, status, body, delay, blocking.
* **Search and filters** — by URL, regular expression, method, status, host,
  body, tags, with negation, `AND`, `OR` and parentheses.
* **Readable bodies** — the proxy never asks a server for brotli/zstd, so
  responses from mobile clients show up as text rather than as a hex dump.
* **Request and response panes with tabs** — `headers`, `body`, `raw`; request
  on the left, response on the right, and either pane can be collapsed.
* **Readable JSON** — bodies are re-indented and coloured, and long lines wrap
  on word boundaries instead of being clipped.
* **Saved filters** — a set of persistent filters that stay enabled as you
  switch tabs and survive a restart.
* **One-key certificate installation** — `i` in the **Cert** section or
  `-install-cert`; on macOS with no password and no sudo, and removal is
  symmetric.
* **System proxy** — one key (`s` in the **Cert** section) or the
  `-system-proxy` flag routes the whole machine's traffic through cli-proxy,
  browsers and GUI applications included. The previous settings are restored on
  exit and even after `kill -9`.
* **Device proxying** — listens on `0.0.0.0`, serves the certificate at
  `http://<ip>:8080/cert`, `/cert.der` and `/ca.mobileconfig` (an iOS profile)
  and shows step-by-step installation instructions.
* **Interface** — rounded frames and tables (`╭ ╮ ╰ ╯ ┬ ┴ ├ ┤ ┼`), fully
  keyboard-driven, plus the mouse: hovering highlights rows, tabs and buttons,
  clicking selects. Menu items switch with `←`/`→` (or `Tab`).

## Installing

`cli-proxy` is a single static binary, so there is no installer to run and
nothing to uninstall. Pick whichever package manager you already use, or take
the archive and put the binary wherever you like.

### macOS and Linux

```sh
brew install sadgoodman/tap/cli-proxy
```

### Windows

Scoop needs no administrator rights:

```powershell
scoop bucket add sadgoodman https://github.com/sadgoodman/scoop-bucket
scoop install cli-proxy
```

Then `scoop update cli-proxy` and `scoop uninstall cli-proxy` manage it. The
manifest covers both x86-64 and arm64 Windows.

The first time the proxy binds to `0.0.0.0`, Windows Defender Firewall asks
whether to allow it on private networks — say yes if you want to intercept
traffic from a phone or another machine.

### Everything else

Download a prebuilt archive from the [latest release][rel]:

| Platform | Asset |
|---|---|
| Linux x86-64 | `cli-proxy_<version>_linux_amd64.tar.gz` |
| Linux arm64 | `cli-proxy_<version>_linux_arm64.tar.gz` |
| macOS Apple Silicon | `cli-proxy_<version>_darwin_arm64.tar.gz` |
| macOS Intel | `cli-proxy_<version>_darwin_amd64.tar.gz` |
| Windows x86-64 | `cli-proxy_<version>_windows_amd64.zip` |

`checksums.txt` in the same release carries the SHA-256 of every archive.

Unpack it and put `cli-proxy` (or `cli-proxy.exe`) somewhere on your `PATH`.
The archives are plain files, not installers: no registry entries, no services.

### Keeping the packages current

Both the Homebrew formula and the Scoop manifest update themselves.
[`sadgoodman/homebrew-tap`][tap] and [`sadgoodman/scoop-bucket`][scoop] are
checked against the latest release every hour. To pick a release up
immediately, run the **Update formulae** or **Update manifests** workflow from
the corresponding repository's Actions tab.

There is no Chocolatey package and no winget manifest yet. Chocolatey would
require administrator rights on every install and a moderated community review,
which buys little for a single binary; winget needs the manifests merged into
`microsoft/winget-pkgs` first.

## Building

```sh
go build -o cli-proxy .            # regular build
go build -ldflags="-s -w" -o cli-proxy .   # ~6.9 MB with no debug info
```

Cross-compiling with no extra tooling:

```sh
GOOS=linux   GOARCH=amd64 go build -o cli-proxy-linux .
GOOS=windows GOARCH=amd64 go build -o cli-proxy.exe .
```

Or through `make build`, `make test`, `make vet`, `make fmt`.

## Quick start

```sh
./cli-proxy                        # TUI on 0.0.0.0:8080
./cli-proxy -port 9090             # same host, different port
./cli-proxy -addr 127.0.0.1 -port 9090   # both host and port explicit
./cli-proxy -addr 127.0.0.1:9000   # local only
./cli-proxy -tunnel                # no MITM: only CONNECT hosts are visible
./cli-proxy -headless              # no UI, a stream of lines on stdout
```

Checking it from the same machine:

```sh
curl -x http://127.0.0.1:8080 --cacert ~/.cli-proxy/ca.pem https://example.com
```

## Changing the port

You can set the port with a flag, or change it right in the running interface:

* `-port 9090` — keeps the host from `-addr` and changes only the port.
* `-addr 127.0.0.1 -port 9090` — both host and port explicit.
* In the TUI — the `p` key (or the **port** button at the bottom of the screen,
  also available in the **Cert** section). It accepts either a number (`9090`)
  or a full address (`127.0.0.1:9090`).

The change takes effect immediately, **with no restart**: already captured
flows, rules, breakpoints and the root certificate are all preserved. The new
address tries to bind the port first, and only if that succeeds is the old
listener closed — so on failure (for example, "address already in use") the
proxy keeps running on the previous port and the reason appears in the status
line. Open connections are not dropped; they are allowed to finish.

After changing the port, remember to change it in the proxy settings on your
device as well. The address in the interface header and in the **Cert** section
updates automatically; when listening on all interfaces it is shown as
`*:9090`.

## One-key certificate installation

The root certificate can be trusted straight from the app — no file to download
and no terminal commands.

* In the interface: the **Cert** section (`3`) → `i`. The **install cert**
  button does the same thing.
* From the command line: `./cli-proxy -install-cert`.
* To remove it: `u` in the same section or `./cli-proxy -uninstall-cert`.

On macOS the per-user install runs **without a password and without sudo**: the
certificate goes into the user's keychain with full trust
(`security add-trusted-cert -r trustRoot`). After that, HTTPS traffic is visible
in browsers, curl and most applications with no `--cacert` and no `-k`.

The operation runs in the background, so the interface does not freeze while
the system asks for authorisation. While it runs, the header shows `cert…`, and
the `Trusted` line in the **Cert** section reports the current state:
`trusted — macOS keychain` or `not trusted by macOS`.

| System | What the per-user install does | For all users (`I` / `-cert-system`) |
|---|---|---|
| **macOS** | `security add-trusted-cert` into the user's keychain, no password | opens a Terminal window with `sudo security add-trusted-cert -d` |
| **Windows** | `certutil -user -addstore ROOT` | `certutil -addstore ROOT` with a UAC prompt |
| **Linux** | `certutil -A` into the NSS database (`~/.pki/nssdb`), which Chrome and Chromium read | a terminal window with `sudo cp … && sudo update-ca-certificates` |

If macOS still asks for authorisation (which happens when a trust entry for
that certificate already exists), the attempt is aborted after a few seconds
and a Terminal window opens automatically with the same command, where the
prompt can be confirmed properly. That way the interface never hangs waiting
for a click.

Removal is symmetric: both the trust setting and the certificate itself are
removed from the keychain, so the state returns to what it was.

> Firefox and some applications keep their own certificate store and do not use
> the system root — they have to be configured separately (there are
> instructions on the `/ssl` page).

## Proxying from a phone or tablet

1. Start `cli-proxy` — the UI header and the **Cert** section (`3`) will show
   an address like `http://192.168.1.42:8080`.
2. On the device, open that address and download the root certificate.

**Short URL.** You do not need to type the IP and port every time: just enter
`http://cli.proxy/ssl` — the request goes through the proxy anyway, so no DNS
is needed and the proxy answers it itself. `/cert`, `/cert.der` and
`/ca.mobileconfig` also work on the names `cli.proxy`, `cliproxy`,
`cliproxy.local`, `cert.local` and `proxy.man` (for Proxyman compatibility). If
you open `https://cli.proxy/ssl`, the proxy returns a clear explanation instead
of a handshake error — the certificate is not installed yet, so you need
`http://`.
3. In the Wi-Fi settings, set the proxy manually: host — the computer's IP,
   port — 8080.
4. The **Cert** section in the UI (`o` opens the installation page in a
   browser) has detailed instructions for iOS, Android, macOS, Windows and
   Firefox.

In short:

| Platform | Installing the root |
|---|---|
| **iOS / iPadOS** | Safari → `/cert` → *Profile Downloaded* → Install → *General → About → Certificate Trust Settings* → enable full trust. Or just use `/ca.mobileconfig`. |
| **Android** | `/cert` → *Settings → Security → Encryption & credentials → Install a certificate → CA certificate*. (Android 7+ does not trust user CAs in apps that explicitly opt out.) |
| **macOS** | `curl -o cli-proxy-ca.crt http://IP:8080/cli-proxy-ca.crt && sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain cli-proxy-ca.crt` |
| **Windows** | Download `/cert`, then `certutil -addstore -f ROOT cli-proxy-ca.crt` |
| **Firefox** | Its own trust store: *Settings → Privacy → Certificates → View Certificates → Authorities → Import*. |
| **Scripts** | `curl -x http://IP:8080 --cacert ca.pem https://…` |

The CA is stored in `~/.cli-proxy/` (`ca.pem`, `ca-key.pem`); the path can be
overridden with the `-ca-dir` flag or the `CLI_PROXY_HOME` environment variable.

### Local traffic from the same machine

The proxy only sees traffic that is sent to it. The easiest way is to turn on
the **system proxy**, which routes everything through cli-proxy, browsers and
GUI applications included:

```sh
./cli-proxy -system-proxy          # enable it right at startup
```

Or in the interface: the **Cert** section (`3`) → the `s` key (the
**sys-proxy** button). The state is visible both in the header (`sys-proxy`)
and as a line in the **Cert** section:

```
│ System proxy ON -> 127.0.0.1:8080   (restored when cli-proxy exits)   │
```

**The previous settings are restored automatically.** A snapshot of the
configuration is taken *before* any changes and saved to
`<ca-dir>/sysproxy-backup.json`, not just kept in memory. Therefore:

* a normal exit (`q`), `Ctrl-C` and `SIGTERM` all restore the settings;
* if enabling fails, the changes are rolled back immediately;
* after `kill -9`, a power loss or a panic, the next launch of cli-proxy finds
  the backup and restores the configuration from **before** it started;
* changing the port while the system proxy is on automatically points it at the
  new address.

Both the enable flag and the address with port are restored, so the state
matches the original byte for byte. What exactly is supported:

| System | Mechanism |
|---|---|
| **macOS** | `networksetup` — every enabled network service, web and secure web proxy |
| **Windows** | the `HKCU\...\Internet Settings` registry keys (`ProxyEnable`, `ProxyServer`, `ProxyOverride`) plus `InternetSetOption`, so the changes apply without a reboot |
| **Linux** | GNOME via `gsettings` (`mode`, `http`, `https`) |

On other systems the button reports that the feature is unavailable.

If you would rather not turn on the system proxy, point programs at it manually:

```sh
export HTTP_PROXY=http://127.0.0.1:8080
export HTTPS_PROXY=http://127.0.0.1:8080

curl -o cli-proxy-ca.crt http://127.0.0.1:8080/cli-proxy-ca.crt
curl -x http://127.0.0.1:8080 --cacert cli-proxy-ca.crt https://example.com
curl -x http://127.0.0.1:8080 -k https://example.com      # without installing the root
```

> **Why local traffic may not show up.** By default macOS and Windows exclude
> the addresses `127.0.0.1`, `localhost` and `*.local` from the proxy. Requests
> to services on the same machine bypass the proxy even when the system proxy
> is on. Remove those entries from the exclusion list if you need to see them.
> Requests to external sites are intercepted as usual.

One more thing: requests to the proxy's own pages (`/ssl`, `/cert`, `/status`)
do not appear in the flow list — they are not proxied traffic. They are written
to the **Log** section marked `direct` or `local-host`, so you can see that the
proxy is hearing you.

## Request and response panes

Below the flow list and in the full-screen view, the request and the response
are shown side by side: **request on the left, response on the right**. Each
pane has its own tab strip:

```
╭─ request · GET https://api.example.com/v1/users ───── headers · body · raw ─╮
│ GET /v1/users HTTP/1.1                                                      │
│ Host: api.example.com                                                       │
╰─────────────────────────────────────────────────────────────────────────────╯
```

* `headers` — the start line and the headers, without the body;
* `body` — the body only, with JSON formatting and colouring;
* `raw` — the whole message exactly as it went over the wire.

Controls:

| Keys | Action |
|---|---|
| `Tab` | switch the active pane (request ↔ response) |
| `Shift-Tab` | change the active pane's tab |
| `h` / `r` | jump straight to `headers` / `raw` |
| `b` | `body` (in the full-screen view; in the list `b` is the breakpoint key) |
| `[` / `]` | collapse the left / right pane |
| `\` | show both panes again |
| click a tab | select that tab and make its pane active |
| `↑ ↓ PgUp PgDn` | scroll the active pane |

The active pane has a brighter border. When one of the panes is collapsed, the
other takes the full width and shows a hint at the bottom explaining how to
bring the hidden one back.

### JSON and long lines

A body with `Content-Type: application/json` (or one that simply starts with
`{` or `[`) is re-indented and coloured: keys, strings, numbers and literals
each get their own colour. Long lines **wrap** instead of being clipped, and
wrapping prefers to break at spaces so a value is not split down the middle. If
`-keep-encoding` is on and the server replied with brotli, you get an explicit
explanation instead of binary noise.

## Saved filters

The filter bar (`f`) is temporary: `Esc` clears it. To make a filter permanent,
save it on the **Filters** tab (`2`):

| Keys | Action |
|---|---|
| `space` (or a click) | enable/disable the selected filter |
| `a` | add a filter |
| `e` | edit the selected one |
| `d` | delete the selected one |
| `c` | delete all |
| `p` | pin what is currently typed in the filter bar |

All enabled saved filters are combined with **AND** and applied together with
the temporary filter from the bar. The active count is shown in the header
(`2 saved filter`) and in the list title (`+2 saved`). Filters are stored in
`~/.cli-proxy/filters.json` (the `-filters` flag) and survive a restart.

## Controls

### Keyboard

| Keys | Action |
|---|---|
| `←` `→` / `1`–`6` | switch menu tabs |
| `↑` `↓` / `j` `k` | move through the list |
| `PgUp` `PgDn` `Home` `End` | page-by-page, and to the start/end |
| `Enter` / double click | full view of the request and the response |
| `f` or `/` | filter and search (applied live) |
| `b` / `B` | breakpoint on all requests / responses |
| `x` | release all paused breakpoints |
| `m` | save the response body to a file and create a `file=` rule |
| `M` | create a `redirect=` rule to another address |
| `i` | create a `break=both` rule for this URL |
| `s` | save the whole exchange to a file |
| `p` | change the listening port (applied immediately) |
| `i` (in the Cert section) | trust the root certificate |
| `I` (in the Cert section) | the same, but for all users |
| `u` (in the Cert section) | remove the certificate from the trust store |
| `s` (in the Cert section) | turn this machine's system proxy on/off |
| `c` | clear the list |
| `Tab` / `Shift-Tab` | active pane / pane tab (see above) |
| `h` `b` `r` | pane tabs: headers, body, raw message |
| `[` `]` `\` | collapse the left / right pane, show both |
| `?` | help |
| `q` / `Ctrl-C` | quit |

In the breakpoint editor: type as usual, `Enter` starts a new line, `Ctrl-S`
(or `F10`) sends, `Esc` discards the exchange.

In the **Rules** section: `Space` enables/disables a rule, `a` adds one, `e`
edits the selected one, `d` deletes it, `c` deletes all of them.

In the port prompt, type either a number or `host:port`; `Enter` applies it,
`Esc` cancels.

### Mouse

* Hovering highlights a table row, a tab or a button.
* Clicking selects a row, switches a tab, or presses a button at the bottom of
  the screen.
* Double-clicking a row opens the detail view.
* The wheel scrolls lists and panes; in the editor, a click places the cursor.

## Filters and search

Adjacent tokens are combined with **AND**; `|` (or the word `OR`) means **OR**;
parentheses group them.

| Token | Meaning |
|---|---|
| `users` | substring in the URL (case-insensitive) |
| `/v1/.*json/` | regular expression against the full URL |
| `method:GET,POST` | method |
| `status:2xx`, `status:404`, `status:>=400` | response code |
| `status:0`, `status:err` | flows that never got a response |
| `host:api.example.com` | substring in the host |
| `body:token` | substring in the request or response body |
| `tag:mock` | flow tag (`mock`, `redirect`, `edit`, `tunnel`, `break`, `upgrade`) |
| `scheme:https` | scheme |
| `!token` | negation of any token |
| `a \| b`, `a OR b` | OR |
| `(a \| b) c` | grouping with parentheses |

Examples:

```
method:GET status:>=400 !host:cdn /api/v1/          # AND
/users$/ | /orders$/                                # OR
(method:GET | method:POST) status:5xx               # (GET or POST) and 5xx
host:api.example.com | host:cdn.example.com         # two hosts
```

While the filter input is open the list still scrolls: `↑`/`↓`/`PgUp`/`PgDn`
move the selection, while `←`/`→`/`Home`/`End` move the caret inside the filter
bar. Clicking a row with the mouse works too, without closing the input. A
regular expression inside `/…/` must not contain spaces — a space counts as a
token separator.

## Rules

A rule is a single line of DSL:

```
[METHOD] URL_REGEX :: action[=argument] :: action[=argument] ...
```

`METHOD` is `*`, a specific method, or a comma-separated list. `URL_REGEX` is
tested against both the full URL and path+query, so both of these work:
`^https://api\.example\.com/v1` and `^/v1`.

Actions:

| Action | What it does |
|---|---|
| `file=/tmp/mock.json` | reply with the contents of a local file (no request is sent to the server) |
| `redirect=https://staging/…` | the proxy fetches another address itself and returns its response |
| `location=https://other/…` | reply with a real `302` to another address |
| `status=503` | override the response status |
| `body={"down":true}` | override the response body |
| `header=X-Debug=1` | set a request header |
| `delheader=Cookie` | delete a request header |
| `resheader=X-Env=test` | set a response header |
| `delresheader=Set-Cookie` | delete a response header |
| `break=req` \| `resp` \| `both` | pause for manual editing |
| `block=403` | reject the request |
| `delay=750ms` | add latency |
| `maplocal=/static/=/var/www` | serve a directory under a URL prefix |

Examples:

```
GET ^http://127\.0\.0\.1:8080/v1/users$  :: file=/tmp/users.json
GET .*/legacy-endpoint$                  :: redirect=https://staging.example.com/legacy-endpoint
*   ^http://cdn\.example\.net/logo\.png  :: location=https://cdn.example.com/logo.png
*   example\.com                          :: header=X-Debug=1 :: break=both
*   ^http://api\.example\.com/flaky       :: status=503 :: body={"maintenance":true}
*   ^http://ads\.example\.com/            :: block=204
*   ^http://slow\.example\.com/           :: delay=1500ms
*   ^http://app\.example\.com/static/     :: maplocal=/static/=/var/www/app
```

Rules are stored in `~/.cli-proxy/rules.json` and saved on every change. Bulk
import:

```sh
./cli-proxy -import-rules rules.txt
```

They can also be created straight from the flow list: `m` (a mock from the
response body), `M` (a redirect), `i` (an intercept) — the rule is inserted
with the URL escaped.

## Breakpoints

A rule with `break=req`, `break=resp` or `break=both` (or the global toggles
`b` / `B`) pauses an exchange. The UI automatically opens an editor with the
raw HTTP message:

```
╭─ breakpoint #1 · REQUEST — edit headers, method, URL or body, then send ─╮
│   1│ POST /v1/orders HTTP/1.1                                            │
│   2│ Host: api.example.com                                               │
│   3│ Content-Type: application/json                                      │
│   4│                                                                     │
│   5│ {"amount":10,"currency":"EUR"}                                      │
╰──────────────────────────────────────────────────────────────────────────╯
```

`Ctrl-S` sends the edited message, `Esc` discards it (the client receives a
`502`). The body length is recomputed automatically, so `Content-Length` needs
no attention. Several paused exchanges can be held at once — they queue up, and
the counter is visible in the header.

## Flags

| Flag | Default | Description |
|---|---|---|
| `-addr` | `0.0.0.0:8080` | listening address |
| `-port` | `0` (taken from `-addr`) | port; overrides the port in `-addr`, the host is kept |
| `-tunnel` | `false` | do not decrypt TLS, only tunnel CONNECT |
| `-ca-dir` | `~/.cli-proxy` | directory holding the root certificate and key |
| `-rules` | `<ca-dir>/rules.json` | rules file |
| `-filters` | `<ca-dir>/filters.json` | saved filters file |
| `-max-body` | `8388608` | maximum body bytes per message |
| `-flows` | `2000` | how many flows to keep in memory |
| `-insecure` | `false` | do not verify the upstream server's certificate |
| `-ca-bundle` | | extra PEM bundle of trusted roots |
| `-keep-encoding` | `false` | leave `Accept-Encoding` alone (brotli/zstd bodies will be shown as binary) |
| `-system-proxy` | `false` | point this machine's system proxy at cli-proxy and restore the previous settings on exit |
| `-install-cert` | `false` | trust the root certificate and exit |
| `-uninstall-cert` | `false` | remove the root certificate from the trust store and exit |
| `-cert-system` | `false` | with `-install-cert`/`-uninstall-cert` — for all users |
| `-headless` | `false` | no UI, flow lines on stdout |
| `-import-rules` | | import a file of DSL rules and exit |
| `-version` | | version |

## Layout

```
main.go                    flags, banner, headless mode
internal/ca/               root CA, leaf certificates, cache
internal/core/
  flow.go                  request/response model, raw message rendering
  store.go                 ring buffer of flows with change subscriptions
  rules.go                 rule DSL, storage, matching
  filter.go                saved filters with on-disk storage
  breakpoint.go            queue of paused exchanges
  query.go                 filter language
  log.go                   engine log
internal/proxy/
  proxy.go                 HTTP proxy, CONNECT, MITM, WebSocket, rules
  local.go                 /cert, /cert.der, /ca.mobileconfig, /status
internal/sysproxy/         system proxy: snapshot, apply, restore
internal/trust/            root certificate installation and removal
internal/term/             raw mode, window size, key and SGR mouse parsing
internal/tui/
  screen.go                cell buffer and diff rendering to ANSI
  theme.go                 palette and rounded frames
  table.go                 tables and boxes with rounded corners
                           (in views.go — panes with tabs, word wrapping,
                            JSON colouring, two-column help)
  editor.go                raw HTTP editor
  app.go                   state, event loop, keyboard and mouse
  views.go                 rendering of every screen
```

## Development

```sh
make build     # build
make test      # run the tests
make vet       # go vet
make fmt       # gofmt
make cross     # build for every supported platform
```

### Continuous integration

`.github/workflows/ci.yml`, on every push to `master` and on pull requests:

* `go vet`, `gofmt -l` and `go test ./...` on **Linux, macOS and Windows**;
* a separate `-race` run on Linux;
* cross-compilation for 7 targets (linux/amd64, linux/arm64, darwin/amd64,
  darwin/arm64, windows/amd64, windows/arm64, freebsd/amd64).

### Cutting a release

`.github/workflows/release.yml` reacts to a `v*` tag:

1. it checks the version in `main.go` against the tag and fails if they
   disagree;
2. it builds the archives with GoReleaser (`goreleaser release`), verifies the checksums and runs the
   built Linux binary;
3. it publishes a GitHub Release with the archives, `checksums.txt` and the
   list of commits since the previous tag.

```sh
# bump the version in main.go, commit, then:
git tag -a v0.2.0 -m "cli-proxy v0.2.0"
git push origin v0.2.0
```

The workflow can also be started manually from the Actions tab — in that case
it builds the archives and attaches them to the run's artifacts without
publishing anything.

## Tests

```sh
go test ./...
```

Covered: end-to-end proxying of HTTP and HTTPS with interception, every rule
action, refusing unreadable encodings and the short certificate URL, `AND`/`OR`/
parentheses in filters, list navigation with the filter open, parsing mouse
hover events, pane tabs and collapsing, JSON formatting and wrapping, saved
filters and how they merge with the filter bar, the two-column help, the system
proxy lifecycle (snapshot, rollback on error, recovery after an unclean
shutdown), installing and removing the root certificate including the background
run and command construction with escaping, editing a request and a response
through a breakpoint, discarding an exchange, tunnel mode, serving the
certificate, leaf certificate properties, the DSL and filters, changing the port
on the fly (including refusing a busy port and preserving flows), parsing
keyboard escape sequences and SGR mouse events, the geometry of the rounded
tables, full rendering of every screen, button availability at any width, and
mouse handling.

[rel]: https://github.com/sadgoodman/cli-proxy/releases/latest
[tap]: https://github.com/sadgoodman/homebrew-tap
[scoop]: https://github.com/sadgoodman/scoop-bucket
