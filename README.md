# cli-proxy

[![CI](https://github.com/sadgoodman/cli-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/sadgoodman/cli-proxy/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/sadgoodman/cli-proxy)](https://github.com/sadgoodman/cli-proxy/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.25-00ADD8?logo=go&logoColor=white)](go.mod)

**English** · [Русский](README.ru.md) · [中文](README.zh.md) · [Español](README.es.md)

A lightweight CLI proxy for watching and rewriting HTTP and HTTPS traffic, with
a keyboard- and mouse-driven terminal UI. One static binary, **no external
dependencies** — standard library only.

```
╭─ cli-proxy ───────────────────────────────────── 0.0.0.0:8080 · MITM · 128 flows ─╮
│  Flows   Filters   Rules   Cert   Log   Help                                       │
├────────────────────────────────────────────────────────────────────────────────────┤
│╭─ flows · 128 shown / 128 captured ─────────┬───────────────────┬────────┬────────╮ │
││    # │ Time     │ Method │ Host            │ Path              │ Status │   Size │ │
│├──────┼──────────┼────────┼─────────────────┼───────────────────┼────────┼────────┤ │
││    1 │ 12:04:11 │ GET    │ api.example.com │ /v1/users         │    200 │   1.2K │ │
││    2 │ 12:04:11 │ POST   │ api.example.com │ /v1/orders        │    201 │    84B │ │
││    3 │ 12:04:12 │ GET    │ cdn.example.net │ /logo.png         │    404 │    0B  │ │
│╰──────┴──────────┴────────┴─────────────────┴───────────────────┴────────┴────────╯ │
╰──[↵]open──[f]filter──[b]brk-req──[m]mock──[M]redir──[i]break──[s]save──[q]quit──────╯
```

## Highlights

- **Real-time interception** of HTTP and HTTPS. A bundled root CA mints a
  per-host certificate on the fly, and HTTP/2 works over ALPN. `-tunnel` keeps
  CONNECT opaque when you only want to see where traffic goes.
- **Breakpoints** pause a request or a response and open the raw HTTP message
  for editing. The body is taken verbatim and `Content-Length` is recomputed,
  so nothing is truncated.
- **Mocks and rewrites** from a compact rule DSL: serve a local file, fetch the
  response from another address, return a real 302, override the status or
  body, rewrite headers, add latency, block. A rule can be created from a
  captured flow with one key.
- **Search and filters** over URL, method, status, host, payload and tags, with
  negation, `AND`, `OR` and parentheses. Saved filters stay switched on while
  you move between tabs and survive a restart.
- **Readable payloads.** JSON is re-indented and coloured, long lines wrap on
  word boundaries instead of being cut off, and the proxy stops asking servers
  for encodings it cannot decode.
- **Request and response panes** with `headers` / `body` / `raw` tabs, side by
  side, each collapsible — in the flow list and in the full detail view.
- **Proxy any device.** A short URL (`http://cli.proxy/ssl`), an installable
  root certificate, an iOS profile and per-platform instructions. The
  certificate can be trusted with one key, and the system proxy can be pointed
  here and is restored on exit, even after a hard kill.
- **Minimalist but complete UI.** Rounded-corner tables, six tabs driven by the
  arrow keys, number keys or the mouse, with hover highlighting everywhere.

## Install

```sh
brew install sadgoodman/tap/cli-proxy
```

On Windows, with [Scoop](https://scoop.sh) — no administrator rights needed:

```powershell
scoop bucket add sadgoodman https://github.com/sadgoodman/scoop-bucket
scoop install cli-proxy
```

Anywhere else, download a prebuilt archive from the [latest release][rel], or
build from source with Go 1.25+.

| Platform | Asset |
|---|---|
| Linux x86-64 | `cli-proxy_<version>_linux_amd64.tar.gz` |
| Linux arm64 | `cli-proxy_<version>_linux_arm64.tar.gz` |
| macOS Apple Silicon | `cli-proxy_<version>_darwin_arm64.tar.gz` |
| macOS Intel | `cli-proxy_<version>_darwin_amd64.tar.gz` |
| Windows x86-64 | `cli-proxy_<version>_windows_amd64.zip` |

`checksums.txt` in the same release carries the SHA-256 of every archive.

```sh
tar -xzf cli-proxy_<version>_darwin_arm64.tar.gz

./cli-proxy                  # terminal UI on 0.0.0.0:8080
./cli-proxy -install-cert    # trust the root certificate (no password on macOS)
./cli-proxy -system-proxy    # route this machine through it, undone on exit
```

Build from source:

```sh
git clone https://github.com/sadgoodman/cli-proxy.git
cd cli-proxy && make build && ./cli-proxy
```

## Documentation

The full reference — every flag, the rule DSL, filter syntax, key bindings,
certificate and system proxy handling, and the project layout — lives in
**[docs/guide.md](docs/guide.md)** (also in [Russian](docs/guide.ru.md)).

Both versions are published as a site at
**<https://sadgoodman.github.io/cli-proxy/>**.

## Status

`go test ./...` covers end-to-end HTTP and HTTPS interception, every rule
action, breakpoint editing, tunnel mode, certificate installation, saved
filters, the pane tabs, JSON formatting and wrapping, keyboard and mouse
parsing, and the table geometry. CI runs the suite on Linux, macOS and Windows,
plus a `-race` pass and a cross-compilation check.

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)

[rel]: https://github.com/sadgoodman/cli-proxy/releases/latest
