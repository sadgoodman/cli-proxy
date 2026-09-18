---
title: cli-proxy
---

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

## Start here

- **[Full guide (English)](guide.md)** — every flag, the rule DSL, filter
  syntax, key bindings, certificates and the system proxy.
- **[Полное руководство (русский)](guide.ru.md)**
- **[Overview (English)](../README.md)** ·
  **[Обзор (русский)](../README.ru.md)** ·
  **[中文](../README.zh.md)** ·
  **[Español](../README.es.md)**

## Quick start

```sh
./cli-proxy                  # terminal UI on 0.0.0.0:8080
./cli-proxy -install-cert    # trust the root certificate
./cli-proxy -system-proxy    # route this machine through it, undone on exit
```

## What it does

- Real-time interception of HTTP and HTTPS, with a bundled root CA that mints a
  per-host certificate on the fly and HTTP/2 over ALPN.
- Breakpoints that pause a request or a response and open the raw HTTP message
  for editing.
- Mocks and rewrites from a compact DSL: local files, other addresses, real
  redirects, status and body overrides, header rewrites, latency, blocking.
- Filters with `AND`, `OR`, parentheses and negation, plus saved filters that
  stay switched on across tabs and restarts.
- JSON bodies re-indented and coloured, with long lines wrapped instead of cut.
- Proxy any device: a short URL, an installable root certificate, an iOS
  profile and per-platform instructions.

## Download

Prebuilt archives for Linux, macOS and Windows are on the
[releases page](https://github.com/sadgoodman/cli-proxy/releases/latest), each
with a SHA-256 in `checksums.txt`.

## Source

<https://github.com/sadgoodman/cli-proxy> — MIT licensed.
