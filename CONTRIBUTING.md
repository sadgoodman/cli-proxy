# Contributing

Thanks for taking a look. This is a small, deliberately dependency-free tool,
so the bar for new code is "does it earn its place".

## Getting started

```sh
git clone https://github.com/sadgoodman/cli-proxy.git
cd cli-proxy
make build     # or: go build .
make test      # go test ./...
make vet       # go vet ./...
make fmt       # gofmt -w .
```

Go 1.25 or newer is required. There is nothing else to install: the module has
no dependencies, and CI enforces that indirectly by building from a clean
checkout.

Try your change against real traffic before opening a pull request:

```sh
./cli-proxy -addr 127.0.0.1:8080
curl -x http://127.0.0.1:8080 http://example.com
```

## What CI expects

Every pull request must pass:

- `gofmt -l .` reporting nothing;
- `go vet ./...`;
- `go test ./...` on Linux, macOS and Windows;
- `go test ./... -race` on Linux;
- cross-compilation for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64,
  windows/amd64, windows/arm64 and freebsd/amd64.

`master` is protected: changes land through a pull request, not by pushing
directly.

## House style

- **Standard library only.** A new dependency needs a very good reason; say so
  in the pull request description.
- **Tests come with the change.** Behaviour that a reviewer cannot see in the
  UI should be covered by a test.
- **Comments explain why, not what.** The code already says what it does.
- **Keep the interface small.** Prefer extending an existing view or key over
  adding a new one.
- Match the existing formatting; `gofmt` decides.

## Reporting bugs

Open an issue with the bug report template. The most useful reports include:

- the platform and the `cli-proxy -version` output;
- what you did, what you expected, what happened;
- the relevant lines from the **Log** tab, and whether the traffic was plain
  HTTP or HTTPS.

## Security

Please do not report security problems in a public issue — see
[SECURITY.md](SECURITY.md).
