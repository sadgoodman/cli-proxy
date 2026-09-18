# Security policy

## What this project is

`cli-proxy` is an intercepting HTTP/HTTPS proxy. Running it is equivalent to
installing a man-in-the-middle on the machine (or network) it serves, and
installing its root certificate means every TLS connection that trusts that
root can be read by whoever holds the CA key.

That is the point of the tool. Two consequences are worth stating plainly:

- **The CA private key is the whole game.** It is written to
  `<ca-dir>/ca-key.pem` with mode `0600`. Anyone who can read that file can
  impersonate any host to any client that trusts the root. Keep it off shared
  machines, out of backups you do not control, and out of the repository.
- **Run it on networks you control.** Binding to `0.0.0.0` exposes the proxy to
  everyone who can reach the host, and the administrative pages (`/ssl`,
  `/cert`, `/status`) are served without authentication. Use `-addr
  127.0.0.1:8080` when you do not need other devices.

`-insecure` disables verification of origin certificates and `-keep-encoding`
passes through encodings that cannot be inspected. Both are opt-in and both
weaken the guarantees above.

## Reporting a vulnerability

Please do not open a public issue. Use GitHub's private reporting:

**Security → Report a vulnerability** on
<https://github.com/sadgoodman/cli-proxy/security/advisories/new>

Useful reports include the version (`cli-proxy -version`), the platform, a
description of the impact, and the smallest reproducer you can manage.

This is a personal project, so there is no formal response deadline, but
security reports are looked at first. You will get an acknowledgement and, if
the report is valid, credit in the release notes unless you prefer otherwise.

## Supported versions

Only the latest release receives fixes. Upgrade with the archives from the
[releases page](https://github.com/sadgoodman/cli-proxy/releases/latest).
