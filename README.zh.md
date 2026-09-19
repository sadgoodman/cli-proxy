# cli-proxy

[![CI](https://github.com/sadgoodman/cli-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/sadgoodman/cli-proxy/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/sadgoodman/cli-proxy)](https://github.com/sadgoodman/cli-proxy/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.25-00ADD8?logo=go&logoColor=white)](go.mod)

[English](README.md) · [Русский](README.ru.md) · **中文** · [Español](README.es.md)

一款轻量级 CLI 代理，用于查看和改写 HTTP 与 HTTPS 流量，并提供一个同时支持
键盘和鼠标操作的终端 UI。单个静态二进制文件，**没有任何外部依赖** —— 只用标准库。

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

## 亮点

- **实时拦截** HTTP 与 HTTPS。内置的根 CA 会即时为每个主机签发证书，HTTP/2 也能
  通过 ALPN 正常工作。当你只想看流量去了哪里时，`-tunnel` 会让 CONNECT 保持原样透传。
- **断点**可以暂停请求或响应，并以原始 HTTP 报文的形式打开编辑。报文体原样保留，
  `Content-Length` 会重新计算，因此内容不会被截断。
- **Mock 与改写**由一套紧凑的规则 DSL 驱动：返回本地文件、到另一个地址取回响应、
  返回真正的 302、覆盖状态码或响应体、改写请求头、注入延迟、直接阻断。捕获到一条
  flow 后，按一个键就能据此创建规则。
- **搜索与过滤**支持按 URL、method、status、host、payload 和 tag 进行匹配，并支持
  取反、`AND`、`OR` 和括号。已保存的过滤器在切换标签页时始终保持启用，重启后也依然有效。
- **可读的 payload。** JSON 会重新缩进并着色，过长的行按单词边界换行而不是直接截断，
  代理也不会再向服务器请求自己无法解码的编码格式。
- **请求与响应面板**带有 `headers` / `body` / `raw` 三个标签页，左右并排、各自可折叠
  —— 在 flow 列表和完整详情视图中都是如此。
- **可为任意设备代理。** 一个短链接（`http://cli.proxy/ssl`）、一张可安装的根证书、
  一个 iOS 描述文件，以及各平台的操作说明。证书按一个键即可信任，系统代理可以指向
  本机，并且会在退出时恢复，即使进程被强制杀死也不会遗漏。
- **极简但功能完整的 UI。** 圆角表格，六个标签页可用方向键、数字键或鼠标切换，所有
  位置都有悬停高亮。

## 安装

```sh
brew install sadgoodman/tap/cli-proxy
```

Windows 上使用 [Scoop](https://scoop.sh)，无需管理员权限：

```powershell
scoop bucket add sadgoodman https://github.com/sadgoodman/scoop-bucket
scoop install cli-proxy
```

其他情况请从[最新发布][rel]下载预编译压缩包，或使用 Go 1.25+ 从源码构建。

| 平台 | 资源文件 |
|---|---|
| Linux x86-64 | `cli-proxy_<version>_linux_amd64.tar.gz` |
| Linux arm64 | `cli-proxy_<version>_linux_arm64.tar.gz` |
| macOS Apple 芯片 | `cli-proxy_<version>_darwin_arm64.tar.gz` |
| macOS Intel | `cli-proxy_<version>_darwin_amd64.tar.gz` |
| Windows x86-64 | `cli-proxy_<version>_windows_amd64.zip` |

同一发布中的 `checksums.txt` 保存了每个压缩包的 SHA-256 校验值。

```sh
tar -xzf cli-proxy_<version>_darwin_arm64.tar.gz

./cli-proxy                  # terminal UI on 0.0.0.0:8080
./cli-proxy -install-cert    # trust the root certificate (no password on macOS)
./cli-proxy -system-proxy    # route this machine through it, undone on exit
```

从源码构建：

```sh
git clone https://github.com/sadgoodman/cli-proxy.git
cd cli-proxy && make build && ./cli-proxy
```

## 文档

完整参考手册 —— 涵盖所有 flag、规则 DSL、过滤语法、快捷键绑定、证书与系统代理的
处理方式，以及项目结构 —— 提供**英文**和**俄文**两个版本：
**[docs/guide.md](docs/guide.md)**、**[docs/guide.ru.md](docs/guide.ru.md)**。

## 状态

`go test ./...` 覆盖了端到端的 HTTP 与 HTTPS 拦截、每一条规则动作、断点编辑、隧道
模式、证书安装、已保存的过滤器、面板标签页、JSON 格式化与换行、键盘与鼠标事件解析，
以及表格布局计算。CI 会在 Linux、macOS 和 Windows 上运行整个测试套件，另外还有一次
`-race` 检测和一次交叉编译检查。

欢迎贡献 —— 参见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 许可证

[MIT](LICENSE)

[rel]: https://github.com/sadgoodman/cli-proxy/releases/latest
